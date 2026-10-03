package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const (
	oldRefresh = "refresh-OLD-secret"
	newRefresh = "refresh-NEW-secret"
	accessTok  = "access-secret-1"
)

// fakeTokenStore is an in-memory TokenStore that records the order of calls.
type fakeTokenStore struct {
	mu      sync.Mutex
	tokens  map[int64]store.Token
	listErr error
	getErr  error
	saveErr error
	events  *[]string
	saves   int
}

func newFakeTokenStore(events *[]string, toks ...store.Token) *fakeTokenStore {
	s := &fakeTokenStore{tokens: make(map[int64]store.Token), events: events}
	for _, t := range toks {
		s.tokens[t.CharacterID] = t
	}
	return s
}

func (s *fakeTokenStore) note(e string) {
	if s.events != nil {
		*s.events = append(*s.events, e)
	}
}

func (s *fakeTokenStore) Tokens(context.Context) ([]store.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []store.Token
	for id := int64(1); id < 100; id++ {
		if t, ok := s.tokens[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *fakeTokenStore) GetToken(_ context.Context, id int64) (store.Token, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return store.Token{}, false, s.getErr
	}
	t, ok := s.tokens[id]
	return t, ok, nil
}

func (s *fakeTokenStore) SaveToken(_ context.Context, t store.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note("save")
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.tokens[t.CharacterID] = t
	return nil
}

type fakeRefresher struct {
	set   sso.TokenSet
	err   error
	calls atomic.Int32
	seen  []string
	mu    sync.Mutex
	hold  chan struct{} // when set, Refresh blocks until it is closed
	event *[]string
}

func (r *fakeRefresher) Refresh(_ context.Context, refreshToken string) (sso.TokenSet, error) {
	r.calls.Add(1)
	r.mu.Lock()
	r.seen = append(r.seen, refreshToken)
	if r.event != nil {
		*r.event = append(*r.event, "refresh")
	}
	r.mu.Unlock()
	if r.hold != nil {
		<-r.hold
	}
	return r.set, r.err
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func aliceToken() store.Token {
	return store.Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: oldRefresh, Scopes: []string{"s1", "s2"}}
}

func newSource(t *testing.T, st TokenStore, rf Refresher) (*StoreTokens, *clock) {
	t.Helper()
	c := &clock{t: t0}
	return NewStoreTokens(st, rf, c.Now), c
}

func TestStoreTokensCharacters(t *testing.T) {
	st := newFakeTokenStore(nil,
		aliceToken(),
		store.Token{CharacterID: 2, UserID: 1, CharacterName: "Bob", RefreshToken: "r", Scopes: []string{"x"}},
	)
	src, _ := newSource(t, st, &fakeRefresher{})
	got, err := src.Characters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Character{
		{ID: 1, Name: "Alice", Scopes: []string{"s1", "s2"}, UserID: 1},
		{ID: 2, Name: "Bob", Scopes: []string{"x"}, UserID: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("characters = %+v, want %+v", got, want)
	}
}

func TestStoreTokensCharactersEmptyAndError(t *testing.T) {
	st := newFakeTokenStore(nil)
	src, _ := newSource(t, st, &fakeRefresher{})
	got, err := src.Characters(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty store: got %v, %v", got, err)
	}
	st.listErr = errors.New("db down")
	if _, err := src.Characters(context.Background()); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreTokensCachesUntilSixtySecondsBeforeExpiry(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: oldRefresh, ExpiresIn: 20 * time.Minute}}
	src, clk := newSource(t, st, rf)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		tok, err := src.Token(ctx, 1)
		if err != nil || tok != accessTok {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if n := rf.calls.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1 (cached)", n)
	}
	clk.Advance(20*time.Minute - 61*time.Second)
	if _, err := src.Token(ctx, 1); err != nil || rf.calls.Load() != 1 {
		t.Fatalf("61s before expiry must still be cached; calls = %d, err = %v", rf.calls.Load(), err)
	}
	clk.Advance(2 * time.Second) // now 59s before expiry
	if _, err := src.Token(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := rf.calls.Load(); n != 2 {
		t.Fatalf("refreshes = %d, want 2 (inside the 60s margin)", n)
	}
}

func TestStoreTokensRotationPersistedBeforeReturn(t *testing.T) {
	var events []string
	st := newFakeTokenStore(&events, aliceToken())
	rf := &fakeRefresher{
		set:   sso.TokenSet{AccessToken: accessTok, RefreshToken: newRefresh, ExpiresIn: 20 * time.Minute},
		event: &events,
	}
	src, _ := newSource(t, st, rf)
	tok, err := src.Token(context.Background(), 1)
	if err != nil || tok != accessTok {
		t.Fatalf("token = %q, %v", tok, err)
	}
	events = append(events, "returned")
	if !reflect.DeepEqual(events, []string{"refresh", "save", "returned"}) {
		t.Fatalf("order = %v", events)
	}
	saved := st.tokens[1]
	if saved.RefreshToken != newRefresh || saved.CharacterName != "Alice" || saved.UserID != 1 || !reflect.DeepEqual(saved.Scopes, []string{"s1", "s2"}) {
		t.Fatalf("saved = %+v", saved)
	}
	if !saved.UpdatedAt.Equal(t0) {
		t.Fatalf("UpdatedAt = %v, want the injected clock", saved.UpdatedAt)
	}
	if rf.seen[0] != oldRefresh {
		t.Fatalf("refreshed with %q", rf.seen[0])
	}
}

func TestStoreTokensSecondRefreshUsesRotatedToken(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: newRefresh, ExpiresIn: time.Minute}}
	src, clk := newSource(t, st, rf)
	ctx := context.Background()
	if _, err := src.Token(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if _, err := src.Token(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rf.seen, []string{oldRefresh, newRefresh}) {
		t.Fatalf("refresh tokens used = %v", rf.seen)
	}
}

func TestStoreTokensSaveFailureReturnsNoToken(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	st.saveErr = errors.New("disk full")
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: newRefresh, ExpiresIn: 20 * time.Minute}}
	src, _ := newSource(t, st, rf)
	tok, err := src.Token(context.Background(), 1)
	if err == nil || tok != "" {
		t.Fatalf("token = %q, err = %v; want an error and no token", tok, err)
	}
	for _, secret := range []string{oldRefresh, newRefresh, accessTok} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a token: %v", err)
		}
	}
}

