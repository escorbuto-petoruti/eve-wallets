package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

type namesEnv struct {
	*harness
	path           string
	charID, corpID int64
}

// newNamesEnv returns a harness backed by a real temporary SQLite database that
// holds one character wallet (Alice) and one corporation wallet (Corp, division 2).
func newNamesEnv(t *testing.T) *namesEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "wallets.db")
	h := newHarness(t, map[string]string{"EVE_WALLETS_DB": path})
	h.deps.openStore = openSecureStore
	st, err := openSecureStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	e := &namesEnv{harness: h, path: path}
	if e.charID, err = st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 1, OwnerName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if e.corpID, err = st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 9, OwnerName: "Corp", Division: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSnapshot(ctx, e.corpID, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), 4200); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *namesEnv) run(args ...string) int {
	return run(context.Background(), args, e.deps)
}

func (e *namesEnv) wallet(t *testing.T, id int64) store.Wallet {
	t.Helper()
	st, err := store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ws, err := st.Wallets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("wallet %d not found", id)
	return store.Wallet{}
}

func TestWalletsTable(t *testing.T) {
	e := newNamesEnv(t)
	st, err := store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetESIName(context.Background(), e.corpID, "Ops"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	if code := e.run("wallets"); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, e.err.String())
	}
	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	for i, want := range [][]string{
		{"ID", "KIND", "OWNER", "DIVISION", "NAME", "SOURCE"},
		{"character", "Alice", "Alice", "default"},
		{"corporation", "Corp", "2", "Ops", "esi"},
	} {
		if got := strings.Fields(lines[i]); !containsInOrder(got, want) {
			t.Errorf("line %d = %q, want fields %q in order", i, lines[i], want)
		}
	}
	// Columns are aligned: every row has the NAME column at the same offset.
	if i := strings.Index(lines[0], "NAME"); !strings.HasPrefix(lines[1][i:], "Alice") || !strings.HasPrefix(lines[2][i:], "Ops") {
		t.Errorf("columns not aligned:\n%s", e.out.String())
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func containsInOrder(got, want []string) bool {
	j := 0
	for _, g := range got {
		if j < len(want) && g == want[j] {
			j++
		}
	}
	return j == len(want)
}

func TestWalletsEmptyDatabase(t *testing.T) {
	h := newHarness(t, map[string]string{"EVE_WALLETS_DB": filepath.Join(t.TempDir(), "w.db")})
	h.deps.openStore = openSecureStore
	if code := run(context.Background(), []string{"wallets"}, h.deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(h.out.String(), "eve-wallets collect") {
		t.Errorf("stdout = %q, want the collect hint", h.out.String())
	}
}

func TestWalletsNeedsNoAuthOrNetwork(t *testing.T) {
	e := newNamesEnv(t)
	e.deps.newTokens = func(*store.Store) auth.TokenSource { t.Error("wallets must not build a token source"); return nil }
	e.deps.newESI = nil
	if code := e.run("wallets"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
}

func TestWalletsDBFlag(t *testing.T) {
	e := newNamesEnv(t)
	e.deps.getenv = func(string) string { return "" }
	if code := e.run("wallets", "--db", e.path); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, e.err.String())
	}
	if !strings.Contains(e.out.String(), "Corp") {
		t.Errorf("stdout = %q", e.out.String())
	}
}

func TestWalletsUsageErrors(t *testing.T) {
	e := newNamesEnv(t)
	if code := e.run("wallets", "extra"); code != 2 {
		t.Errorf("extra arg exit = %d, want 2", code)
	}
	if !strings.Contains(e.err.String(), "Usage:") {
		t.Errorf("stderr = %q", e.err.String())
	}
	if code := e.run("wallets", "-h"); code != 0 {
		t.Errorf("-h exit = %d, want 0", code)
	}
	if code := e.run("wallets", "--bogus"); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
}

func TestLabelSetAndClear(t *testing.T) {
	e := newNamesEnv(t)
	id := itoa(e.corpID)

	if code := e.run("label", id, "Mining", "fund"); code != 0 {
		t.Fatalf("set exit = %d, stderr %q", code, e.err.String())
	}
	if got := e.wallet(t, e.corpID); got.Label != "Mining fund" {
		t.Errorf("label = %q, want %q", got.Label, "Mining fund")
	}
	out := e.out.String()
	for _, want := range []string{id, "Corp", "Mining fund"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirmation %q lacks %q", out, want)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("confirmation should be one line: %q", out)
	}

	// A quoted name is the same as separate words.
	e.out.b.Reset()
	if code := e.run("label", id, "  Ship   fund "); code != 0 {
		t.Fatalf("quoted exit = %d, stderr %q", code, e.err.String())
	}
	if got := e.wallet(t, e.corpID); got.Label != "Ship   fund" {
		t.Errorf("label = %q", got.Label)
	}

	e.out.b.Reset()
	if code := e.run("label", "--clear", id); code != 0 {
		t.Fatalf("clear exit = %d, stderr %q", code, e.err.String())
	}
	if got := e.wallet(t, e.corpID); got.Label != "" {
		t.Errorf("label after clear = %q", got.Label)
	}
	if out := e.out.String(); !strings.Contains(out, "Division 2") || !strings.Contains(out, "default") {
		t.Errorf("clear confirmation = %q, want the default name and source", out)
	}
}

func TestLabelClearWithoutLabelAndWithESIName(t *testing.T) {
	e := newNamesEnv(t)
	if code := e.run("label", "--clear", itoa(e.corpID)); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, e.err.String())
	}
	st, _ := store.Open(e.path)
	_ = st.SetESIName(context.Background(), e.corpID, "Ops")
	_ = st.SetLabel(context.Background(), e.corpID, "Mine")
	_ = st.Close()
	e.out.b.Reset()
	if code := e.run("label", "--clear", itoa(e.corpID)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out := e.out.String(); !strings.Contains(out, "Ops") || !strings.Contains(out, "esi") {
		t.Errorf("confirmation = %q, want the ESI name", out)
	}
}

