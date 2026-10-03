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
		{ID: 1, Name: "Alice", UserID: 10, Scopes: []string{charScope, corpScope}},
		{ID: 2, Name: "Bob", UserID: 20, Scopes: []string{charScope, corpScope}},
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

func TestRunTwoUsersSameCorpBothLinkedWithOneFetch(t *testing.T) {
	a, e := twoUsersOneCorp()
	s := &fakeStore{}
	if _, err := newCollector(a, e, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Alice personal=1, corp divisions 2,3; Bob personal=4.
	want := [][2]int64{{10, 1}, {10, 2}, {10, 3}, {20, 2}, {20, 3}, {20, 4}}
	if got := sortedLinks(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
	if n := countCalls(e, "corpwallets/"); n != 1 {
		t.Fatalf("corp wallet fetches = %d, want 1 (calls %v)", n, e.calls)
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