// failRotationSave rotates once while saving fails, then lets saving work again.
func failRotationSave(t *testing.T, expiresIn time.Duration) (*StoreTokens, *fakeTokenStore, *fakeRefresher, *clock) {
	t.Helper()
	st := newFakeTokenStore(nil, aliceToken())
	st.saveErr = errors.New("database is locked")
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: newRefresh, ExpiresIn: expiresIn}}
	src, clk := newSource(t, st, rf)
	if tok, err := src.Token(context.Background(), 1); err == nil || tok != "" {
		t.Fatalf("token = %q, err = %v; want an error and no token", tok, err)
	}
	return src, st, rf, clk
}

func TestStoreTokensFailedSaveIsRetriedWithTheRotatedToken(t *testing.T) {
	src, st, rf, _ := failRotationSave(t, 20*time.Minute)
	st.saveErr = nil
	tok, err := src.Token(context.Background(), 1)
	if err != nil || tok != accessTok {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if st.tokens[1].RefreshToken != newRefresh {
		t.Fatalf("stored refresh token = %q, want the rotated one", st.tokens[1].RefreshToken)
	}
	if !reflect.DeepEqual(rf.seen, []string{oldRefresh}) {
		t.Fatalf("refresh tokens used = %v; the dead one must not be used again", rf.seen)
	}
}

func TestStoreTokensKeepsFailingWithoutBurningAnotherRotation(t *testing.T) {
	src, st, rf, _ := failRotationSave(t, 20*time.Minute)
	for i := 0; i < 2; i++ {
		if tok, err := src.Token(context.Background(), 1); err == nil || tok != "" {
			t.Fatalf("call %d: token = %q, err = %v", i, tok, err)
		}
	}
	if n := rf.calls.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
	if st.saves != 3 {
		t.Fatalf("save attempts = %d, want 3 (one per call)", st.saves)
	}
	st.saveErr = nil // storage recovers: the pending token is still there
	if tok, err := src.Token(context.Background(), 1); err != nil || tok != accessTok || st.tokens[1].RefreshToken != newRefresh {
		t.Fatalf("token = %q, err = %v, stored = %q", tok, err, st.tokens[1].RefreshToken)
	}
}

func TestStoreTokensRefreshesWithPendingTokenAfterFlush(t *testing.T) {
	src, st, rf, clk := failRotationSave(t, time.Minute)
	st.saveErr = nil
	clk.Advance(time.Hour) // the held access token has expired
	rf.set.RefreshToken = "refresh-3-secret"
	tok, err := src.Token(context.Background(), 1)
	if err != nil || tok != accessTok {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if !reflect.DeepEqual(rf.seen, []string{oldRefresh, newRefresh}) {
		t.Fatalf("refresh tokens used = %v, want the pending one second", rf.seen)
	}
	if st.tokens[1].RefreshToken != "refresh-3-secret" {
		t.Fatalf("stored = %q", st.tokens[1].RefreshToken)
	}
}

func TestStoreTokensUnchangedRefreshTokenIsNotSaved(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: oldRefresh, ExpiresIn: time.Minute}}
	src, _ := newSource(t, st, rf)
	if tok, err := src.Token(context.Background(), 1); err != nil || tok != accessTok {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if st.saves != 0 {
		t.Fatalf("saves = %d, want 0", st.saves)
	}
}

func TestStoreTokensEmptyRefreshTokenKeepsStoredOne(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, ExpiresIn: time.Minute}}
	src, _ := newSource(t, st, rf)
	if _, err := src.Token(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if st.saves != 0 || st.tokens[1].RefreshToken != oldRefresh {
		t.Fatalf("saves = %d, stored = %q", st.saves, st.tokens[1].RefreshToken)
	}
}

