package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "wallets.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustWallet(t *testing.T, s *Store, w Wallet) int64 {
	t.Helper()
	id, err := s.UpsertWallet(context.Background(), w)
	if err != nil {
		t.Fatalf("UpsertWallet(%+v): %v", w, err)
	}
	return id
}

func ts(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func TestOpenMigrationsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "Alice"})
	if err := s.AddSnapshot(ctx, id, ts(100), 500); err != nil {
		t.Fatalf("AddSnapshot: %v", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	ws, err := s2.Wallets(ctx)
	if err != nil || len(ws) != 1 || ws[0].ID != id {
		t.Fatalf("after reopen wallets = %+v, err = %v", ws, err)
	}
}

func TestOpenInMemory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer s.Close()
	mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	ws, err := s.Wallets(context.Background())
	if err != nil || len(ws) != 1 {
		t.Fatalf("wallets = %+v, err = %v", ws, err)
	}
}

func TestUpsertWalletUpdatesNameKeepsID(t *testing.T) {
	s := openTemp(t)
	w := Wallet{Kind: KindCorporation, OwnerID: 98000001, OwnerName: "Old Corp", Division: 3}
	id1 := mustWallet(t, s, w)
	w.OwnerName = "New Corp"
	id2 := mustWallet(t, s, w)
	if id1 != id2 {
		t.Fatalf("id changed: %d -> %d", id1, id2)
	}
	ws, err := s.Wallets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].OwnerName != "New Corp" || ws[0].ID != id1 || ws[0].Division != 3 {
		t.Fatalf("wallets = %+v", ws)
	}
}

func TestWalletsOrdering(t *testing.T) {
	s := openTemp(t)
	mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "c", Division: 2})
	mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 5, OwnerName: "c", Division: 1})
	mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 9, OwnerName: "z"})
	mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 3, OwnerName: "a"})
	ws, err := s.Wallets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		k Kind
		o int64
		d int
	}
	want := []key{{KindCharacter, 3, 0}, {KindCharacter, 9, 0}, {KindCorporation, 5, 1}, {KindCorporation, 5, 2}}
	if len(ws) != len(want) {
		t.Fatalf("got %d wallets", len(ws))
	}
	for i, w := range ws {
		if (key{w.Kind, w.OwnerID, w.Division}) != want[i] {
			t.Errorf("wallet %d = %+v, want %+v", i, w, want[i])
		}
	}
}

func TestCheckConstraintsRejectBadWallets(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tests := []struct {
		name string
		w    Wallet
	}{
		{"bad kind", Wallet{Kind: "alliance", OwnerID: 1, OwnerName: "x"}},
		{"character with division", Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "x", Division: 1}},
		{"corporation division 0", Wallet{Kind: KindCorporation, OwnerID: 1, OwnerName: "x", Division: 0}},
		{"corporation division 8", Wallet{Kind: KindCorporation, OwnerID: 1, OwnerName: "x", Division: 8}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.UpsertWallet(ctx, tt.w); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBalanceRequiresExistingWallet(t *testing.T) {
	s := openTemp(t)
	if err := s.AddSnapshot(context.Background(), 999, ts(1), 1); err == nil {
		t.Fatal("expected foreign key error")
	}
}

func TestSnapshotIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	for i := 0; i < 3; i++ {
		if err := s.AddSnapshot(ctx, id, ts(100), 1000); err != nil {
			t.Fatalf("AddSnapshot #%d: %v", i, err)
		}
	}
	pts, err := s.Series(ctx, SeriesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("points = %+v, want 1", pts)
	}
}

func TestJournalBalanceIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	for i := 0; i < 2; i++ {
		if err := s.AddJournalBalance(ctx, id, 777, ts(50), 2000); err != nil {
			t.Fatalf("AddJournalBalance #%d: %v", i, err)
		}
	}
	// Same entry id with a different time must still not duplicate.
	if err := s.AddJournalBalance(ctx, id, 777, ts(60), 2000); err != nil {
		t.Fatal(err)
	}
	if err := s.AddJournalBalance(ctx, id, 778, ts(60), 2500); err != nil {
		t.Fatal(err)
	}
	pts, err := s.Series(ctx, SeriesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %+v, want 2", pts)
	}
}

