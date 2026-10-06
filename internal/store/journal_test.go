package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func jentry(id, at int64, cents int64, ref string) JournalEntry {
	return JournalEntry{ID: id, Date: ts(at), AmountCents: cents, RefType: ref, Description: "d" + ref}
}

func TestMigrationJournalUpgradesPreviousVersionWithData(t *testing.T) {
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
	got, err := s.LatestBalances(ctx)
	if err != nil || len(got) != 1 || got[0].Cents != 500 {
		t.Fatalf("data lost in upgrade: %+v, %v", got, err)
	}
	n, err := s.AddJournalEntries(ctx, 1, []JournalEntry{jentry(1, 10, 5, "x")})
	if err != nil || n != 1 {
		t.Fatalf("AddJournalEntries after upgrade = %d, %v", n, err)
	}
}

func TestAddJournalEntriesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	entries := []JournalEntry{jentry(1, 10, 100, "bounty"), jentry(2, 20, -50, "fee")}

	n, err := s.AddJournalEntries(ctx, id, entries)
	if err != nil || n != 2 {
		t.Fatalf("first = %d, %v", n, err)
	}
	n, err = s.AddJournalEntries(ctx, id, append(entries, jentry(3, 30, 7, "bounty")))
	if err != nil || n != 1 {
		t.Fatalf("second = %d, %v (want 1 new)", n, err)
	}
	// The same entry id in another wallet is a different row.
	other := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	if n, err = s.AddJournalEntries(ctx, other, entries[:1]); err != nil || n != 1 {
		t.Fatalf("other wallet = %d, %v", n, err)
	}
	if n, err = s.AddJournalEntries(ctx, id, nil); err != nil || n != 0 {
		t.Fatalf("empty = %d, %v", n, err)
	}
}

