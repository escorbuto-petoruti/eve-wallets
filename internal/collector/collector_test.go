package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const (
	charScope = "esi-wallet.read_character_wallet.v1"
	corpScope = "esi-wallet.read_corporation_wallets.v1"
)

type fakeAuth struct {
	chars     []auth.Character
	listErr   error
	tokenErrs map[int64]error
	tokenCall []int64
}

func (f *fakeAuth) Characters(context.Context) ([]auth.Character, error) { return f.chars, f.listErr }

func (f *fakeAuth) Token(_ context.Context, id int64) (string, error) {
	f.tokenCall = append(f.tokenCall, id)
	if err := f.tokenErrs[id]; err != nil {
		return "", err
	}
	return fmt.Sprintf("tok-%d", id), nil
}

type fakeESI struct {
	wallets    map[int64]int64 // character id -> cents
	walletErr  map[int64]error
	corpOf     map[int64]int64 // character id -> corp id
	corpOfErr  error
	corpNames  map[int64]string
	nameErr    error
	corpWallet map[string][]esi.DivisionBalance // "corp/char" -> divisions
	corpErr    map[string]error
	divisions  map[string]esi.DivisionNames // "corp/char" -> custom wallet names
	divErr     map[string]error
	calls      []string
	onCall     func(call string) // optional hook

	journals   map[string][]esi.JournalEntry // "char/<id>" or "corp/<id>/<division>"
	journalErr map[string]error
}

func (f *fakeESI) record(c string) {
	f.calls = append(f.calls, c)
	if f.onCall != nil {
		f.onCall(c)
	}
}

func (f *fakeESI) CharacterWallet(_ context.Context, token string, id int64) (int64, error) {
	f.record(fmt.Sprintf("wallet/%d", id))
	if err := f.walletErr[id]; err != nil {
		return 0, err
	}
	return f.wallets[id], nil
}

func (f *fakeESI) CharacterCorporationID(_ context.Context, id int64) (int64, error) {
	f.record(fmt.Sprintf("corpid/%d", id))
	if f.corpOfErr != nil {
		return 0, f.corpOfErr
	}
	return f.corpOf[id], nil
}

func (f *fakeESI) CorporationName(_ context.Context, corp int64) (string, error) {
	f.record(fmt.Sprintf("corpname/%d", corp))
	if f.nameErr != nil {
		return "", f.nameErr
	}
	return f.corpNames[corp], nil
}

func (f *fakeESI) CorporationWallets(_ context.Context, token string, corp int64) ([]esi.DivisionBalance, error) {
	char := strings.TrimPrefix(token, "tok-")
	key := fmt.Sprintf("%d/%s", corp, char)
	f.record("corpwallets/" + key)
	if err := f.corpErr[key]; err != nil {
		return nil, err
	}
	return f.corpWallet[key], nil
}

func (f *fakeESI) CorporationDivisions(_ context.Context, token string, corp int64) (esi.DivisionNames, error) {
	key := fmt.Sprintf("%d/%s", corp, strings.TrimPrefix(token, "tok-"))
	f.record("divisions/" + key)
	if err := f.divErr[key]; err != nil {
		return nil, err
	}
	return f.divisions[key], nil
}

func (f *fakeESI) CharacterJournal(_ context.Context, _ string, id int64) ([]esi.JournalEntry, error) {
	key := fmt.Sprintf("char/%d", id)
	f.record("journal/" + key)
	return f.journals[key], f.journalErr[key]
}

func (f *fakeESI) CorporationJournal(_ context.Context, _ string, corp int64, division int) ([]esi.JournalEntry, error) {
	key := fmt.Sprintf("corp/%d/%d", corp, division)
	f.record("journal/" + key)
	return f.journals[key], f.journalErr[key]
}

type snap struct {
	wallet store.Wallet
	at     time.Time
	cents  int64
}

type fakeStore struct {
	wallets   []store.Wallet
	snaps     []snap
	upsertErr error
	snapErr   error

	// journal points keyed by wallet id and entry id, like the real store.
	points     map[[2]int64]point
	journalErr error

	// esiNames holds the stored ESI name per wallet id; nameCalls counts writes.
	esiNames  map[int64]string
	nameCalls int
	nameErr   error
}

type point struct {
	at    time.Time
	cents int64
}

func (s *fakeStore) AddJournalBalance(_ context.Context, id, entry int64, at time.Time, cents int64) error {
	if s.journalErr != nil {
		return s.journalErr
	}
	if s.points == nil {
		s.points = make(map[[2]int64]point)
	}
	k := [2]int64{id, entry}
	if _, ok := s.points[k]; !ok {
		s.points[k] = point{at: at, cents: cents}
	}
	return nil
}

func (s *fakeStore) SetESIName(_ context.Context, id int64, name string) error {
	s.nameCalls++
	if s.nameErr != nil {
		return s.nameErr
	}
	if s.esiNames == nil {
		s.esiNames = make(map[int64]string)
	}
	s.esiNames[id] = name
	return nil
}

