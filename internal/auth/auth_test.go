package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const secretToken = "tok-SECRET-abc123"

type call struct {
	bin  string
	args []string
	env  []string
}

// fakeRunner returns a Runner that records calls and answers with res/err.
func fakeRunner(res Result, err error, calls *[]call) Runner {
	return func(ctx context.Context, bin string, args, env []string) (Result, error) {
		if calls != nil {
			*calls = append(*calls, call{bin, args, env})
		}
		return res, err
	}
}

func TestNewEveAuthDefaultBinary(t *testing.T) {
	var calls []call
	a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte("No characters saved. Run `eve-auth login`.\n")}, nil, &calls)})
	if _, err := a.Characters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls[0].bin != "eve-auth" || !reflect.DeepEqual(calls[0].args, []string{"list"}) {
		t.Fatalf("call = %+v", calls[0])
	}
}

func TestCharactersParsing(t *testing.T) {
	out := "42\tAlice One\t[esi-wallet.read_character_wallet.v1 esi-wallet.read_corporation_wallets.v1]\taccess token expires 2026-10-03T12:00:00Z\n" +
		"\n" +
		"43\tBob\t[]\taccess token expires 2026-10-03T12:00:00Z\n"
	var calls []call
	a := NewEveAuth(Options{Bin: "/opt/eve-auth", Env: []string{"EVE_CLIENT_ID=abc"}, Run: fakeRunner(Result{Stdout: []byte(out)}, nil, &calls)})
	got, err := a.Characters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Character{
		{ID: 42, Name: "Alice One", Scopes: []string{"esi-wallet.read_character_wallet.v1", "esi-wallet.read_corporation_wallets.v1"}},
		{ID: 43, Name: "Bob", Scopes: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if calls[0].bin != "/opt/eve-auth" || !reflect.DeepEqual(calls[0].env, []string{"EVE_CLIENT_ID=abc"}) {
		t.Fatalf("call = %+v", calls[0])
	}
}

func TestCharactersEmptyStore(t *testing.T) {
	for _, out := range []string{"No characters saved. Run `eve-auth login`.\n", "", "\n"} {
		a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte(out)}, nil, nil)})
		got, err := a.Characters(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("out %q: got %v, %v", out, got, err)
		}
	}
}

func TestCharactersMalformed(t *testing.T) {
	tests := map[string]string{
		"too few fields": "42\tAlice\n",
		"bad id":         "abc\tAlice\t[a]\texpires\n",
		"zero id":        "0\tAlice\t[a]\texpires\n",
		"no brackets":    "42\tAlice\ta b\texpires\n",
		"empty name":     "42\t\t[a]\texpires\n",
	}
	for name, out := range tests {
		t.Run(name, func(t *testing.T) {
			a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte(out)}, nil, nil)})
			_, err := a.Characters(context.Background())
			if err == nil || !strings.Contains(err.Error(), "line 1") {
				t.Fatalf("err = %v, want line 1 error", err)
			}
		})
	}
}

func TestTokenSuccess(t *testing.T) {
	var calls []call
	a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte(secretToken + "\n")}, nil, &calls)})
	got, err := a.Token(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got != secretToken {
		t.Fatalf("token = %q", got)
	}
	if !reflect.DeepEqual(calls[0].args, []string{"token", "42"}) {
		t.Fatalf("args = %v", calls[0].args)
	}
}

func TestTokenRejectsBadOutput(t *testing.T) {
	tests := map[string]string{
		"empty":      "",
		"only nl":    "\n",
		"inner nl":   "abc\ndef\n",
		"inner sp":   "abc def\n",
		"inner tab":  "abc\tdef\n",
		"two nl":     "abc\n\n",
		"leading sp": " abc\n",
	}
	for name, out := range tests {
		t.Run(name, func(t *testing.T) {
			a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte(out)}, nil, nil)})
			_, err := a.Token(context.Background(), 1)
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), "abc") {
				t.Fatalf("error leaks output: %v", err)
			}
		})
	}
}

func TestTokenNonZeroExit(t *testing.T) {
	stderr := "refresh failed: bad things\x1b[31m\nsecond line " + strings.Repeat("x", 2000)
	a := NewEveAuth(Options{Run: fakeRunner(Result{ExitCode: 3, Stdout: []byte(secretToken + "\n"), Stderr: []byte(stderr + secretToken)}, nil, nil)})
	_, err := a.Token(context.Background(), 42)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Contains(msg, secretToken) {
		t.Fatalf("error leaks token: %v", msg)
	}
	if !strings.Contains(msg, "exit status 3") || !strings.Contains(msg, "refresh failed") {
		t.Fatalf("error = %v", msg)
	}
	if strings.ContainsAny(msg, "\n\x1b") {
		t.Fatalf("error not scrubbed: %q", msg)
	}
	if len(msg) > 400 {
		t.Fatalf("error not bounded: %d bytes", len(msg))
	}
}

func TestRunnerStartError(t *testing.T) {
	a := NewEveAuth(Options{Run: fakeRunner(Result{}, errors.New("exec: not found"), nil)})
	if _, err := a.Token(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.Characters(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestListNonZeroExit(t *testing.T) {
	a := NewEveAuth(Options{Run: fakeRunner(Result{ExitCode: 1, Stderr: []byte("boom")}, nil, nil)})
	_, err := a.Characters(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextTimeout(t *testing.T) {
	blocking := func(ctx context.Context, bin string, args, env []string) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	a := NewEveAuth(Options{Run: blocking, Timeout: 20 * time.Millisecond})
	_, err := a.Token(context.Background(), 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestOutputBounded(t *testing.T) {
	huge := strings.Repeat("a", maxStdoutBytes*2)
	a := NewEveAuth(Options{Run: fakeRunner(Result{Stdout: []byte(huge)}, nil, nil)})
	if _, err := a.Token(context.Background(), 1); err == nil {
		t.Fatal("want error for oversized token")
	}
}
