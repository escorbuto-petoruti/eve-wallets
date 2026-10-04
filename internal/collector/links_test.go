package collector

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

func sortedLinks(s *fakeStore) [][2]int64 {
	out := slices.Clone(s.linkCalls)
	slices.SortFunc(out, func(a, b [2]int64) int {
		if a[0] != b[0] {
			return int(a[0] - b[0])
		}
		return int(a[1] - b[1])
	})
	return out
}

// twoUsersOneCorp is two characters of different users in the same corporation.
func twoUsersOneCorp() (*fakeAuth, *fakeESI) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope, corpScope, lpScope}},
		{ID: 2, Name: "Bob", UserID: 20, Scopes: []string{charScope, corpScope, lpScope}},
	}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100, 2: 200},
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 5}, {Division: 2, Cents: 6}}, "900/2": {{Division: 1, Cents: 5}, {Division: 2, Cents: 6}}},
	}
	return a, e
}

func TestRunLinksPersonalAndCorporationWallets(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100},
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 5}, {Division: 2, Cents: 6}}},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Wallet ids: 1 personal, 2 and 3 the corporation divisions.
	want := [][2]int64{{10, 1}, {10, 2}, {10, 3}}
	if got := sortedLinks(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
}

func TestRunSecondUserWithRoleIsLinkedAfterOwnVerificationFetch(t *testing.T) {
	a, e := twoUsersOneCorp()
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Alice personal=1, corp divisions 2,3; Bob personal=4.
	want := [][2]int64{{10, 1}, {10, 2}, {10, 3}, {20, 2}, {20, 3}, {20, 4}}
	if got := sortedLinks(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
	// One fetch per user, each with that character's own token; balances are
	// stored once (3 wallets of Alice and Bob's personal one: 4 snapshots).
	if got := callsWithPrefix(e, "corpwallets/"); !reflect.DeepEqual(got, []string{"corpwallets/900/1", "corpwallets/900/2"}) {
		t.Fatalf("corp wallet fetches = %v", got)
	}
	if len(s.snaps) != 4 || len(rep.Errors) != 0 {
		t.Fatalf("snaps = %d, errors = %v", len(s.snaps), rep.Errors)
	}
}

func TestRunSecondUserWithoutRoleIsNotLinked(t *testing.T) {
	a, e := twoUsersOneCorp()
	e.corpErr = map[string]error{"900/2": forbidden()}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int64{{10, 1}, {10, 2}, {10, 3}, {20, 4}}
	if got := sortedLinks(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v (Bob must not see the corporation)", got, want)
	}
	var missing bool
	for _, sk := range rep.Skipped {
		if sk.Reason == ReasonMissingRole {
			missing = true
			if sk.UserID != 20 {
				t.Errorf("skip user = %d, want 20 (Bob's character hit the 403)", sk.UserID)
			}
		}
	}
	if !missing || len(rep.Errors) != 0 {
		t.Fatalf("skipped = %+v, errors = %+v", rep.Skipped, rep.Errors)
	}
}

func TestRunSecondUserVerificationErrorIsReportedAndNotLinked(t *testing.T) {
	a, e := twoUsersOneCorp()
	e.corpErr = map[string]error{"900/2": errors.New("esi boom")}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedLinks(s); !reflect.DeepEqual(got, [][2]int64{{10, 1}, {10, 2}, {10, 3}, {20, 4}}) {
		t.Fatalf("links = %v", got)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "esi boom") {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

func TestRunSecondUserVerificationRateLimitStopsTheRun(t *testing.T) {
	a, e := twoUsersOneCorp()
	e.corpErr = map[string]error{"900/2": &esi.RateLimitError{RetryAfter: 30 * time.Second}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RateLimited || rep.RetryAfter != 30*time.Second {
		t.Fatalf("report = %+v", rep)
	}
	for _, l := range s.linkCalls {
		if l[0] == 20 && l[1] != 4 { // wallet 4 is Bob's own personal wallet
			t.Fatalf("Bob must not be linked to the corporation: %v", s.linkCalls)
		}
	}
}

func TestRunSameUserSecondCharacterCausesNoExtraFetch(t *testing.T) {
	a, e := twoUsersOneCorp()
	a.chars[1].UserID = 10 // Bob's character belongs to Alice's user
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := countCalls(e, "corpwallets/"); n != 1 {
		t.Fatalf("corp wallet fetches = %d, want 1 (calls %v)", n, e.calls)
	}
	if len(rep.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", rep.Skipped)
	}
}

func TestRunLinksEachUserWalletOnce(t *testing.T) {
	// Two characters of the same user reading the same corporation.
	a, e := twoUsersOneCorp()
	a.chars[1].UserID = 10
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := map[[2]int64]int{}
	for _, l := range s.linkCalls {
		seen[l]++
	}
	for l, n := range seen {
		if n != 1 {
			t.Fatalf("link %v made %d times", l, n)
		}
	}
}

func TestRunUserIDZeroMakesNoLinkCalls(t *testing.T) {
	a, e := twoUsersOneCorp()
	a.chars[0].UserID, a.chars[1].UserID = 0, 0
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.linkCalls) != 0 || len(rep.Errors) != 0 {
		t.Fatalf("links = %v, errors = %v", s.linkCalls, rep.Errors)
	}
	// Without users there is nothing to verify: the corporation is fetched once.
	if n := countCalls(e, "corpwallets/"); n != 1 {
		t.Fatalf("corp wallet fetches = %d, want 1 (calls %v)", n, e.calls)
	}
}

func TestRunLinkFailureIsReportedLikeStoreErrors(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope}}}}
	e := &fakeESI{wallets: map[int64]int64{1: 100}}
	s := &fakeStore{linkErr: errors.New("link boom")}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Owner != "Alice" || !strings.Contains(rep.Errors[0].Error(), "link boom") {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	if len(s.snaps) != 1 {
		t.Fatal("the snapshot must still be stored when only the link fails")
	}
}

