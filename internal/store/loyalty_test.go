package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// seedLoyaltyUser registers a user whose own character holds a token.
func seedLoyaltyUser(t *testing.T, s *Store, id int64, name string) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertUser(ctx, id, name, ts(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, Token{CharacterID: id, UserID: id, CharacterName: name, RefreshToken: "r", Scopes: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationLoyaltyUpgradesPreviousVersionWithData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prev.db")
	prev := len(migrations) - 1

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:prev] {
		if _, err := raw.Exec(m); err != nil {
			t.Fatalf("apply previous schema: %v", err)
		}
	}
	if _, err := raw.Exec(`
		INSERT INTO users (character_id, name, created_at) VALUES (10, 'Alice', 1);
		INSERT INTO tokens (character_id, user_id, character_name, refresh_token, scopes, updated_at) VALUES (10, 10, 'Alice', 'r', 'a', 1);
		INSERT INTO wallets (id, kind, owner_id, owner_name, division) VALUES (1, 'character', 10, 'Alice', 0);
		INSERT INTO balances (wallet_id, taken_at, cents, source) VALUES (1, 100, 500, 'snapshot');
		PRAGMA user_version = ` + strconv.Itoa(prev) + `;`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open previous database: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d, err = %v, want %d", version, err, len(migrations))
	}
	if got, err := s.LatestBalances(ctx); err != nil || len(got) != 1 || got[0].Cents != 500 {
		t.Fatalf("data lost in upgrade: %+v, %v", got, err)
	}
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{CorporationID: 1, Points: 5}}, ts(200)); err != nil {
		t.Fatalf("ReplaceLoyalty after upgrade: %v", err)
	}
}

func TestReplaceLoyaltyReplacesTheSnapshotOfOneCharacter(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	seedLoyaltyUser(t, s, 20, "Bob")

	first := []LoyaltyPoints{{CorporationID: 1, Points: 100}, {CorporationID: 2, Points: 9007199254740993}, {CorporationID: 3, Points: 7}}
	if err := s.ReplaceLoyalty(ctx, 10, first, ts(1000)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLoyalty(ctx, 20, []LoyaltyPoints{{CorporationID: 1, Points: 55}}, ts(1000)); err != nil {
		t.Fatal(err)
	}
	// Corporation 2 and 3 are no longer returned by ESI; 1 changed.
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{CorporationID: 1, Points: 250}}, ts(2000)); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoyaltyForUser(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []CharacterLoyalty{{CharacterID: 10, CorporationID: 1, Points: 250, FetchedAt: ts(2000)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user 10 = %+v, want %+v", got, want)
	}
	got, err = s.LoyaltyForUser(ctx, 20)
	if err != nil || len(got) != 1 || got[0].Points != 55 {
		t.Fatalf("user 20 = %+v, %v (another character's replace must not touch it)", got, err)
	}

	// An empty snapshot clears the character.
	if err := s.ReplaceLoyalty(ctx, 10, nil, ts(3000)); err != nil {
		t.Fatal(err)
	}
	if got, err = s.LoyaltyForUser(ctx, 10); err != nil || len(got) != 0 {
		t.Fatalf("after empty replace = %+v, %v", got, err)
	}
}

func TestReplaceLoyaltyIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{CorporationID: 1, Points: 100}}, ts(1000)); err != nil {
		t.Fatal(err)
	}
	// A character without a token violates the foreign key: nothing changes.
	if err := s.ReplaceLoyalty(ctx, 99, []LoyaltyPoints{{CorporationID: 1, Points: 1}}, ts(2000)); err == nil {
		t.Fatal("expected an error for a character without a token")
	}
	// A duplicated corporation fails the whole replace, keeping the old rows.
	dup := []LoyaltyPoints{{CorporationID: 5, Points: 1}, {CorporationID: 5, Points: 2}}
	if err := s.ReplaceLoyalty(ctx, 10, dup, ts(3000)); err == nil {
		t.Fatal("expected an error for a duplicated corporation")
	}
	got, err := s.LoyaltyForUser(ctx, 10)
	if err != nil || len(got) != 1 || got[0].CorporationID != 1 || got[0].Points != 100 {
		t.Fatalf("snapshot after failed replace = %+v, %v", got, err)
	}
}

func TestLoyaltyForUserIsScopedAndOrdered(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	seedLoyaltyUser(t, s, 20, "Bob")
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{CorporationID: 1, Points: 5}, {CorporationID: 2, Points: 50}}, ts(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLoyalty(ctx, 20, []LoyaltyPoints{{CorporationID: 9, Points: 999}}, ts(10)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoyaltyForUser(ctx, 10)
	if err != nil || len(got) != 2 || got[0].CorporationID != 2 || got[1].CorporationID != 1 {
		t.Fatalf("got %+v, %v (want points descending, own characters only)", got, err)
	}
	if got, err = s.LoyaltyForUser(ctx, 12345); err != nil || len(got) != 0 {
		t.Fatalf("unknown user = %+v, %v", got, err)
	}
}

func TestLoyaltyFollowsTheTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	seedLoyaltyUser(t, s, 20, "Bob")
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{CorporationID: 1, Points: 5}}, ts(10)); err != nil {
		t.Fatal(err)
	}
	// Moving the character keeps its snapshot with the new owner.
	if err := s.MoveToken(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoyaltyForUser(ctx, 20); err != nil || len(got) != 1 || got[0].CharacterID != 10 {
		t.Fatalf("after move = %+v, %v", got, err)
	}
	// Removing the token removes the snapshot.
	if err := s.DeleteToken(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoyaltyForUser(ctx, 20); err != nil || len(got) != 0 {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
}

func TestCorporationNamesCache(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if got, err := s.CorporationNames(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty ids = %v, %v", got, err)
	}
	if err := s.UpsertCorporationNames(ctx, map[int64]string{1: "One", 2: "Two"}, ts(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCorporationNames(ctx, map[int64]string{2: "Two Renamed", 3: "Three"}, ts(20)); err != nil {
		t.Fatal(err)
	}
	got, err := s.CorporationNames(ctx, []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{1: "One", 2: "Two Renamed", 3: "Three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (unknown ids are absent)", got, want)
	}
	if err := s.UpsertCorporationNames(ctx, nil, ts(30)); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
}
