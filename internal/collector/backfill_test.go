package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

func i64(v int64) *int64 { return &v }

func entry(id int64, at time.Time, balance *int64) esi.JournalEntry {
	return esi.JournalEntry{ID: id, Date: at, BalanceCents: balance}
}

var (
	day1 = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 9, 21, 11, 30, 0, 0, time.UTC)
)

func TestBackfillPersonalStoresExactPoints(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{journals: map[string][]esi.JournalEntry{"char/1": {
		entry(10, day1, i64(1050)),
		entry(11, day2, i64(-25)),
		entry(12, day2, nil),
	}}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.wallets) != 1 || s.wallets[0] != (store.Wallet{Kind: store.KindCharacter, OwnerID: 1, OwnerName: "Alice"}) {
		t.Fatalf("wallets = %+v", s.wallets)
	}
	want := map[[2]int64]point{{1, 10}: {day1, 1050}, {1, 11}: {day2, -25}}
	if len(s.points) != len(want) {
		t.Fatalf("points = %+v", s.points)
	}
	for k, p := range want {
		if s.points[k] != p {
			t.Errorf("point %v = %+v, want %+v", k, s.points[k], p)
		}
	}
	if len(rep.Wallets) != 1 || rep.Wallets[0].Points != 2 || rep.Wallets[0].NoBalance != 1 {
		t.Fatalf("report = %+v", rep.Wallets)
	}
	if rep.Points() != 2 || rep.NoBalance() != 1 || len(rep.Errors) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBackfillCorporationDivisions(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1}, {Division: 3}}},
		journals: map[string][]esi.JournalEntry{
			"corp/900/1": {entry(1, day1, i64(500))},
			"corp/900/3": {entry(2, day2, i64(700)), entry(3, day2, i64(710))},
		},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.wallets) != 2 || s.wallets[0].Division != 1 || s.wallets[1] != (store.Wallet{Kind: store.KindCorporation, OwnerID: 900, OwnerName: "Acme", Division: 3}) {
		t.Fatalf("wallets = %+v", s.wallets)
	}
	if len(s.points) != 3 || rep.Points() != 3 || len(rep.Wallets) != 2 {
		t.Fatalf("points=%+v report=%+v", s.points, rep)
	}
	for _, call := range e.calls {
		if call == "journal/corp/900/2" {
			t.Fatalf("fetched a division that does not exist: %v", e.calls)
		}
	}
}

func TestBackfillCorporationOnceWithTwoCharacters(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope, corpScope}},
	}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1}}, "900/2": {{Division: 1}}},
		journals:   map[string][]esi.JournalEntry{"corp/900/1": {entry(1, day1, i64(5))}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range e.calls {
		if c == "journal/corp/900/1" {
			n++
		}
	}
	corpWallets := 0
	for _, w := range rep.Wallets {
		if w.Kind == store.KindCorporation {
			corpWallets++
		}
	}
	if n != 1 || corpWallets != 1 {
		t.Fatalf("journal fetches = %d, report = %+v, calls = %v", n, rep, e.calls)
	}
	if len(rep.Skipped) != 0 {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
}

func TestBackfillForbiddenThenSecondCharacterSucceeds(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope, corpScope}},
	}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/2": {{Division: 1}}},
		corpErr:    map[string]error{"900/1": forbidden()},
		journals:   map[string][]esi.JournalEntry{"corp/900/1": {entry(1, day1, i64(5))}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Reason != ReasonMissingRole || rep.Skipped[0].Owner != "Corp 900" {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	if len(s.points) != 1 || len(rep.Errors) != 0 {
		t.Fatalf("points=%+v report=%+v", s.points, rep)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 2}}},
		journals: map[string][]esi.JournalEntry{
			"char/1":     {entry(10, day1, i64(1)), entry(11, day2, i64(2))},
			"corp/900/2": {entry(20, day1, i64(3))},
		},
	}
	s := &fakeStore{}
	c := newCollector(a, e, s)
	first, err := c.Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, wallets := len(s.points), len(s.wallets)
	second, err := c.Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.points) != before || len(s.wallets) != wallets || before != 3 {
		t.Fatalf("points %d -> %d, wallets %d -> %d", before, len(s.points), wallets, len(s.wallets))
	}
	if first.Points() != second.Points() {
		t.Fatalf("reports differ: %d vs %d", first.Points(), second.Points())
	}
}

