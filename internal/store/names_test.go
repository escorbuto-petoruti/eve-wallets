package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func columnNames(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestMigrationUpgradesV1DatabaseInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	// migrations[0] is the frozen v1 schema.
	if _, err := raw.Exec(migrations[0]); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	if _, err := raw.Exec(`
		INSERT INTO wallets (id, kind, owner_id, owner_name, division) VALUES
			(1, 'character', 10, 'Alice', 0),
			(2, 'corporation', 20, 'Acme', 3);
		INSERT INTO balances (wallet_id, taken_at, cents, source) VALUES
			(1, 100, 500, 'snapshot'),
			(2, 200, 900, 'snapshot');
		PRAGMA user_version = 1;`); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 database: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d, err = %v, want %d", version, err, len(migrations))
	}
	got, err := s.LatestBalances(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("LatestBalances = %+v, err = %v", got, err)
	}
	if got[0].Wallet.OwnerName != "Alice" || got[0].Cents != 500 ||
		got[1].Wallet.OwnerName != "Acme" || got[1].Wallet.Division != 3 || got[1].Cents != 900 {
		t.Fatalf("data lost in upgrade: %+v", got)
	}
	for _, wb := range got {
		if wb.Wallet.Label != "" || wb.Wallet.ESIName != "" {
			t.Fatalf("names should start empty: %+v", wb.Wallet)
		}
	}
}

func TestFreshDatabaseMatchesUpgradedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations[0] + `; PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	fresh := openTemp(t)

	for _, table := range []string{"wallets", "balances"} {
		if g, w := columnNames(t, upgraded, table), columnNames(t, fresh, table); !reflect.DeepEqual(g, w) {
			t.Errorf("%s columns: upgraded %v, fresh %v", table, g, w)
		}
	}
	want := []string{"balances_wallet_time"}
	var idx []string
	rows, err := fresh.db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, want[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		idx = append(idx, n)
	}
	if !reflect.DeepEqual(idx, want) {
		t.Errorf("indexes = %v, want %v", idx, want)
	}
}

func TestReopenAfterMigration2IsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wallets.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "Alice"})
	if err := s.SetLabel(ctx, id, "Main"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	ws, err := s2.Wallets(ctx)
	if err != nil || len(ws) != 1 || ws[0].Label != "Main" {
		t.Fatalf("after reopen = %+v, err = %v", ws, err)
	}
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("Open accepted a database newer than the code")
	}
}

func walletByID(t *testing.T, s *Store, id int64) Wallet {
	t.Helper()
	ws, err := s.Wallets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("wallet %d not found", id)
	return Wallet{}
}

func TestSetAndClearLabel(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "Acme", Division: 2})

	if err := s.SetLabel(ctx, id, "  Ops  "); err != nil {
		t.Fatal(err)
	}
	if got := walletByID(t, s, id).Label; got != "Ops" {
		t.Fatalf("Label = %q, want trimmed %q", got, "Ops")
	}
	if err := s.ClearLabel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := walletByID(t, s, id).Label; got != "" {
		t.Fatalf("Label after clear = %q", got)
	}
}

func TestSetAndClearESIName(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "Acme", Division: 2})

	if err := s.SetESIName(ctx, id, "Manufacturing"); err != nil {
		t.Fatal(err)
	}
	w := walletByID(t, s, id)
	if w.ESIName != "Manufacturing" || w.Label != "" {
		t.Fatalf("wallet = %+v", w)
	}
	if err := s.ClearESIName(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := walletByID(t, s, id).ESIName; got != "" {
		t.Fatalf("ESIName after clear = %q", got)
	}
}

func TestNameValidation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "Alice"})

	tests := []struct {
		name  string
		input string
		want  string // stored value; empty when rejected
		ok    bool
	}{
		{"simple", "Savings", "Savings", true},
		{"max length", strings.Repeat("a", 64), strings.Repeat("a", 64), true},
		{"multi-byte runes count as runes", strings.Repeat("é", 64), strings.Repeat("é", 64), true},
		{"too long", strings.Repeat("a", 65), "", false},
		{"too long in runes", strings.Repeat("é", 65), "", false},
		{"empty", "", "", false},
		{"only spaces", "   ", "", false},
		{"newline", "a\nb", "", false},
		{"tab", "a\tb", "", false},
		{"control character", "a\x07b", "", false},
		{"delete character", "a\x7fb", "", false},
	}
	setters := map[string]func(context.Context, int64, string) error{
		"SetLabel":   s.SetLabel,
		"SetESIName": s.SetESIName,
	}
	for setter, set := range setters {
		for _, tt := range tests {
			t.Run(setter+"/"+tt.name, func(t *testing.T) {
				if err := s.ClearLabel(ctx, id); err != nil {
					t.Fatal(err)
				}
				if err := s.ClearESIName(ctx, id); err != nil {
					t.Fatal(err)
				}
				err := set(ctx, id, tt.input)
				if tt.ok {
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
				} else if !errors.Is(err, ErrInvalidName) {
					t.Fatalf("err = %v, want ErrInvalidName", err)
				}
				w := walletByID(t, s, id)
				got := w.Label
				if setter == "SetESIName" {
					got = w.ESIName
				}
				if got != tt.want {
					t.Fatalf("stored = %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestNameWritesUnknownWallet(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	calls := map[string]error{
		"SetLabel":     s.SetLabel(ctx, 999, "x"),
		"SetESIName":   s.SetESIName(ctx, 999, "x"),
		"ClearLabel":   s.ClearLabel(ctx, 999),
		"ClearESIName": s.ClearESIName(ctx, 999),
	}
	for name, err := range calls {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestDisplayNamePrecedenceAndSource(t *testing.T) {
	tests := []struct {
		name       string
		w          Wallet
		wantName   string
		wantSource NameSource
	}{
		{"label wins", Wallet{Kind: KindCorporation, OwnerName: "Acme", Division: 3, Label: "Ops", ESIName: "Mfg"}, "Ops", NameCustom},
		{"esi when no label", Wallet{Kind: KindCorporation, OwnerName: "Acme", Division: 3, ESIName: "Mfg"}, "Mfg", NameESI},
		{"corporation default", Wallet{Kind: KindCorporation, OwnerName: "Acme", Division: 3}, "Division 3", NameDefault},
		{"character default", Wallet{Kind: KindCharacter, OwnerName: "Alice"}, "Alice", NameDefault},
		{"character label", Wallet{Kind: KindCharacter, OwnerName: "Alice", Label: "Wallet"}, "Wallet", NameCustom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.w.DisplayName(); got != tt.wantName {
				t.Errorf("DisplayName = %q, want %q", got, tt.wantName)
			}
			if got := tt.w.NameSource(); got != tt.wantSource {
				t.Errorf("NameSource = %q, want %q", got, tt.wantSource)
			}
		})
	}
	if NameCustom != "custom" || NameESI != "esi" || NameDefault != "default" {
		t.Errorf("source values = %q %q %q", NameCustom, NameESI, NameDefault)
	}
}

func TestQueriesPopulateNames(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "Acme", Division: 2})
	if err := s.SetLabel(ctx, id, "Ops"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetESIName(ctx, id, "Mfg"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSnapshot(ctx, id, ts(100), 10); err != nil {
		t.Fatal(err)
	}

	ws, err := s.Wallets(ctx)
	if err != nil || len(ws) != 1 || ws[0].Label != "Ops" || ws[0].ESIName != "Mfg" {
		t.Fatalf("Wallets = %+v, err = %v", ws, err)
	}
	lb, err := s.LatestBalances(ctx)
	if err != nil || len(lb) != 1 || lb[0].Wallet.Label != "Ops" || lb[0].Wallet.ESIName != "Mfg" ||
		lb[0].Wallet.DisplayName() != "Ops" {
		t.Fatalf("LatestBalances = %+v, err = %v", lb, err)
	}
}

func TestUpsertWalletKeepsNames(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	w := Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "Acme", Division: 2}
	id := mustWallet(t, s, w)
	if err := s.SetLabel(ctx, id, "Ops"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetESIName(ctx, id, "Mfg"); err != nil {
		t.Fatal(err)
	}
	w.OwnerName = "Acme Renamed"
	if again := mustWallet(t, s, w); again != id {
		t.Fatalf("id changed: %d != %d", again, id)
	}
	got := walletByID(t, s, id)
	if got.Label != "Ops" || got.ESIName != "Mfg" || got.OwnerName != "Acme Renamed" {
		t.Fatalf("wallet = %+v", got)
	}
}
