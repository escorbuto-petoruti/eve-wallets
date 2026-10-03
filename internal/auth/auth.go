// Package auth obtains ESI access tokens from the external eve-auth CLI.
//
// This application never stores tokens: each call asks eve-auth for a fresh
// access token. Tokens are never logged, never placed on a command line and
// never included in error messages.
package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultBin     = "eve-auth"
	defaultTimeout = 30 * time.Second
	maxStdoutBytes = 64 << 10
	maxStderrBytes = 4 << 10
	maxErrDetail   = 200 // longest stderr excerpt placed in an error
	emptyStoreMsg  = "No characters saved."
)

// Character is an authenticated EVE character with a usable token.
type Character struct {
	ID     int64
	Name   string
	Scopes []string
	// UserID is the app user the character belongs to; 0 when the source has no
	// notion of users (the eve-auth source).
	UserID int64
}

// TokenSource lists the available characters and yields access tokens.
type TokenSource interface {
	Characters(ctx context.Context) ([]Character, error)
	Token(ctx context.Context, characterID int64) (string, error)
}

// Result is the outcome of one command run. ExitCode is non-zero when the
// command ran and failed.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner executes bin with args and the extra env entries (added to the
// parent environment). It returns an error only when the command could not
// run to completion, for instance it was not found or the context ended.
type Runner func(ctx context.Context, bin string, args, env []string) (Result, error)

// Options configures EveAuth. Zero values select defaults.
type Options struct {
	Bin     string        // executable, default "eve-auth"
	Env     []string      // extra KEY=VALUE entries, such as EVE_CLIENT_ID
	Run     Runner        // command runner, default runs the real binary
	Timeout time.Duration // per command, default 30s
}

// EveAuth is a TokenSource backed by the eve-auth binary.
type EveAuth struct {
	bin     string
	env     []string
	run     Runner
	timeout time.Duration
}

var _ TokenSource = (*EveAuth)(nil)

// NewEveAuth returns an EveAuth for the given options.
func NewEveAuth(opts Options) *EveAuth {
	a := &EveAuth{bin: opts.Bin, env: opts.Env, run: opts.Run, timeout: opts.Timeout}
	if a.bin == "" {
		a.bin = defaultBin
	}
	if a.run == nil {
		a.run = execRun
	}
	if a.timeout <= 0 {
		a.timeout = defaultTimeout
	}
	return a
}

// Characters runs `eve-auth list` and parses its output.
func (a *EveAuth) Characters(ctx context.Context) ([]Character, error) {
	res, err := a.exec(ctx, "list")
	if err != nil {
		return nil, fmt.Errorf("auth: list characters: %w", err)
	}
	return parseList(string(res.Stdout))
}

// Token runs `eve-auth token <id>` and returns the access token.
func (a *EveAuth) Token(ctx context.Context, characterID int64) (string, error) {
	res, err := a.exec(ctx, "token", strconv.FormatInt(characterID, 10))
	if err != nil {
		return "", fmt.Errorf("auth: token for character %d: %w", characterID, err)
	}
	tok, err := parseToken(res.Stdout)
	if err != nil {
		return "", fmt.Errorf("auth: token for character %d: %w", characterID, err)
	}
	return tok, nil
}

// exec runs one eve-auth command and converts failures into scrubbed errors.
func (a *EveAuth) exec(ctx context.Context, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	res, err := a.run(ctx, a.bin, args, a.env)
	res.Stdout = truncate(res.Stdout, maxStdoutBytes)
	res.Stderr = truncate(res.Stderr, maxStderrBytes)
	if err != nil {
		// The runner error comes from starting or waiting for the process and
		// carries no output; the context error stays reachable via errors.Is.
		return res, fmt.Errorf("run %s: %w", a.bin, err)
	}
	if res.ExitCode != 0 {
		detail := scrubStderr(res.Stderr, res.Stdout)
		if detail == "" {
			return res, fmt.Errorf("%s exited with exit status %d", a.bin, res.ExitCode)
		}
		return res, fmt.Errorf("%s exited with exit status %d: %s", a.bin, res.ExitCode, detail)
	}
	return res, nil
}

func parseList(out string) ([]Character, error) {
	var chars []Character
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, emptyStoreMsg) {
			continue
		}
		c, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("auth: parse list output line %d: %w", i+1, err)
		}
		chars = append(chars, c)
	}
	return chars, nil
}

func parseLine(line string) (Character, error) {
	fields := strings.Split(line, "\t")
	if len(fields) < 3 {
		return Character{}, errors.New("want at least 3 tab-separated fields")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
	if err != nil || id <= 0 {
		return Character{}, errors.New("invalid character id")
	}
	name := strings.TrimSpace(fields[1])
	if name == "" {
		return Character{}, errors.New("empty character name")
	}
	sc := strings.TrimSpace(fields[2])
	if !strings.HasPrefix(sc, "[") || !strings.HasSuffix(sc, "]") {
		return Character{}, errors.New("scopes field must be enclosed in brackets")
	}
	return Character{ID: id, Name: name, Scopes: strings.Fields(sc[1 : len(sc)-1])}, nil
}

// parseToken accepts a single token optionally followed by one newline.
func parseToken(out []byte) (string, error) {
	s := strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r")
	if s == "" {
		return "", errors.New("eve-auth returned an empty token")
	}
	if len(out) >= maxStdoutBytes {
		return "", errors.New("eve-auth output is too large to be a token")
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("eve-auth output is not a single token")
		}
	}
	return s, nil
}

func truncate(b []byte, max int) []byte {
	if len(b) > max {
		return b[:max]
	}
	return b
}

// scrubStderr turns stderr into a short single-line excerpt: control
// characters become spaces, whitespace is collapsed, anything that equals the
// stdout payload (a possible token) is removed, and the length is bounded.
func scrubStderr(stderr, stdout []byte) string {
	s := string(stderr)
	if tok := strings.TrimSpace(string(stdout)); tok != "" {
		s = strings.ReplaceAll(s, tok, "[redacted]")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrDetail {
		s = strings.ToValidUTF8(s[:maxErrDetail], "") + "..."
	}
	return s
}

// limitedBuffer keeps at most max bytes and silently drops the rest.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// execRun runs the real binary without a shell.
func execRun(ctx context.Context, bin string, args, env []string) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	stdout := &limitedBuffer{max: maxStdoutBytes}
	stderr := &limitedBuffer{max: maxStderrBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil && ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, err
}
