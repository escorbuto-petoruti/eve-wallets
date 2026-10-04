package collector

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const lpScope = "esi-characters.read_loyalty.v1"

func lpChar(id int64, name string) auth.Character {
	return auth.Character{ID: id, Name: name, UserID: id, Scopes: []string{charScope, corpScope, lpScope}}
}

func TestLoyaltyIsFetchedAndStored(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice"), lpChar(2, "Bob")}}
	e := &fakeESI{
		wallets: map[int64]int64{1: 100, 2: 200},
		corpOf:  map[int64]int64{1: 500, 2: 500},
		corpWallet: map[string][]esi.DivisionBalance{
			"500/1": {{Division: 1, Cents: 5}},
		},
		loyalty: map[int64][]esi.LoyaltyPoints{
			1: {{CorporationID: 1000125, Points: 9000}, {CorporationID: 1000002, Points: 7}},
			2: {{CorporationID: 1000125, Points: 1}},
		},
		universe: map[int64]string{1000125: "Caldari Navy", 1000002: "CBD Corporation"},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	want := map[int64][]store.LoyaltyPoints{
		1: {{CorporationID: 1000125, Points: 9000}, {CorporationID: 1000002, Points: 7}},
		2: {{CorporationID: 1000125, Points: 1}},
	}
	if !reflect.DeepEqual(s.loyalty, want) {
		t.Fatalf("stored loyalty = %+v", s.loyalty)
	}
	// One batched, deduplicated lookup for the whole run.
	if len(e.nameBatches) != 1 {
		t.Fatalf("name lookups = %v, want exactly one", e.nameBatches)
	}
	got := slices.Clone(e.nameBatches[0])
	slices.Sort(got)
	if !reflect.DeepEqual(got, []int64{1000002, 1000125}) {
		t.Fatalf("looked up %v", got)
	}
	if s.corpNames[1000125] != "Caldari Navy" || s.corpNames[1000002] != "CBD Corporation" {
		t.Fatalf("cached names = %v", s.corpNames)
	}
	// The ISK wallets are untouched by the new feature.
	if len(rep.Snapshots) < 2 {
		t.Fatalf("snapshots = %+v", rep.Snapshots)
	}
}

func TestLoyaltyCachedNamesAreNotLookedUpAgain(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{
		loyalty:  map[int64][]esi.LoyaltyPoints{1: {{CorporationID: 10, Points: 1}, {CorporationID: 11, Points: 2}}},
		universe: map[int64]string{11: "Eleven"},
	}
	s := &fakeStore{corpNames: map[int64]string{10: "Ten"}}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.nameBatches) != 1 || !reflect.DeepEqual(e.nameBatches[0], []int64{11}) {
		t.Fatalf("lookups = %v, want only the uncached id", e.nameBatches)
	}

	// Everything cached: no call at all.
	e.nameBatches = nil
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.nameBatches) != 0 {
		t.Fatalf("lookups = %v, want none", e.nameBatches)
	}
}

func TestLoyaltyNameLookupFailureIsNotAnError(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{
		loyalty:     map[int64][]esi.LoyaltyPoints{1: {{CorporationID: 10, Points: 1}}},
		universeErr: &esi.APIError{Status: 500},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 || rep.RateLimited {
		t.Fatalf("report = %+v", rep)
	}
	if len(s.loyalty[1]) != 1 || len(s.corpNames) != 0 {
		t.Fatalf("loyalty = %+v, names = %v", s.loyalty, s.corpNames)
	}
}

func TestLoyaltyEmptyResponseClearsTheSnapshot(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{}
	s := &fakeStore{loyalty: map[int64][]store.LoyaltyPoints{1: {{CorporationID: 10, Points: 1}}}}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.replaceCalls != 1 || len(s.loyalty[1]) != 0 {
		t.Fatalf("replaceCalls = %d, snapshot = %+v", s.replaceCalls, s.loyalty)
	}
	if len(e.nameBatches) != 0 {
		t.Fatalf("lookups = %v", e.nameBatches)
	}
}

func TestLoyaltyMissingScopeIsASkip(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 1, Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{wallets: map[int64]int64{1: 100}, corpOf: map[int64]int64{1: 500}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(e.calls, func(c string) bool { return strings.HasPrefix(c, "loyalty/") }) {
		t.Fatalf("calls = %v, no loyalty call without the scope", e.calls)
	}
	if s.replaceCalls != 0 {
		t.Fatal("a skipped character must keep its stored snapshot")
	}
	var found bool
	for _, k := range rep.Skipped {
		if k.OwnerKind == store.KindCharacter && k.OwnerID == 1 && k.Owner == "Alice" && k.UserID == 1 &&
			k.Reason == "missing scope "+lpScope {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	if len(rep.Errors) != 0 || len(s.snaps) == 0 || s.snaps[0].cents != 100 {
		t.Fatalf("the ISK wallet must still be collected: errors %+v snaps %+v", rep.Errors, s.snaps)
	}
}

func TestLoyaltyForbiddenIsASkipAndKeepsISK(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100},
		corpOf:     map[int64]int64{1: 500},
		loyaltyErr: map[int64]error{1: forbidden()},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 || s.replaceCalls != 0 {
		t.Fatalf("errors = %+v, replaceCalls = %d", rep.Errors, s.replaceCalls)
	}
	if !slices.ContainsFunc(rep.Skipped, func(k Skip) bool { return k.OwnerID == 1 && strings.Contains(k.Reason, lpScope) }) {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	if len(s.snaps) == 0 || s.snaps[0].cents != 100 {
		t.Fatalf("snaps = %+v", s.snaps)
	}
}

func TestLoyaltyOtherFailureIsAnItemError(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice"), lpChar(2, "Bob")}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100, 2: 200},
		loyaltyErr: map[int64]error{1: &esi.APIError{Status: 500}},
		loyalty:    map[int64][]esi.LoyaltyPoints{2: {{CorporationID: 7, Points: 3}}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].OwnerID != 1 || rep.Errors[0].OwnerKind != store.KindCharacter {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	if _, ok := s.loyalty[1]; ok || len(s.loyalty[2]) != 1 {
		t.Fatalf("loyalty = %+v: the failing character is skipped, the next one is stored", s.loyalty)
	}
}

func TestLoyaltyRateLimitStopsTheRun(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice"), lpChar(2, "Bob")}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100, 2: 200},
		loyaltyErr: map[int64]error{1: &esi.RateLimitError{Status: 420}},
	}
	rep, err := newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil || !rep.RateLimited {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
	if slices.Contains(e.calls, "wallet/2") || slices.Contains(e.calls, "loyalty/2") {
		t.Fatalf("calls = %v, the run must stop issuing calls", e.calls)
	}
}

func TestLoyaltyStoreFailureIsAnItemError(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{loyalty: map[int64][]esi.LoyaltyPoints{1: {{CorporationID: 7, Points: 3}}}}
	s := &fakeStore{replaceErr: errors.New("disk full")}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "disk full") {
		t.Fatalf("rep = %+v, err = %v", rep, err)
	}
}

func TestBackfillDoesNotFetchLoyalty(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{lpChar(1, "Alice")}}
	e := &fakeESI{corpOf: map[int64]int64{1: 500}}
	if _, err := newCollector(a, e, &fakeStore{}).Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.calls {
		if strings.HasPrefix(c, "loyalty/") || c == "universe" {
			t.Fatalf("calls = %v", e.calls)
		}
	}
}
