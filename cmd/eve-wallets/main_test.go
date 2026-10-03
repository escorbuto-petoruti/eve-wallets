package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
	"github.com/escorbuto-petoruti/eve-wallets/internal/web"
)

const charScope = "esi-wallet.read_character_wallet.v1"

type fakeTokens struct {
	chars   []auth.Character
	listErr error
	calls   int
	mu      sync.Mutex
}

func (f *fakeTokens) Characters(context.Context) ([]auth.Character, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.chars, f.listErr
}

func (f *fakeTokens) Token(context.Context, int64) (string, error) { return "secret-token", nil }

func (f *fakeTokens) listCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeESI struct{}

func (fakeESI) CharacterWallet(context.Context, string, int64) (int64, error) { return 123456, nil }
func (fakeESI) CharacterCorporationID(context.Context, int64) (int64, error) {
	return 0, errors.New("unused")
}
func (fakeESI) CorporationName(context.Context, int64) (string, error) {
	return "", errors.New("unused")
}
func (fakeESI) CorporationWallets(context.Context, string, int64) ([]esi.DivisionBalance, error) {
	return nil, errors.New("unused")
}

func (fakeESI) CorporationDivisions(context.Context, string, int64) (esi.DivisionNames, error) {
	return nil, errors.New("unused")
}

func (fakeESI) CharacterJournal(context.Context, string, int64) ([]esi.JournalEntry, error) {
	bal := int64(5000)
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	return []esi.JournalEntry{{ID: 1, Date: at, BalanceCents: &bal}, {ID: 2, Date: at}}, nil
}
func (fakeESI) CorporationJournal(context.Context, string, int64, int) ([]esi.JournalEntry, error) {
	return nil, errors.New("unused")
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	deps      deps
	out, err  *syncBuffer
	tokens    *fakeTokens
	dbPath    string
	listeners chan net.Listener
	opened    *store.Store        // the store openStore returned
	tokensFor *store.Store        // the store newTokens was given
	esi       collector.ESIClient // overrides fakeESI when set
}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	h := &harness{
		out:       &syncBuffer{},
		err:       &syncBuffer{},
		tokens:    &fakeTokens{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}, UserID: 1}}},
		listeners: make(chan net.Listener, 1),
	}
	h.deps = deps{
		stdout: h.out,
		stderr: h.err,
		getenv: func(k string) string { return env[k] },
		openStore: func(path string) (*store.Store, error) {
			h.dbPath = path
			st, err := store.Open(":memory:")
			if err == nil {
				// The fake character below has UserID 1, and LinkWallet enforces
				// its user_id foreign key: create that user so the harness does
				// not record a spurious link failure in every collection.
				if uerr := st.UpsertUser(context.Background(), 1, "Alice", time.Unix(0, 0).UTC()); uerr != nil {
					_ = st.Close()
					return nil, uerr
				}
			}
			h.opened = st
			return st, err
		},
		newTokens: func(st *store.Store) auth.TokenSource {
			h.tokensFor = st
			return h.tokens
		},
		newESI: func(ua string) collector.ESIClient {
			if !strings.HasPrefix(ua, "eve-wallets/") || !strings.HasSuffix(ua, " (local)") {
				t.Errorf("user agent = %q", ua)
			}
			if h.esi != nil {
				return h.esi
			}
			return fakeESI{}
		},
		newSSO: func() web.SSO { return fakeLoginSSO{} },
		listen: func(network, addr string) (net.Listener, error) {
			ln, err := net.Listen(network, addr)
			if err == nil {
				h.listeners <- ln
			}
			return ln, err
		},
	}
	return h
}

func TestUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		out  string // expected on stdout
		err  string // expected on stderr
	}{
		{"no arguments", nil, 2, "", "Usage:"},
		{"help command", []string{"help"}, 2, "", "Usage:"},
		{"unknown command", []string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
		{"long help flag", []string{"--help"}, 0, "Usage:", ""},
		{"short help flag", []string{"-h"}, 0, "Usage:", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if got := run(context.Background(), tt.args, h.deps); got != tt.code {
				t.Errorf("exit = %d, want %d", got, tt.code)
			}
			if !strings.Contains(h.out.String(), tt.out) {
				t.Errorf("stdout = %q, want %q", h.out.String(), tt.out)
			}
			if !strings.Contains(h.err.String(), tt.err) {
				t.Errorf("stderr = %q, want %q", h.err.String(), tt.err)
			}
		})
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, cmd := range []string{"collect", "backfill", "serve"} {
		h := newHarness(t, nil)
		if got := run(context.Background(), []string{cmd, "-h"}, h.deps); got != 0 {
			t.Errorf("%s -h exit = %d, want 0", cmd, got)
		}
	}
}

func TestCheckLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:8088", true},
		{"127.0.0.1:0", true},
		{"[::1]:8088", true},
		{"localhost:8088", true},
		{"0.0.0.0:8088", false},
		{"[::]:8088", false},
		{":8088", false},
		{"example.com:8088", false},
		{"192.168.1.10:8088", false},
		{"127.0.0.1", false},
		{"127.0.0.1:notaport", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := checkLoopbackAddr(tt.addr)
			if (err == nil) != tt.ok {
				t.Errorf("checkLoopbackAddr(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
			}
		})
	}
}

func TestServeRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8088", "[::]:8088", "example.com:8088"} {
		h := newHarness(t, nil)
		if got := run(context.Background(), []string{"serve", "--addr", addr}, h.deps); got != 2 {
			t.Errorf("%s: exit = %d, want 2", addr, got)
		}
		if !strings.Contains(h.err.String(), "loopback") {
			t.Errorf("%s: stderr = %q", addr, h.err.String())
		}
		select {
		case <-h.listeners:
			t.Errorf("%s: must not listen", addr)
		default:
		}
		if h.dbPath != "" {
			t.Errorf("%s: store must not be opened", addr)
		}
	}
}

func TestServeRejectsShortEvery(t *testing.T) {
	for _, every := range []string{"59s", "0s", "-5m", "soon"} {
		h := newHarness(t, nil)
		if got := run(context.Background(), []string{"serve", "--every", every}, h.deps); got != 2 {
			t.Errorf("--every %s: exit = %d, want 2", every, got)
		}
	}
}