func TestSeriesOrderingAndFilters(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	b := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	// Insert out of order across wallets.
	for _, in := range []struct {
		id    int64
		at    int64
		cents int64
	}{{b, 300, 3}, {a, 200, 2}, {b, 100, 1}, {a, 100, 10}, {a, 300, 30}} {
		if err := s.AddSnapshot(ctx, in.id, ts(in.at), in.cents); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.Series(ctx, SeriesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	wantAll := []Point{
		{a, ts(100), 10}, {a, ts(200), 2}, {a, ts(300), 30},
		{b, ts(100), 1}, {b, ts(300), 3},
	}
	assertPoints(t, all, wantAll)

	onlyB, err := s.Series(ctx, SeriesFilter{WalletIDs: []int64{b}})
	if err != nil {
		t.Fatal(err)
	}
	assertPoints(t, onlyB, []Point{{b, ts(100), 1}, {b, ts(300), 3}})

	ranged, err := s.Series(ctx, SeriesFilter{From: ts(200), To: ts(300)})
	if err != nil {
		t.Fatal(err)
	}
	assertPoints(t, ranged, []Point{{a, ts(200), 2}, {a, ts(300), 30}, {b, ts(300), 3}})

	fromOnly, err := s.Series(ctx, SeriesFilter{WalletIDs: []int64{a}, From: ts(250)})
	if err != nil {
		t.Fatal(err)
	}
	assertPoints(t, fromOnly, []Point{{a, ts(300), 30}})

	toOnly, err := s.Series(ctx, SeriesFilter{WalletIDs: []int64{a}, To: ts(150)})
	if err != nil {
		t.Fatal(err)
	}
	assertPoints(t, toOnly, []Point{{a, ts(100), 10}})
}

func assertPoints(t *testing.T, got, want []Point) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d points %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].WalletID != want[i].WalletID || !got[i].At.Equal(want[i].At) || got[i].Cents != want[i].Cents {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLatestBalancesAcrossSources(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	b := mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 2, OwnerName: "B", Division: 1})
	mustWallet(t, s, Wallet{Kind: KindCorporation, OwnerID: 2, OwnerName: "B", Division: 2}) // no balances

	if err := s.AddSnapshot(ctx, a, ts(100), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddJournalBalance(ctx, a, 1, ts(200), 2); err != nil { // newest, journal
		t.Fatal(err)
	}
	if err := s.AddJournalBalance(ctx, b, 2, ts(100), 7); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSnapshot(ctx, b, ts(300), 9); err != nil { // newest, snapshot
		t.Fatal(err)
	}

	got, err := s.LatestBalances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (wallets without balances are omitted): %+v", len(got), got)
	}
	if got[0].Wallet.ID != a || got[0].Cents != 2 || !got[0].At.Equal(ts(200)) {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Wallet.ID != b || got[1].Cents != 9 || !got[1].At.Equal(ts(300)) {
		t.Errorf("second = %+v", got[1])
	}
}

func TestISKToCents(t *testing.T) {
	tests := []struct {
		name string
		isk  float64
		want int64
	}{
		{"zero", 0, 0},
		{"float noise 0.1+0.2", 0.1 + 0.2, 30},
		{"round half away from zero", 0.005, 1},
		{"typical", 1234567.89, 123456789},
		{"negative", -42.42, -4242},
		{"negative noise", -(0.1 + 0.2), -30},
		{"large", 9_000_000_000_000.99, 900_000_000_000_099},
		{"sub cent rounds down", 0.004, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ISKToCents(tt.isk); got != tt.want {
				t.Fatalf("ISKToCents(%v) = %d, want %d", tt.isk, got, tt.want)
			}
		})
	}
}

func TestCentsToISK(t *testing.T) {
	if got := CentsToISK(123456789); got != 1234567.89 {
		t.Fatalf("CentsToISK = %v", got)
	}
}
