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

func TestScopesForUser(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	seedLoyaltyUser(t, s, 20, "Bob")
	if err := s.SaveToken(ctx, Token{CharacterID: 11, UserID: 10, CharacterName: "Alt", RefreshToken: "r", Scopes: []string{"x", "y"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScopesForUser(ctx, 10)
	want := map[int64][]string{10: {"a"}, 11: {"x", "y"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ScopesForUser(10) = %v, %v, want %v (own characters only)", got, err, want)
	}
	if got, err = s.ScopesForUser(ctx, 99); err != nil || len(got) != 0 {
		t.Fatalf("unknown user = %v, %v", got, err)
	}
}

// historyRows lists the stored history of a character as "corp@taken_at=points".
func historyRows(t *testing.T, s *Store, char int64) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT corporation_id, taken_at, points FROM loyalty_history
		WHERE character_id = ? ORDER BY taken_at, corporation_id`, char)
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var corp, at, pts int64
		if err := rows.Scan(&corp, &at, &pts); err != nil {
			t.Fatal(err)
		}
		out = append(out, strconv.FormatInt(corp, 10)+"@"+strconv.FormatInt(at, 10)+"="+strconv.FormatInt(pts, 10))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplaceLoyaltyRecordsHistoryOnlyOnChange(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	replace := func(at int64, rows ...LoyaltyPoints) {
		t.Helper()
		if err := s.ReplaceLoyalty(ctx, 10, rows, ts(at)); err != nil {
			t.Fatal(err)
		}
	}
	replace(100, LoyaltyPoints{1, 50}, LoyaltyPoints{2, 7})
	replace(200, LoyaltyPoints{1, 50}, LoyaltyPoints{2, 7}) // unchanged: nothing new
	if got, want := historyRows(t, s, 10), []string{"1@100=50", "2@100=7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unchanged = %v, want %v", got, want)
	}
	replace(300, LoyaltyPoints{1, 80}, LoyaltyPoints{2, 7}) // one change: one row
	if got, want := historyRows(t, s, 10), []string{"1@100=50", "2@100=7", "1@300=80"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %v, want %v", got, want)
	}
}

func TestReplaceLoyaltyRecordsASingleZeroForAVanishedCorporation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	replace := func(at int64, rows ...LoyaltyPoints) {
		t.Helper()
		if err := s.ReplaceLoyalty(ctx, 10, rows, ts(at)); err != nil {
			t.Fatal(err)
		}
	}
	replace(100, LoyaltyPoints{1, 50}, LoyaltyPoints{2, 7})
	replace(200, LoyaltyPoints{2, 7})  // corporation 1 vanished: one zero
	replace(300, LoyaltyPoints{2, 7})  // still gone: no repeated zero
	replace(400)                       // corporation 2 vanished too
	replace(500, LoyaltyPoints{1, 50}) // corporation 1 reappears
	want := []string{"1@100=50", "2@100=7", "1@200=0", "2@400=0", "1@500=50"}
	if got := historyRows(t, s, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
}

func TestReplaceLoyaltyFailureLeavesHistoryUntouched(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{1, 50}}, ts(100)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{1, 60}, {5, 1}, {5, 2}}, ts(200)); err == nil {
		t.Fatal("expected an error for a duplicated corporation")
	}
	if got, want := historyRows(t, s, 10), []string{"1@100=50"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history after failed replace = %v, want %v", got, want)
	}
}

func TestMigrationLoyaltyHistorySeedsExistingSnapshot(t *testing.T) {
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
		INSERT INTO loyalty_points (character_id, corporation_id, points, fetched_at) VALUES (10, 1, 50, 111), (10, 2, 0, 111);
		PRAGMA user_version = ` + strconv.Itoa(prev) + `;`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open previous database: %v", err)
	}
	defer s.Close()
	if got, want := historyRows(t, s, 10), []string{"1@111=50", "2@111=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded history = %v, want %v", got, want)
	}
	// The first collection after the upgrade records only real changes.
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{1, 50}, {2, 9}}, ts(222)); err != nil {
		t.Fatal(err)
	}
	if got, want := historyRows(t, s, 10), []string{"1@111=50", "2@111=0", "2@222=9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history after first collection = %v, want %v", got, want)
	}
	// Reopening does not seed again.
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := historyRows(t, s2, 10); len(got) != 3 {
		t.Fatalf("reopen duplicated the seed: %v", got)
	}
}

func TestLoyaltyHistoryFollowsTheTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedLoyaltyUser(t, s, 10, "Alice")
	seedLoyaltyUser(t, s, 20, "Bob")
	if err := s.ReplaceLoyalty(ctx, 10, []LoyaltyPoints{{1, 5}}, ts(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveToken(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	if got, want := historyRows(t, s, 10), []string{"1@10=5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after move = %v, want %v", got, want)
	}
	if err := s.DeleteToken(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if got := historyRows(t, s, 10); len(got) != 0 {
		t.Fatalf("after delete = %v, want none", got)
	}
}