func TestStoreTokensConcurrentCallersRefreshOnce(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{
		set:  sso.TokenSet{AccessToken: accessTok, RefreshToken: newRefresh, ExpiresIn: 20 * time.Minute},
		hold: make(chan struct{}),
	}
	src, _ := newSource(t, st, rf)

	const callers = 8
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = src.Token(context.Background(), 1)
		}()
	}
	// Wait until the first caller is inside Refresh, then release it.
	for rf.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(rf.hold)
	wg.Wait()

	if n := rf.calls.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want exactly 1", n)
	}
	for i := range results {
		if errs[i] != nil || results[i] != accessTok {
			t.Fatalf("caller %d: %q, %v", i, results[i], errs[i])
		}
	}
}

func TestStoreTokensDifferentCharactersDoNotBlockEachOther(t *testing.T) {
	bob := store.Token{CharacterID: 2, UserID: 2, CharacterName: "Bob", RefreshToken: "bob-refresh"}
	st := newFakeTokenStore(nil, aliceToken(), bob)
	rf := &fakeRefresher{set: sso.TokenSet{AccessToken: accessTok, RefreshToken: oldRefresh, ExpiresIn: time.Hour}}
	src, _ := newSource(t, st, rf)
	for _, id := range []int64{1, 2} {
		if _, err := src.Token(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if n := rf.calls.Load(); n != 2 {
		t.Fatalf("refreshes = %d, want one per character", n)
	}
}

func TestStoreTokensInvalidGrantIsReauthRequired(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	ssoErr := fmt.Errorf(`sso: POST /v2/oauth/token: status 400: {"error":"invalid_grant","error_description":"refresh %s revoked"}`, oldRefresh)
	rf := &fakeRefresher{err: ssoErr}
	src, _ := newSource(t, st, rf)
	_, err := src.Token(context.Background(), 1)
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("err = %v, want ErrReauthRequired", err)
	}
	var re *ReauthError
	if !errors.As(err, &re) || re.CharacterID != 1 || re.Name != "Alice" {
		t.Fatalf("reauth error = %+v", re)
	}
	if strings.Contains(err.Error(), oldRefresh) || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error leaks SSO detail: %v", err)
	}
	if !strings.Contains(err.Error(), "Alice") {
		t.Fatalf("error should name the character: %v", err)
	}
}

func TestStoreTokensOtherRefreshFailuresAreScrubbed(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{err: fmt.Errorf("sso: POST /v2/oauth/token: status 500: echo %s and %s\n\tmore", oldRefresh, oldRefresh)}
	src, _ := newSource(t, st, rf)
	_, err := src.Token(context.Background(), 1)
	if err == nil || errors.Is(err, ErrReauthRequired) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), oldRefresh) {
		t.Fatalf("error leaks the refresh token: %v", err)
	}
	if strings.ContainsAny(err.Error(), "\n\t") {
		t.Fatalf("error is not single line: %q", err)
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error lost its useful detail: %v", err)
	}
}

func TestStoreTokensContextErrorStaysReachable(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{err: fmt.Errorf("sso: POST: %w", context.DeadlineExceeded)}
	src, _ := newSource(t, st, rf)
	if _, err := src.Token(context.Background(), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreTokensUnknownCharacter(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	rf := &fakeRefresher{}
	src, _ := newSource(t, st, rf)
	tok, err := src.Token(context.Background(), 42)
	if err == nil || tok != "" || !strings.Contains(err.Error(), "42") {
		t.Fatalf("token = %q, err = %v", tok, err)
	}
	if rf.calls.Load() != 0 {
		t.Fatal("must not refresh an unknown character")
	}
}

func TestStoreTokensGetTokenFailure(t *testing.T) {
	st := newFakeTokenStore(nil, aliceToken())
	st.getErr = errors.New("db down")
	src, _ := newSource(t, st, &fakeRefresher{})
	if _, err := src.Token(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v", err)
	}
}