func TestJournalPagingFiltersAndOrder(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	other := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	if _, err := s.AddJournalEntries(ctx, id, []JournalEntry{
		jentry(1, 10, 1, "bounty"), jentry(2, 20, 2, "fee"), jentry(3, 20, 3, "bounty"), jentry(4, 30, 4, "tax"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalEntries(ctx, other, []JournalEntry{jentry(9, 25, 9, "secret")}); err != nil {
		t.Fatal(err)
	}
	ids := func(es []JournalEntry) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}

	all, err := s.Journal(ctx, JournalFilter{WalletID: id, Limit: 10})
	if err != nil || !reflect.DeepEqual(ids(all), []int64{4, 3, 2, 1}) {
		t.Fatalf("all = %v, %v (newest first, id desc on ties)", ids(all), err)
	}
	if all[0].AmountCents != 4 || all[0].RefType != "tax" || all[0].Description != "dtax" || !all[0].Date.Equal(ts(30)) {
		t.Fatalf("fields = %+v", all[0])
	}
	page1, _ := s.Journal(ctx, JournalFilter{WalletID: id, Limit: 2})
	last := page1[len(page1)-1]
	page2, err := s.Journal(ctx, JournalFilter{WalletID: id, Limit: 2, After: &JournalCursor{Date: last.Date, ID: last.ID}})
	if err != nil || !reflect.DeepEqual(ids(page1), []int64{4, 3}) || !reflect.DeepEqual(ids(page2), []int64{2, 1}) {
		t.Fatalf("pages = %v %v, %v", ids(page1), ids(page2), err)
	}
	byType, _ := s.Journal(ctx, JournalFilter{WalletID: id, Limit: 10, RefType: "bounty"})
	if !reflect.DeepEqual(ids(byType), []int64{3, 1}) {
		t.Fatalf("by type = %v", ids(byType))
	}
	ranged, _ := s.Journal(ctx, JournalFilter{WalletID: id, Limit: 10, From: ts(20), To: ts(20)})
	if !reflect.DeepEqual(ids(ranged), []int64{3, 2}) {
		t.Fatalf("range (inclusive) = %v", ids(ranged))
	}
	types, err := s.JournalRefTypes(ctx, id)
	if err != nil || !reflect.DeepEqual(types, []string{"bounty", "fee", "tax"}) {
		t.Fatalf("ref types = %v, %v", types, err)
	}
	none, err := s.JournalRefTypes(ctx, 999)
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown wallet ref types = %v, %v", none, err)
	}
}

func TestJournalCascadesWithWallet(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	if _, err := s.AddJournalEntries(ctx, id, []JournalEntry{jentry(1, 10, 1, "x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM wallets WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("journal rows after delete = %d, %v", n, err)
	}
}

func TestJournalAmountsFiltersAndIgnoresPaging(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	id := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	other := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	if _, err := s.AddJournalEntries(ctx, id, []JournalEntry{
		jentry(1, 10, 1, "bounty"), jentry(2, 20, -2, "fee"), jentry(3, 20, 3, "bounty"), jentry(4, 30, -4, "tax"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalEntries(ctx, other, []JournalEntry{jentry(9, 25, 9, "secret")}); err != nil {
		t.Fatal(err)
	}
	sum := func(f JournalFilter) (int, int64) {
		f.WalletID = id
		got, err := s.JournalAmounts(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, a := range got {
			total += a.AmountCents
		}
		return len(got), total
	}
	if n, total := sum(JournalFilter{Limit: 1}); n != 4 || total != -2 {
		t.Fatalf("all (Limit must be ignored) = %d rows, sum %d", n, total)
	}
	if n, total := sum(JournalFilter{RefType: "bounty"}); n != 2 || total != 4 {
		t.Fatalf("by type = %d rows, sum %d", n, total)
	}
	if n, total := sum(JournalFilter{From: ts(20), To: ts(20)}); n != 2 || total != 1 {
		t.Fatalf("inclusive range = %d rows, sum %d", n, total)
	}
	got, _ := s.JournalAmounts(ctx, JournalFilter{WalletID: id})
	if len(got) != 4 || !got[0].Date.Equal(ts(10)) || got[0].AmountCents != 1 {
		t.Fatalf("rows must be oldest first with date and amount: %+v", got)
	}
}

func TestJournalAcrossWalletsPagesWithCollidingEntryIDs(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	a := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	b := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	c := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 3, OwnerName: "C"})
	// Entry ids collide across wallets on purpose, some at the same instant.
	if _, err := s.AddJournalEntries(ctx, a, []JournalEntry{jentry(1, 10, 1, "bounty"), jentry(2, 20, 2, "fee")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalEntries(ctx, b, []JournalEntry{jentry(1, 10, 3, "tax"), jentry(2, 20, 4, "fee")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalEntries(ctx, c, []JournalEntry{jentry(2, 20, 99, "secret")}); err != nil {
		t.Fatal(err)
	}
	type key struct{ wallet, id int64 }
	keys := func(es []JournalEntry) []key {
		var out []key
		for _, e := range es {
			out = append(out, key{e.WalletID, e.ID})
		}
		return out
	}
	ids := []int64{a, b}
	all, err := s.Journal(ctx, JournalFilter{WalletIDs: ids, Limit: 10})
	want := []key{{b, 2}, {a, 2}, {b, 1}, {a, 1}}
	if err != nil || !reflect.DeepEqual(keys(all), want) {
		t.Fatalf("all = %v, %v, want %v", keys(all), err, want)
	}
	var got []key
	var after *JournalCursor
	for range 4 {
		page, err := s.Journal(ctx, JournalFilter{WalletIDs: ids, Limit: 1, After: after})
		if err != nil || len(page) != 1 {
			t.Fatalf("page = %v, %v", page, err)
		}
		got = append(got, keys(page)...)
		after = &JournalCursor{Date: page[0].Date, ID: page[0].ID, WalletID: page[0].WalletID}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paged = %v, want %v", got, want)
	}
	if rest, _ := s.Journal(ctx, JournalFilter{WalletIDs: ids, Limit: 10, After: after}); len(rest) != 0 {
		t.Fatalf("after the last row = %v", rest)
	}
	byType, _ := s.Journal(ctx, JournalFilter{WalletIDs: ids, Limit: 10, RefType: "fee"})
	if !reflect.DeepEqual(keys(byType), []key{{b, 2}, {a, 2}}) {
		t.Fatalf("by type = %v", keys(byType))
	}
	ranged, _ := s.Journal(ctx, JournalFilter{WalletIDs: ids, Limit: 10, From: ts(10), To: ts(10)})
	if !reflect.DeepEqual(keys(ranged), []key{{b, 1}, {a, 1}}) {
		t.Fatalf("range = %v", keys(ranged))
	}
	// A single wallet keeps carrying its id.
	one, _ := s.Journal(ctx, JournalFilter{WalletID: a, Limit: 1})
	if len(one) != 1 || one[0].WalletID != a {
		t.Fatalf("single wallet entry = %+v", one)
	}
}

func TestJournalAmountsAndRefTypesAcrossWallets(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	a := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 1, OwnerName: "A"})
	b := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 2, OwnerName: "B"})
	c := mustWallet(t, s, Wallet{Kind: KindCharacter, OwnerID: 3, OwnerName: "C"})
	for w, es := range map[int64][]JournalEntry{
		a: {jentry(1, 10, 1, "bounty"), jentry(2, 30, -2, "fee")},
		b: {jentry(1, 20, 5, "tax")},
		c: {jentry(1, 20, 100, "secret")},
	} {
		if _, err := s.AddJournalEntries(ctx, w, es); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.JournalAmounts(ctx, JournalFilter{WalletIDs: []int64{a, b}})
	if err != nil || len(got) != 3 {
		t.Fatalf("amounts = %v, %v", got, err)
	}
	if got[0].AmountCents != 1 || got[1].AmountCents != 5 || got[2].AmountCents != -2 {
		t.Fatalf("amounts must be oldest first across wallets: %+v", got)
	}
	types, err := s.JournalRefTypesFor(ctx, []int64{a, b})
	if err != nil || !reflect.DeepEqual(types, []string{"bounty", "fee", "tax"}) {
		t.Fatalf("types union = %v, %v", types, err)
	}
}
