package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0).UTC()

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func charWallet(t *testing.T, s *Store, ownerID int64, name string) int64 {
	t.Helper()
	id, err := s.UpsertWallet(context.Background(), Wallet{Kind: KindCharacter, OwnerID: ownerID, OwnerName: name})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigrationUpgradesV2DatabaseToV3(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations[0] + `;` + migrations[1] + `;
		INSERT INTO wallets (id, kind, owner_id, owner_name, division, label) VALUES
			(1, 'character', 10, 'Alice', 0, 'Main'),
			(2, 'corporation', 20, 'Acme', 3, NULL);
		INSERT INTO balances (wallet_id, taken_at, cents, source) VALUES
			(1, 100, 500, 'snapshot'),
			(2, 200, 900, 'snapshot');
		PRAGMA user_version = 2;`); err != nil {
		t.Fatalf("seed v2: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v2 database: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("user_version = %d, err = %v, want 3", version, err)
	}
	got, err := s.LatestBalances(ctx)
	if err != nil || len(got) != 2 || got[0].Cents != 500 || got[0].Wallet.Label != "Main" || got[1].Cents != 900 {
		t.Fatalf("old rows not intact: %+v, err = %v", got, err)
	}
	for _, table := range []string{"users", "tokens", "user_wallets", "sessions"} {
		if n := count(t, s, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s missing after migration", table)
		}
	}
}

func TestForeignKeysEnforcedOnEveryConnection(t *testing.T) {
	s := openTemp(t)
	// Hold several connections open at once so the pool must create more than one.
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		var on int
		if err := c.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
			t.Fatalf("foreign_keys = %d, err = %v, want 1", on, err)
		}
		_ = c.Close()
	}
	// A token for a user that does not exist must be refused.
	err := s.SaveToken(context.Background(), Token{CharacterID: 1, UserID: 1, CharacterName: "x", RefreshToken: "r", Scopes: []string{"a"}})
	if err == nil {
		t.Fatal("SaveToken accepted a token for a missing user")
	}
}

func TestUpsertUserKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 7, "Old", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertUser(ctx, 7, "New", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var name string
	var created int64
	if err := s.db.QueryRow(`SELECT name, created_at FROM users WHERE character_id = 7`).Scan(&name, &created); err != nil {
		t.Fatal(err)
	}
	if name != "New" || created != t0.Unix() {
		t.Fatalf("name = %q created_at = %d, want New and %d", name, created, t0.Unix())
	}
	if n := count(t, s, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func TestTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 1, "Alice", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetToken(ctx, 1); err != nil || ok {
		t.Fatalf("GetToken on empty = ok %v, err %v", ok, err)
	}
	tok := Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "first",
		Scopes: []string{"esi-wallet.read_character_wallet.v1", "esi-corporations.read_divisions.v1"}, UpdatedAt: t0}
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetToken(ctx, 1)
	if err != nil || !ok || !reflect.DeepEqual(got, tok) {
		t.Fatalf("GetToken = %+v, ok %v, err %v, want %+v", got, ok, err, tok)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT scopes FROM tokens WHERE character_id = 1`).Scan(&raw); err != nil ||
		raw != "esi-wallet.read_character_wallet.v1 esi-corporations.read_divisions.v1" {
		t.Fatalf("scopes stored as %q, err %v", raw, err)
	}

	tok.RefreshToken, tok.UpdatedAt = "second", t0.Add(time.Minute)
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetToken(ctx, 1)
	if got.RefreshToken != "second" || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("upsert did not replace the token: %+v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM tokens`); n != 1 {
		t.Fatalf("tokens = %d, want 1", n)
	}

	// Empty scopes round-trip as an empty slice, not [""].
	if err := s.UpsertUser(ctx, 2, "Bob", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, Token{CharacterID: 2, UserID: 2, CharacterName: "Bob", RefreshToken: "b", UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Tokens(ctx)
	if err != nil || len(all) != 2 || all[0].CharacterID != 1 || all[1].CharacterID != 2 || len(all[1].Scopes) != 0 {
		t.Fatalf("Tokens = %+v, err %v", all, err)
	}

	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetToken(ctx, 1); ok {
		t.Fatal("token still present after DeleteToken")
	}
	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatalf("DeleteToken of a missing token should be a no-op: %v", err)
	}
}

func TestTokenErrorsNeverContainTheRefreshToken(t *testing.T) {
	s := openTemp(t)
	err := s.SaveToken(context.Background(), Token{CharacterID: 1, UserID: 99, CharacterName: "x", RefreshToken: "SECRET-VALUE"})
	if err == nil || strings.Contains(err.Error(), "SECRET-VALUE") {
		t.Fatalf("err = %v, want an error without the secret", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 1, "Alice", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "live", 1, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "old", 1, t0.Add(-2*time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.SessionUser(ctx, "live", t0)
	if err != nil || !ok || u.CharacterID != 1 || u.Name != "Alice" {
		t.Fatalf("SessionUser(live) = %+v, ok %v, err %v", u, ok, err)
	}
	if _, ok, err := s.SessionUser(ctx, "old", t0); err != nil || ok {
		t.Fatalf("expired session returned: ok %v, err %v", ok, err)
	}
	if _, ok, _ := s.SessionUser(ctx, "live", t0.Add(time.Hour)); ok {
		t.Fatal("session valid at exactly expires_at")
	}
	if _, ok, _ := s.SessionUser(ctx, "missing", t0); ok {
		t.Fatal("unknown session returned")
	}

	n, err := s.PurgeExpiredSessions(ctx, t0)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions = %d, err %v, want 1", n, err)
	}
	if c := count(t, s, `SELECT count(*) FROM sessions`); c != 1 {
		t.Fatalf("sessions = %d, want 1", c)
	}
	if err := s.DeleteSession(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SessionUser(ctx, "live", t0); ok {
		t.Fatal("session survives DeleteSession")
	}
}

// The session user must carry the identifier the wallet links are keyed by
// (sessions.user_id, which user_wallets.user_id references too), so callers
// never have to infer a user id from a character id: today the two are equal
// only because users.character_id is the primary key the FKs point at.
func TestSessionUserCarriesUserID(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 42, "Dana", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "dana", 42, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := charWallet(t, s, 42, "Dana")
	if err := s.LinkWallet(ctx, 42, w); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.SessionUser(ctx, "dana", t0)
	if err != nil || !ok || u.CharacterID != 42 || u.Name != "Dana" {
		t.Fatalf("SessionUser(dana) = %+v, ok %v, err %v", u, ok, err)
	}
	if u.UserID != 42 {
		t.Fatalf("SessionUser(dana).UserID = %d, want 42 (sessions.user_id)", u.UserID)
	}
	ws, err := s.WalletsForUser(ctx, u.UserID)
	if err != nil || len(ws) != 1 || ws[0].ID != w {
		t.Fatalf("WalletsForUser(SessionUser.UserID) = %v, err %v, want the linked wallet", ws, err)
	}
}

func TestLinkWalletIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_ = s.UpsertUser(ctx, 1, "Alice", t0)
	w := charWallet(t, s, 1, "Alice")
	for i := 0; i < 2; i++ {
		if err := s.LinkWallet(ctx, 1, w); err != nil {
			t.Fatalf("LinkWallet #%d: %v", i+1, err)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM user_wallets`); n != 1 {
		t.Fatalf("links = %d, want 1", n)
	}
	if err := s.LinkWallet(ctx, 1, 9999); err == nil {
		t.Fatal("LinkWallet accepted a missing wallet")
	}
}

func TestCascadeDeletes(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_ = s.UpsertUser(ctx, 1, "Alice", t0)
	w := charWallet(t, s, 1, "Alice")
	_ = s.LinkWallet(ctx, 1, w)
	_ = s.SaveToken(ctx, Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "r", UpdatedAt: t0})
	_ = s.CreateSession(ctx, "s", 1, t0, t0.Add(time.Hour))

	// Wallet deleted -> links gone, user stays.
	if _, err := s.db.Exec(`DELETE FROM wallets WHERE id = ?`, w); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM user_wallets`); n != 0 {
		t.Fatalf("links after wallet delete = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users after wallet delete = %d", n)
	}

	w = charWallet(t, s, 1, "Alice")
	_ = s.LinkWallet(ctx, 1, w)
	// User deleted -> tokens, links, sessions gone; wallet stays.
	if _, err := s.db.Exec(`DELETE FROM users WHERE character_id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tokens", "user_wallets", "sessions"} {
		if n := count(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s rows after user delete = %d", table, n)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM wallets`); n != 1 {
		t.Fatalf("wallets after user delete = %d, want 1", n)
	}
}

func TestUserScopedReads(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	for _, u := range []struct {
		id   int64
		name string
	}{{1, "Alice"}, {2, "Bob"}, {3, "Carol"}} {
		if err := s.UpsertUser(ctx, u.id, u.name, t0); err != nil {
			t.Fatal(err)
		}
	}
	a := charWallet(t, s, 1, "Alice")
	b := charWallet(t, s, 2, "Bob")
	corp, err := s.UpsertWallet(ctx, Wallet{Kind: KindCorporation, OwnerID: 50, OwnerName: "Acme", Division: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]int64{{1, a}, {2, b}, {1, corp}, {2, corp}} {
		if err := s.LinkWallet(ctx, l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []int64{a, b, corp} {
		if err := s.AddSnapshot(ctx, w, t0, w*100); err != nil {
			t.Fatal(err)
		}
		if err := s.AddSnapshot(ctx, w, t0.Add(time.Hour), w*100+1); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(ws []Wallet) []int64 {
		var out []int64
		for _, w := range ws {
			out = append(out, w.ID)
		}
		return out
	}
	wa, err := s.WalletsForUser(ctx, 1)
	if err != nil || !reflect.DeepEqual(ids(wa), []int64{a, corp}) {
		t.Fatalf("WalletsForUser(Alice) = %v, err %v, want [%d %d]", ids(wa), err, a, corp)
	}
	wb, _ := s.WalletsForUser(ctx, 2)
	if !reflect.DeepEqual(ids(wb), []int64{b, corp}) {
		t.Fatalf("WalletsForUser(Bob) = %v", ids(wb))
	}
	if wc, err := s.WalletsForUser(ctx, 3); err != nil || len(wc) != 0 {
		t.Fatalf("WalletsForUser(unlinked) = %v, err %v, want empty", wc, err)
	}

	ba, err := s.LatestBalancesForUser(ctx, 1)
	if err != nil || len(ba) != 2 || ba[0].Wallet.ID != a || ba[0].Cents != a*100+1 || ba[1].Wallet.ID != corp {
		t.Fatalf("LatestBalancesForUser(Alice) = %+v, err %v", ba, err)
	}
	if bc, err := s.LatestBalancesForUser(ctx, 3); err != nil || len(bc) != 0 {
		t.Fatalf("LatestBalancesForUser(unlinked) = %+v, err %v", bc, err)
	}

	sa, err := s.SeriesForUser(ctx, 1, SeriesFilter{})
	if err != nil || len(sa) != 4 {
		t.Fatalf("SeriesForUser(Alice) = %+v, err %v, want 4 points", sa, err)
	}
	for _, p := range sa {
		if p.WalletID == b {
			t.Fatalf("Alice sees Bob's wallet: %+v", p)
		}
	}
	// A filter naming a wallet the user cannot see must not leak it.
	sf, err := s.SeriesForUser(ctx, 1, SeriesFilter{WalletIDs: []int64{b}})
	if err != nil || len(sf) != 0 {
		t.Fatalf("SeriesForUser(Alice, wallet of Bob) = %+v, err %v, want empty", sf, err)
	}
	sf, _ = s.SeriesForUser(ctx, 1, SeriesFilter{WalletIDs: []int64{a, b}, From: t0.Add(time.Minute)})
	if len(sf) != 1 || sf[0].WalletID != a || sf[0].Cents != a*100+1 {
		t.Fatalf("filtered series = %+v", sf)
	}
	if sc, err := s.SeriesForUser(ctx, 3, SeriesFilter{}); err != nil || len(sc) != 0 {
		t.Fatalf("SeriesForUser(unlinked) = %+v, err %v, want empty", sc, err)
	}
}
