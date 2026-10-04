package collector

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
)

const divScope = "esi-corporations.read_divisions.v1"

func director(id int64, name string) auth.Character {
	return auth.Character{ID: id, Name: name, Scopes: []string{charScope, corpScope, divScope}}
}

// namesByDivision returns the stored ESI names of corporation wallets keyed by
// division, resolving the fake store's wallet ids.
func namesByDivision(s *fakeStore) map[int]string {
	out := make(map[int]string)
	for id, name := range s.esiNames {
		out[s.wallets[id-1].Division] = name
	}
	return out
}

func hasCall(calls []string, want string) bool { return slices.Contains(calls, want) }

func TestNamesSetForPresentClearedForAbsent(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}, {Division: 2, Cents: 2}, {Division: 3, Cents: 3}}},
		divisions:  map[string]esi.DivisionNames{"900/1": {1: "Master", 3: "Ops", 6: "Not stored"}},
	}
	// Wallet ids: 1 is Alice's personal wallet, 2..4 are divisions 1..3.
	s := &fakeStore{esiNames: map[int64]string{3: "Stale"}}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Division 2 is cleared and division 6 has no wallet, so it is not stored.
	if got := namesByDivision(s); len(got) != 2 || got[1] != "Master" || got[3] != "Ops" {
		t.Fatalf("names = %v", got)
	}
	if rep.NamesUpdated != 3 {
		t.Fatalf("NamesUpdated = %d, want 3", rep.NamesUpdated)
	}
	if len(rep.Errors) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNamesUntouchedAfterFailedCall(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": &esi.APIError{Status: 500}},
	}
	s := &fakeStore{esiNames: map[int64]string{2: "Keep me"}}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.esiNames[2] != "Keep me" || s.nameCalls != 0 {
		t.Fatalf("names = %v, calls = %d", s.esiNames, s.nameCalls)
	}
	if len(rep.Errors) != 1 || rep.NamesUpdated != 0 || len(rep.Snapshots) != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNamesForbiddenIsSkipThenSecondCharacterSucceeds(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice"), director(2, "Bob")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": forbidden()},
		divisions:  map[string]esi.DivisionNames{"900/2": {1: "Master"}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if namesByDivision(s)[1] != "Master" || rep.NamesUpdated != 1 {
		t.Fatalf("names = %v, report = %+v", namesByDivision(s), rep)
	}
	var denied int
	for _, sk := range rep.Skipped {
		if sk.Reason == ReasonMissingDirector {
			denied++
		}
	}
	if denied != 1 || ReasonMissingDirector != "cannot read division names (needs the Director role); default names are shown" || len(rep.Errors) != 0 {
		t.Fatalf("skipped = %+v errors = %+v", rep.Skipped, rep.Errors)
	}
}

func TestNamesForbiddenForEveryone(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": forbidden()},
	}
	s := &fakeStore{esiNames: map[int64]string{2: "Keep me"}}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.esiNames[2] != "Keep me" || len(rep.Errors) != 0 || len(rep.Snapshots) != 2 {
		t.Fatalf("names = %v, report = %+v", namesByDivision(s), rep)
	}
	if !slices.ContainsFunc(rep.Skipped, func(k Skip) bool { return k.Reason == ReasonMissingDirector }) {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
}

func TestNamesWithoutScopeDoNothingAndRecordNothing(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range e.calls {
		if strings.HasPrefix(c, "divisions/") {
			t.Fatalf("unexpected call %s", c)
		}
	}
	if s.nameCalls != 0 || len(rep.Skipped) != 0 || len(rep.Errors) != 0 || rep.NamesUpdated != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNamesLaterCharacterWithScopeStillFetches(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}},
		director(2, "Bob"),
	}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divisions:  map[string]esi.DivisionNames{"900/2": {1: "Master"}},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if namesByDivision(s)[1] != "Master" {
		t.Fatalf("names = %v", namesByDivision(s))
	}
}

func TestNamesFetchedOncePerCorporation(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice"), director(2, "Bob")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divisions:  map[string]esi.DivisionNames{"900/1": {1: "Master"}, "900/2": {1: "Other"}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasCall(e.calls, "divisions/900/2") || !hasCall(e.calls, "divisions/900/1") {
		t.Fatalf("calls = %v", e.calls)
	}
	if namesByDivision(s)[1] != "Master" || rep.NamesUpdated != 1 {
		t.Fatalf("names = %v, report = %+v", namesByDivision(s), rep)
	}
}