func (s *fakeStore) ClearESIName(_ context.Context, id int64) error {
	s.nameCalls++
	if s.nameErr != nil {
		return s.nameErr
	}
	delete(s.esiNames, id)
	return nil
}

func (s *fakeStore) UpsertWallet(_ context.Context, w store.Wallet) (int64, error) {
	if s.upsertErr != nil {
		return 0, s.upsertErr
	}
	for i, have := range s.wallets {
		if have == w {
			return int64(i + 1), nil
		}
	}
	s.wallets = append(s.wallets, w)
	return int64(len(s.wallets)), nil
}

func (s *fakeStore) AddSnapshot(_ context.Context, id int64, at time.Time, cents int64) error {
	if s.snapErr != nil {
		return s.snapErr
	}
	s.snaps = append(s.snaps, snap{wallet: s.wallets[id-1], at: at, cents: cents})
	return nil
}

var fixedNow = time.Date(2026, 10, 3, 12, 0, 0, 987654321, time.UTC)

func newCollector(a *fakeAuth, e *fakeESI, s *fakeStore) *Collector {
	return New(Deps{Auth: a, ESI: e, Store: s, Now: func() time.Time { return fixedNow }})
}

func forbidden() error { return &esi.APIError{Status: 403, Message: "no role"} }

func TestPersonalOnly(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{wallets: map[int64]int64{1: 123456}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 1 || s.snaps[0].cents != 123456 {
		t.Fatalf("snaps = %+v", s.snaps)
	}
	w := s.snaps[0].wallet
	if w.Kind != store.KindCharacter || w.OwnerID != 1 || w.OwnerName != "Alice" || w.Division != 0 {
		t.Fatalf("wallet = %+v", w)
	}
	if len(rep.Snapshots) != 1 || len(rep.Errors) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	// The corporation scope is missing: a recorded skip, not an error.
	if len(rep.Skipped) != 1 || rep.Skipped[0].Owner != "Alice" || !strings.Contains(rep.Skipped[0].Reason, corpScope) {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
}

func TestPersonalAndCorporation(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}}}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 100},
		corpOf:     map[int64]int64{1: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 500}, {Division: 2, Cents: 700}}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 3 || len(rep.Snapshots) != 3 || len(rep.Skipped) != 0 {
		t.Fatalf("snaps=%d report=%+v", len(s.snaps), rep)
	}
	c := s.snaps[1].wallet
	if c.Kind != store.KindCorporation || c.OwnerID != 900 || c.OwnerName != "Acme" || c.Division != 1 || s.snaps[1].cents != 500 {
		t.Fatalf("corp snap = %+v", s.snaps[1])
	}
}

func TestMissingPersonalScopeIsSkip(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: nil}}}
	e := &fakeESI{}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 0 || len(rep.Errors) != 0 || len(rep.Skipped) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if len(e.calls) != 0 || len(a.tokenCall) != 0 {
		t.Fatalf("no calls expected: esi=%v tokens=%v", e.calls, a.tokenCall)
	}
}

func TestCorporationForbiddenIsSkip(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{corpScope}}}}
	e := &fakeESI{corpOf: map[int64]int64{1: 900}, corpErr: map[string]error{"900/1": forbidden()}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 || len(rep.Skipped) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	var found bool
	for _, sk := range rep.Skipped {
		if sk.Reason == "missing corporation role" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
}

func TestTwoCharactersSameCorporationCollectedOnce(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{corpScope}},
		{ID: 2, Name: "Bob", Scopes: []string{corpScope}},
	}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 5}}, "900/2": {{Division: 1, Cents: 5}}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 1 {
		t.Fatalf("snaps = %d, want 1", len(s.snaps))
	}
	var already bool
	for _, sk := range rep.Skipped {
		if sk.Reason == "already collected" {
			already = true
		}
	}
	if !already {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	for _, c := range e.calls {
		if c == "corpwallets/900/2" {
			t.Fatal("second character must not fetch the corp wallets again")
		}
	}
}