func TestRunSecondUserLinkFailureOnSharedCorpIsReported(t *testing.T) {
	a, e := twoUsersOneCorp()
	s := &fakeStore{}
	// Fail only Bob's links.
	failing := &failingLinkStore{fakeStore: s, user: 20}
	c := New(Deps{Auth: a, ESI: e, Store: failing, Now: func() time.Time { return fixedNow }})
	rep, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) == 0 {
		t.Fatal("want link errors for Bob")
	}
}

func TestBackfillLinksPersonalAndCorporationWallets(t *testing.T) {
	a, e := twoUsersOneCorp()
	bal := int64(1)
	e.journals = map[string][]esi.JournalEntry{
		"char/1":     {{ID: 1, BalanceCents: &bal}},
		"char/2":     {{ID: 2, BalanceCents: &bal}},
		"corp/900/1": {{ID: 3, BalanceCents: &bal}},
		"corp/900/2": {{ID: 4, BalanceCents: &bal}},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][2]int64{{10, 1}, {10, 2}, {10, 3}, {20, 2}, {20, 3}, {20, 4}}
	if got := sortedLinks(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
	if n := countCalls(e, "journal/corp/900/1"); n != 1 {
		t.Fatalf("corp journal fetches = %d, want 1", n)
	}
	// Bob proves his role with his own token; journals are not fetched again.
	if got := callsWithPrefix(e, "corpwallets/"); !reflect.DeepEqual(got, []string{"corpwallets/900/1", "corpwallets/900/2"}) {
		t.Fatalf("corp wallet fetches = %v", got)
	}
}

func TestBackfillUserIDZeroMakesNoLinkCalls(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.linkCalls) != 0 {
		t.Fatalf("links = %v", s.linkCalls)
	}
}

func TestBackfillLinkFailureIsReported(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope}}}}
	e := &fakeESI{}
	s := &fakeStore{linkErr: errors.New("link boom")}
	rep, err := newCollector(a, e, s).Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "link boom") {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

func callsWithPrefix(e *fakeESI, prefix string) []string {
	var out []string
	for _, c := range e.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func countCalls(e *fakeESI, prefix string) int {
	n := 0
	for _, c := range e.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// failingLinkStore fails LinkWallet for one user.
type failingLinkStore struct {
	*fakeStore
	user int64
}

func (f *failingLinkStore) LinkWallet(ctx context.Context, userID, walletID int64) error {
	if userID == f.user {
		return errors.New("link boom")
	}
	return f.fakeStore.LinkWallet(ctx, userID, walletID)
}

var _ = store.KindCharacter

// After a character moves to another user, MoveToken drops the previous owner's
// corporation links. The collector restores them on the next Run only for the
// users whose remaining characters can still read the corporation.
func TestRunRestoresCorporationLinksOfARemainingCharacterThatCanReadIt(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100},
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 5}}},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Wallet ids: 1 personal, 2 the corporation division.
	if got, want := sortedLinks(s), [][2]int64{{10, 1}, {10, 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
}

func TestRunDoesNotRestoreCorporationLinksOfARemainingCharacterThatCannotReadIt(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100},
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 5}}},
		corpErr:    map[string]error{"900/1": forbidden()},
	}
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedLinks(s), [][2]int64{{10, 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v (no corporation link without the role)", got, want)
	}
}