func TestNamesStoreFailureIsItemError(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divisions:  map[string]esi.DivisionNames{"900/1": {1: "Master"}},
	}
	s := &fakeStore{nameErr: errors.New("disk full")}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Owner != "Acme" || rep.NamesUpdated != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNamesRateLimitStops(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice"), director(2, "Bob")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 901},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}, "901/2": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": &esi.RateLimitError{Status: 429, RetryAfter: 20 * time.Second}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RateLimited || rep.RetryAfter != 20*time.Second || len(rep.Snapshots) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if hasCall(e.calls, "corpid/2") {
		t.Fatalf("no ESI call may follow a rate limit: %v", e.calls)
	}
}

func TestNamesContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divisions:  map[string]esi.DivisionNames{"900/1": {1: "Master"}},
	}
	e.onCall = func(c string) {
		if c == "corpname/900" {
			cancel()
		}
	}
	s := &fakeStore{}
	_, err := newCollector(a, e, s).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hasCall(e.calls, "divisions/900/1") || s.nameCalls != 0 {
		t.Fatalf("no names call after cancel: %v", e.calls)
	}
}

func TestBackfillNeverRequestsNames(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{director(1, "Alice")}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divisions:  map[string]esi.DivisionNames{"900/1": {1: "Master"}},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.calls {
		if strings.HasPrefix(c, "divisions/") {
			t.Fatalf("Backfill requested names: %v", e.calls)
		}
	}
	if s.nameCalls != 0 {
		t.Fatalf("nameCalls = %d", s.nameCalls)
	}
}

func directorOf(id int64, name string, user int64) auth.Character {
	c := director(id, name)
	c.UserID = user
	return c
}

func skipsOf(rep Report, reason string) []Skip {
	var out []Skip
	for _, k := range rep.Skipped {
		if k.Reason == reason {
			out = append(out, k)
		}
	}
	return out
}

func TestSkipsUseRealCorporationNameAndAreDeduplicated(t *testing.T) {
	// Two characters of one user, both denied the division names: one skip,
	// under the real name, and no "already collected" noise.
	a := &fakeAuth{chars: []auth.Character{directorOf(1, "Alice", 10), directorOf(2, "Bob", 10)}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": forbidden(), "900/2": forbidden()},
	}
	rep, err := newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly one", rep.Skipped)
	}
	if k := rep.Skipped[0]; k.Reason != ReasonMissingDirector || k.Owner != "Acme" || k.OwnerID != 900 {
		t.Fatalf("skip = %+v", k)
	}
}

func TestSkipsStayVisibleToEachUser(t *testing.T) {
	// Two users denied the same thing: the dedupe must not hide it from one.
	a := &fakeAuth{chars: []auth.Character{directorOf(1, "Alice", 10), directorOf(2, "Bob", 20)}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}, "900/2": {{Division: 1, Cents: 1}}},
		divErr:     map[string]error{"900/1": forbidden(), "900/2": forbidden()},
	}
	rep, err := newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := skipsOf(rep, ReasonMissingDirector)
	if len(got) != 2 || got[0].UserID != 10 || got[1].UserID != 20 {
		t.Fatalf("skips = %+v, want one per user (10 and 20)", rep.Skipped)
	}
	for _, k := range got {
		if k.Owner != "Acme" {
			t.Errorf("skip = %+v, want owner Acme", k)
		}
	}
}

func TestVerifyFailureAndErrorsUseRealCorporationName(t *testing.T) {
	a, e := twoUsersOneCorp()
	e.corpErr = map[string]error{"900/2": forbidden()}
	rep, err := newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Owner != "Acme" {
		t.Fatalf("skipped = %+v, want one skip under Acme", rep.Skipped)
	}

	a, e = twoUsersOneCorp()
	e.corpErr = map[string]error{"900/2": errors.New("esi boom")}
	rep, err = newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Owner != "Acme" {
		t.Fatalf("errors = %+v, want one under Acme", rep.Errors)
	}
}