func TestLabelErrors(t *testing.T) {
	long := strings.Repeat("x", 65)
	tests := []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"unknown wallet", []string{"label", "999", "Nope"}, 1, "no wallet with id 999"},
		{"clear unknown wallet", []string{"label", "--clear", "999"}, 1, "no wallet with id 999"},
		{"id not a number", []string{"label", "abc", "Nope"}, 1, `invalid wallet id "abc"`},
		{"id zero", []string{"label", "0", "Nope"}, 1, `invalid wallet id "0"`},
		{"id negative", []string{"label", "--", "-3", "Nope"}, 1, `invalid wallet id "-3"`},
		{"name too long", []string{"label", "1", long}, 1, "1-64 characters, no control characters"},
		{"control character", []string{"label", "1", "bad\x07name"}, 1, "1-64 characters, no control characters"},
		{"blank name", []string{"label", "1", "   "}, 1, "1-64 characters, no control characters"},
		{"no arguments", []string{"label"}, 2, "Usage:"},
		{"id without name", []string{"label", "1"}, 2, "Usage:"},
		{"clear without id", []string{"label", "--clear"}, 2, "Usage:"},
		{"clear with a name", []string{"label", "--clear", "1", "Name"}, 2, "Usage:"},
		{"bad flag", []string{"label", "--bogus", "1", "x"}, 2, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNamesEnv(t)
			if code := e.run(tt.args...); code != tt.code {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tt.code, e.err.String())
			}
			if !strings.Contains(e.err.String(), tt.err) {
				t.Errorf("stderr = %q, want %q", e.err.String(), tt.err)
			}
			if e.out.String() != "" {
				t.Errorf("stdout = %q, want empty", e.out.String())
			}
			if tt.code == 1 && strings.Count(strings.TrimRight(e.err.String(), "\n"), "\n") != 0 {
				t.Errorf("error should be one line: %q", e.err.String())
			}
			if got := e.wallet(t, e.charID); got.Label != "" {
				t.Errorf("a failed command changed a label: %q", got.Label)
			}
		})
	}
}

func TestLabelDBFlag(t *testing.T) {
	e := newNamesEnv(t)
	e.deps.getenv = func(string) string { return "" }
	if code := e.run("label", "--db", e.path, itoa(e.charID), "Main"); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, e.err.String())
	}
	if got := e.wallet(t, e.charID); got.Label != "Main" {
		t.Errorf("label = %q", got.Label)
	}
}

func TestLabelKeepsBalances(t *testing.T) {
	e := newNamesEnv(t)
	if code := e.run("label", itoa(e.corpID), "Renamed"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	st, err := store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	latest, err := st.LatestBalances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].Wallet.ID != e.corpID || latest[0].Cents != 4200 || latest[0].Wallet.DisplayName() != "Renamed" {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestUsageListsNameCommands(t *testing.T) {
	for _, want := range []string{"eve-wallets wallets", "eve-wallets label"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}