func TestResolveDBPath(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  map[string]string
		want string
	}{
		{"flag wins", "/flag.db", map[string]string{"EVE_WALLETS_DB": "/env.db", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, "/flag.db"},
		{"env var", "", map[string]string{"EVE_WALLETS_DB": "/env.db", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, "/env.db"},
		{"xdg data home", "", map[string]string{"XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, "/xdg/eve-wallets/wallets.db"},
		{"home fallback", "", map[string]string{"HOME": "/home/u"}, "/home/u/.local/share/eve-wallets/wallets.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDBPath(tt.flag, func(k string) string { return tt.env[k] })
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := resolveDBPath("", func(string) string { return "" }); err == nil {
		t.Error("no HOME: want error")
	}
}

func TestOpenSecureStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "data", "wallets.db")
	s, err := openSecureStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Force WAL side files to exist.
	if _, err := s.UpsertWallet(context.Background(), store.Wallet{Kind: store.KindCharacter, OwnerID: 1, OwnerName: "A"}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if st, err := os.Stat(side); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", side, st.Mode().Perm())
		}
	}
}

func TestCollectHappyPath(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	if got := run(context.Background(), []string{"collect"}, h.deps); got != 0 {
		t.Fatalf("exit = %d; stderr = %q", got, h.err.String())
	}
	out := h.out.String()
	for _, want := range []string{"Snapshots taken: 1", "Alice", "1234.56", "Skipped", "esi-wallet.read_corporation_wallets.v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+h.err.String(), "secret-token") {
		t.Error("token leaked into the output")
	}
	if h.dbPath != "/home/u/.local/share/eve-wallets/wallets.db" {
		t.Errorf("db path = %q", h.dbPath)
	}
}

func TestCollectBuildsTokenSourceFromTheOpenedStore(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	if got := run(context.Background(), []string{"collect"}, h.deps); got != 0 {
		t.Fatalf("exit = %d; stderr = %q", got, h.err.String())
	}
	if h.opened == nil || h.tokensFor != h.opened {
		t.Errorf("token source built from %p, opened store is %p", h.tokensFor, h.opened)
	}
}

func TestBackfillBuildsTokenSourceFromTheOpenedStore(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	if got := run(context.Background(), []string{"backfill"}, h.deps); got != 0 {
		t.Fatalf("exit = %d; stderr = %q", got, h.err.String())
	}
	if h.opened == nil || h.tokensFor != h.opened {
		t.Errorf("token source built from %p, opened store is %p", h.tokensFor, h.opened)
	}
}

func TestServeBuildsTokenSourceFromTheOpenedStore(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve", "--addr", "127.0.0.1:0"}, h.deps) }()
	ln := <-h.listeners
	defer ln.Close()
	for h.tokens.listCalls() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if got := <-done; got != 0 {
		t.Fatalf("exit = %d; stderr = %q", got, h.err.String())
	}
	if h.opened == nil || h.tokensFor != h.opened {
		t.Errorf("token source built from %p, opened store is %p", h.tokensFor, h.opened)
	}
}

func TestCollectAndBackfillWithoutCharactersPointToTheWebLogin(t *testing.T) {
	for _, cmd := range []string{"collect", "backfill"} {
		h := newHarness(t, map[string]string{"HOME": "/home/u"})
		h.tokens.chars = nil
		if got := run(context.Background(), []string{cmd}, h.deps); got != 1 {
			t.Fatalf("%s: exit = %d, want 1", cmd, got)
		}
		msg := h.err.String()
		for _, want := range []string{"no characters registered", "eve-wallets serve", "http://localhost:8088"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: stderr missing %q: %q", cmd, want, msg)
			}
		}
		if h.out.String() != "" {
			t.Errorf("%s: unexpected stdout %q", cmd, h.out.String())
		}
	}
}

func TestCollectFatalError(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	h.tokens.listErr = errors.New("auth: list characters: boom")
	if got := run(context.Background(), []string{"collect"}, h.deps); got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
	if !strings.Contains(h.err.String(), "boom") {
		t.Errorf("stderr = %q", h.err.String())
	}
}

func TestCollectReportsErrorsAndRateLimit(t *testing.T) {
	var buf bytes.Buffer
	printReport(&buf, collector.Report{
		Errors:      []collector.ItemError{{Owner: "Bob", Err: errors.New("wallet failed")}},
		RateLimited: true,
		RetryAfter:  90 * time.Second,
	})
	for _, want := range []string{"Bob: wallet failed", "rate limit", "1m30s"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestBackfillHappyPath(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	if got := run(context.Background(), []string{"backfill"}, h.deps); got != 0 {
		t.Fatalf("exit = %d; stderr = %q", got, h.err.String())
	}
	out := h.out.String()
	for _, want := range []string{"Wallets processed: 1", "Journal points stored: 1", "Entries without balance: 1", "Alice", "esi-wallet.read_corporation_wallets.v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+h.err.String(), "secret-token") {
		t.Error("token leaked into the output")
	}
	if h.dbPath != "/home/u/.local/share/eve-wallets/wallets.db" {
		t.Errorf("db = %q", h.dbPath)
	}
}

func TestBackfillDBFlag(t *testing.T) {
	h := newHarness(t, nil)
	if got := run(context.Background(), []string{"backfill", "--db", "/x/y.db"}, h.deps); got != 0 || h.dbPath != "/x/y.db" {
		t.Fatalf("exit = %d, db = %q", got, h.dbPath)
	}
}

func TestBackfillFatalError(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	h.tokens.listErr = errors.New("auth: list characters: boom")
	if got := run(context.Background(), []string{"backfill"}, h.deps); got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
	if !strings.Contains(h.err.String(), "boom") {
		t.Errorf("stderr = %q", h.err.String())
	}
}

func TestBackfillUnknownFlag(t *testing.T) {
	h := newHarness(t, nil)
	if got := run(context.Background(), []string{"backfill", "--nope"}, h.deps); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

func TestPrintBackfillReport(t *testing.T) {
	var buf bytes.Buffer
	printBackfillReport(&buf, collector.BackfillReport{
		Wallets: []collector.BackfillWallet{
			{Kind: store.KindCorporation, OwnerName: "Acme", Division: 2, Points: 3, NoBalance: 1},
		},
		Skipped:     []collector.Skip{{Owner: "Corp 9", Reason: "missing corporation role"}},
		Errors:      []collector.ItemError{{Owner: "Bob", Err: errors.New("journal failed")}},
		RateLimited: true,
		RetryAfter:  90 * time.Second,
	})
	for _, want := range []string{"Acme (division 2)", "Skipped: 1", "Corp 9: missing corporation role", "Bob: journal failed", "rate limit", "1m30s"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestFormatCents(t *testing.T) {
	tests := map[int64]string{0: "0.00", 5: "0.05", 123456: "1234.56", -250: "-2.50", -5: "-0.05"}
	for in, want := range tests {
		if got := formatCents(in); got != want {
			t.Errorf("formatCents(%d) = %q, want %q", in, got, want)
		}
	}
}

// startServe runs `serve` on a free port and returns the base URL, the
// cancel func and a channel that yields the exit code.
func startServe(t *testing.T, h *harness, extra ...string) (string, context.CancelFunc, <-chan int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	args := append([]string{"serve", "--addr", "127.0.0.1:0"}, extra...)
	go func() { done <- run(ctx, args, h.deps) }()
	select {
	case ln := <-h.listeners:
		return "http://" + ln.Addr().String(), cancel, done
	case code := <-done:
		t.Fatalf("serve exited early with %d: %s", code, h.err.String())
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start listening")
	}
	return "", nil, nil
}

func getJSON(t *testing.T, c *http.Client, url string) map[string]any {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestServeCollectsAndShutsDownCleanly(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h)
	c := signIn(t, base)

	waitFor(t, "first collection in /api/status", func() bool {
		return getJSON(t, c, base+"/api/status")["snapshots"] == float64(1)
	})
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/ status = %d", resp.StatusCode)
	}
	if !strings.Contains(h.out.String(), base) {
		t.Errorf("stdout should print the URL %s: %q", base, h.out.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d; stderr = %q", code, h.err.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	calls := h.tokens.listCalls()
	time.Sleep(50 * time.Millisecond)
	if h.tokens.listCalls() != calls {
		t.Error("collection loop still running after shutdown")
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
}

func TestServeNoCollect(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h, "--no-collect")
	c := signIn(t, base)
	time.Sleep(50 * time.Millisecond)
	if st := getJSON(t, c, base+"/api/status"); st["snapshots"] != float64(0) || st["taken_at"] != nil {
		t.Errorf("status = %v", st)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d", code)
	}
	if h.tokens.listCalls() != 0 {
		t.Errorf("collector ran %d times with --no-collect", h.tokens.listCalls())
	}
}

func TestServeKeepsServingAfterFailedCollection(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	h.tokens.listErr = errors.New("auth: list characters: boom")
	base, cancel, done := startServe(t, h)
	c := signIn(t, base)
	waitFor(t, "failed run in /api/status", func() bool {
		errs, _ := getJSON(t, c, base+"/api/status")["errors"].([]any)
		return len(errs) == 1 && strings.Contains(fmt.Sprint(errs[0]), "boom")
	})
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d", code)
	}
}

// corpESI adds a corporation with one division whose journal is forbidden, so
// the snapshot succeeds and the backfill reports a skipped item.
type corpESI struct{ fakeESI }

func (corpESI) CharacterCorporationID(context.Context, int64) (int64, error) { return 77, nil }
func (corpESI) CorporationName(context.Context, int64) (string, error)       { return "Acme", nil }
func (corpESI) CorporationWallets(context.Context, string, int64) ([]esi.DivisionBalance, error) {
	return []esi.DivisionBalance{{Division: 1, Cents: 900}}, nil
}
func (corpESI) CorporationJournal(context.Context, string, int64, int) ([]esi.JournalEntry, error) {
	return nil, &esi.APIError{Status: http.StatusForbidden}
}

func corpHarness(t *testing.T) *harness {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	h.tokens.chars[0].Scopes = append(h.tokens.chars[0].Scopes, collector.ScopeCorporationWallet)
	h.esi = corpESI{}
	return h
}

func TestServeCycleBackfillsAndMergesStatus(t *testing.T) {
	h := corpHarness(t)
	base, cancel, done := startServe(t, h)
	c := signIn(t, base)
	waitFor(t, "merged cycle in /api/status", func() bool {
		return getJSON(t, c, base+"/api/status")["journal_points"] == float64(1)
	})
	st := getJSON(t, c, base+"/api/status")
	if st["snapshots"] != float64(2) {
		t.Errorf("snapshots = %v, want 2", st["snapshots"])
	}
	skipped, _ := st["skipped"].([]any)
	if len(skipped) != 1 || !strings.Contains(fmt.Sprint(skipped[0]), "Acme (division 1)") {
		t.Errorf("skipped = %v, want the forbidden corporation journal", skipped)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d; stderr = %q", code, h.err.String())
	}
}

func TestServeNoBackfill(t *testing.T) {
	h := corpHarness(t)
	base, cancel, done := startServe(t, h, "--no-backfill")
	c := signIn(t, base)
	waitFor(t, "snapshot in /api/status", func() bool {
		return getJSON(t, c, base+"/api/status")["snapshots"] == float64(2)
	})
	st := getJSON(t, c, base+"/api/status")
	if st["journal_points"] != float64(0) {
		t.Errorf("journal_points = %v, want 0 with --no-backfill", st["journal_points"])
	}
	if skipped, _ := st["skipped"].([]any); len(skipped) != 0 {
		t.Errorf("skipped = %v, want none without the backfill", skipped)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d", code)
	}
}