func TestBackfillRateLimitReturnsPartialReport(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{
		journals:   map[string][]esi.JournalEntry{"char/1": {entry(1, day1, i64(9))}},
		journalErr: map[string]error{"char/2": &esi.RateLimitError{Status: 429, RetryAfter: 30 * time.Second}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RateLimited || rep.RetryAfter != 30*time.Second || len(s.points) != 1 || len(rep.Errors) != 0 {
		t.Fatalf("report = %+v points=%+v", rep, s.points)
	}
}

func TestBackfillContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{
		journals: map[string][]esi.JournalEntry{"char/1": {entry(1, day1, i64(9))}},
		onCall: func(c string) {
			if c == "journal/char/1" {
				cancel()
			}
		},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range e.calls {
		if c == "journal/char/2" {
			t.Fatalf("calls after cancel: %v", e.calls)
		}
	}
	_ = rep
}

func TestBackfillPerItemErrorDoesNotAbort(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{
		journals:   map[string][]esi.JournalEntry{"char/2": {entry(1, day1, i64(9))}},
		journalErr: map[string]error{"char/1": errors.New("boom")},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Owner != "Alice" || len(s.points) != 1 {
		t.Fatalf("report = %+v points=%+v", rep, s.points)
	}
}

func TestBackfillStoreFailureRecorded(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{journals: map[string][]esi.JournalEntry{"char/1": {entry(1, day1, i64(9)), entry(2, day2, i64(8))}}}
	s := &fakeStore{journalErr: errors.New("disk full")}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "disk full") {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

func TestBackfillListFailureIsFatal(t *testing.T) {
	a := &fakeAuth{listErr: errors.New("no eve-auth")}
	if _, err := newCollector(a, &fakeESI{}, &fakeStore{}).Backfill(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

// Backfill skips and errors must carry the owner identity too, so the status
// endpoint can scope them to the signed-in user.
func TestBackfillSkipsAndErrorsCarryOwnerIdentity(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}}, // corporation journal store fails
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},              // corporation scope missing
		{ID: 3, Name: "Cara", Scopes: []string{charScope, corpScope}},  // corporation journal forbidden
	}}
	e := &fakeESI{
		corpOf: map[int64]int64{1: 900, 3: 800},
		corpWallet: map[string][]esi.DivisionBalance{
			"900/1": {{Division: 1}},
			"800/3": {{Division: 1}},
		},
		corpErr: map[string]error{"800/3": forbidden()},
		journals: map[string][]esi.JournalEntry{
			"char/1":     {entry(10, day1, i64(1050))},
			"corp/900/1": {entry(20, day1, i64(500))},
		},
	}
	s := &fakeStore{journalErr: errors.New("db down")}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 2 || len(rep.Errors) != 2 {
		t.Fatalf("skipped=%+v errors=%+v", rep.Skipped, rep.Errors)
	}
	for _, k := range rep.Skipped {
		switch k.Reason {
		case ReasonMissingRole:
			if k.OwnerKind != store.KindCorporation || k.OwnerID != 800 || k.Owner != "Corp 800" {
				t.Errorf("corporation skip = %+v, want kind corporation, id 800", k)
			}
		case "missing scope " + corpScope:
			if k.OwnerKind != store.KindCharacter || k.OwnerID != 2 || k.Owner != "Bob" {
				t.Errorf("character skip = %+v, want kind character, id 2", k)
			}
		default:
			t.Errorf("unexpected skip = %+v", k)
		}
	}
	for _, ie := range rep.Errors {
		switch ie.Owner {
		case "Alice":
			if ie.OwnerKind != store.KindCharacter || ie.OwnerID != 1 {
				t.Errorf("character error = %+v, want kind character, id 1", ie)
			}
		case "Corp 900 (division 1)":
			if ie.OwnerKind != store.KindCorporation || ie.OwnerID != 900 {
				t.Errorf("corporation error = %+v, want kind corporation, id 900", ie)
			}
		default:
			t.Errorf("unexpected error = %+v", ie)
		}
	}
}