func TestFirstForbiddenSecondSucceeds(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{corpScope}},
		{ID: 2, Name: "Bob", Scopes: []string{corpScope}},
	}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900, 2: 900},
		corpNames:  map[int64]string{900: "Acme"},
		corpErr:    map[string]error{"900/1": forbidden()},
		corpWallet: map[string][]esi.DivisionBalance{"900/2": {{Division: 3, Cents: 42}}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 1 || s.snaps[0].cents != 42 || s.snaps[0].wallet.Division != 3 {
		t.Fatalf("snaps = %+v", s.snaps)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

func TestTokenFailureDoesNotAbortOthers(t *testing.T) {
	a := &fakeAuth{
		chars: []auth.Character{
			{ID: 1, Name: "Alice", Scopes: []string{charScope}},
			{ID: 2, Name: "Bob", Scopes: []string{charScope}},
		},
		tokenErrs: map[int64]error{1: errors.New("eve-auth exited with exit status 1")},
	}
	e := &fakeESI{wallets: map[int64]int64{2: 77}}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.snaps) != 1 || s.snaps[0].wallet.OwnerID != 2 {
		t.Fatalf("snaps = %+v", s.snaps)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Owner != "Alice" {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

func TestWalletErrorRecordedAndContinues(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{wallets: map[int64]int64{2: 9}, walletErr: map[int64]error{1: &esi.APIError{Status: 500}}}
	s := &fakeStore{}
	rep, _ := newCollector(a, e, s).Run(context.Background())
	if len(s.snaps) != 1 || len(rep.Errors) != 1 {
		t.Fatalf("snaps=%d report=%+v", len(s.snaps), rep)
	}
}

func TestStoreFailureRecorded(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{charScope}}}}
	e := &fakeESI{wallets: map[int64]int64{1: 1}}
	s := &fakeStore{upsertErr: errors.New("disk full")}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || len(rep.Snapshots) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestRateLimitReturnsPartialReport(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
		{ID: 3, Name: "Cy", Scopes: []string{charScope}},
	}}
	e := &fakeESI{
		wallets:   map[int64]int64{1: 10},
		walletErr: map[int64]error{2: &esi.RateLimitError{Status: 429, RetryAfter: 30 * time.Second}},
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RateLimited || rep.RetryAfter != 30*time.Second {
		t.Fatalf("report = %+v", rep)
	}
	if len(s.snaps) != 1 {
		t.Fatalf("snaps = %d, want 1 (partial)", len(s.snaps))
	}
	for _, c := range e.calls {
		if c == "wallet/3" {
			t.Fatal("no ESI call may follow a rate limit")
		}
	}
}

func TestRateLimitOnCorporationNameKeepsData(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		nameErr:    &esi.RateLimitError{Status: 420},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 8}}},
	}
	s := &fakeStore{}
	rep, _ := newCollector(a, e, s).Run(context.Background())
	if !rep.RateLimited || len(s.snaps) != 1 || s.snaps[0].wallet.OwnerName != "Corp 900" {
		t.Fatalf("report=%+v snaps=%+v", rep, s.snaps)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{wallets: map[int64]int64{1: 1, 2: 2}}
	e.onCall = func(c string) {
		if c == "wallet/1" {
			cancel()
		}
	}
	s := &fakeStore{}
	rep, err := newCollector(a, e, s).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(e.calls) != 1 {
		t.Fatalf("calls after cancel: %v", e.calls)
	}
	_ = rep
}

func TestSharedTimestamp(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{charScope, corpScope}},
		{ID: 2, Name: "Bob", Scopes: []string{charScope}},
	}}
	e := &fakeESI{
		wallets:    map[int64]int64{1: 1, 2: 2},
		corpOf:     map[int64]int64{1: 900},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 3}}},
	}
	s := &fakeStore{}
	calls := 0
	c := New(Deps{Auth: a, ESI: e, Store: s, Now: func() time.Time {
		calls++
		return fixedNow.Add(time.Duration(calls) * time.Second)
	}})
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := fixedNow.Add(time.Second).Truncate(time.Second)
	if len(s.snaps) != 3 {
		t.Fatalf("snaps = %d", len(s.snaps))
	}
	for _, sn := range s.snaps {
		if !sn.at.Equal(want) || sn.at.Nanosecond() != 0 {
			t.Fatalf("takenAt = %v, want %v", sn.at, want)
		}
	}
}

func TestOwnerNameFallback(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{{ID: 1, Name: "Alice", Scopes: []string{corpScope}}}}
	e := &fakeESI{
		corpOf:     map[int64]int64{1: 900},
		nameErr:    &esi.APIError{Status: 500},
		corpWallet: map[string][]esi.DivisionBalance{"900/1": {{Division: 1, Cents: 1}}},
	}
	s := &fakeStore{}
	rep, _ := newCollector(a, e, s).Run(context.Background())
	if len(s.snaps) != 1 || s.snaps[0].wallet.OwnerName != "Corp 900" || rep.RateLimited {
		t.Fatalf("report=%+v snaps=%+v", rep, s.snaps)
	}
}

func TestCorporationIDFailureIsError(t *testing.T) {
	a := &fakeAuth{chars: []auth.Character{
		{ID: 1, Name: "Alice", Scopes: []string{corpScope}},
	}}
	e := &fakeESI{corpOfErr: &esi.APIError{Status: 500}}
	rep, err := newCollector(a, e, &fakeStore{}).Run(context.Background())
	if err != nil || len(rep.Errors) != 1 {
		t.Fatalf("err=%v report=%+v", err, rep)
	}
}

func TestListFailureIsFatal(t *testing.T) {
	a := &fakeAuth{listErr: errors.New("eve-auth missing")}
	if _, err := newCollector(a, &fakeESI{}, &fakeStore{}).Run(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
