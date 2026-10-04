package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

func jentry(id int64, cents int64, ref string, balance *int64) esi.JournalEntry {
	return esi.JournalEntry{ID: id, Date: day1, AmountCents: cents, BalanceCents: balance, RefType: ref, Description: "desc " + ref}
}

func TestBackfillPersistsEveryJournalEntry(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{journals: map[string][]esi.JournalEntry{"char/1": {
		jentry(10, 500, "bounty_prizes", i64(1050)),
		jentry(11, -25, "market_fee", nil), // no balance: still a movement
	}}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.journal) != 2 {
		t.Fatalf("journal = %+v", s.journal)
	}
	got := s.journal[[2]int64{1, 11}]
	want := store.JournalEntry{ID: 11, Date: day1, AmountCents: -25, RefType: "market_fee", Description: "desc market_fee"}
	if got != want {
		t.Fatalf("entry = %+v, want %+v", got, want)
	}
	if len(s.points) != 1 || rep.Wallets[0].Points != 1 || rep.Wallets[0].NoBalance != 1 {
		t.Fatalf("balances changed: points=%+v report=%+v", s.points, rep.Wallets)
	}
	if rep.Wallets[0].NewEntries != 2 || rep.NewEntries() != 2 {
		t.Fatalf("new entries = %+v", rep.Wallets)
	}
}

func TestBackfillJournalIsIdempotentAndOnlyAddsNewEntries(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{journals: map[string][]esi.JournalEntry{"char/1": {jentry(10, 1, "a", i64(1)), jentry(11, 2, "b", i64(3))}}}
	s := &fakeStore{}
	c := newCollector(a, e, s)
	first, err := c.Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.NewEntries() != 2 || second.NewEntries() != 0 || len(s.journal) != 2 {
		t.Fatalf("first=%d second=%d rows=%d", first.NewEntries(), second.NewEntries(), len(s.journal))
	}
	e.journals["char/1"] = append(e.journals["char/1"], jentry(12, 4, "c", i64(7)))
	third, _ := c.Backfill(context.Background())
	if third.NewEntries() != 1 || len(s.journal) != 3 {
		t.Fatalf("third=%d rows=%d", third.NewEntries(), len(s.journal))
	}
}

func TestBackfillJournalWriteFailureKeepsBalancesAndIsReported(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{journals: map[string][]esi.JournalEntry{"char/1": {jentry(10, 1, "a", i64(1))}}}
	s := &fakeStore{entriesErr: errors.New("disk full")}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.points) != 1 || len(rep.Errors) != 1 || rep.Wallets[0].Points != 1 {
		t.Fatalf("points=%+v report=%+v", s.points, rep)
	}
}

func TestBackfillCorporationJournalPersistedPerDivision(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1}, {Division: 3}}},
		journals: map[string][]esi.JournalEntry{
			"corp/900/1": {jentry(1, 5, "a", nil)},
			"corp/900/3": {jentry(1, 6, "b", nil)}, // same entry id, other wallet
		},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.journal) != 2 {
		t.Fatalf("journal = %+v", s.journal)
	}
}
