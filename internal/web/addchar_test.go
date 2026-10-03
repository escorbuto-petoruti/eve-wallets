package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// sameSite marks a request as a same-origin browser navigation.
func sameSite(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }

func withSession(value string) func(*http.Request) {
	return func(r *http.Request) {
		sameSite(r)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	}
}

// startAdd starts the add-character flow as the holder of the session cookie.
func startAdd(t *testing.T, f *fixture, session string) loginStart {
	t.Helper()
	rec := request(f.anon, http.MethodGet, "/auth/add-character", withSession(session))
	if rec.Code != http.StatusFound {
		t.Fatalf("add-character status = %d, body %q", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	c := cookieNamed(rec, loginCookie)
	if c == nil {
		t.Fatal("no eve_login cookie")
	}
	return loginStart{state: loc.Query().Get("state"), challenge: loc.Query().Get("code_challenge"), cookie: c}
}

// addCallbackRec returns from SSO carrying the flow cookie and the session cookie.
func addCallbackRec(f *fixture, ls loginStart, session string) *httptest.ResponseRecorder {
	return request(f.anon, http.MethodGet, "/auth/callback?"+okQuery(ls.state).Encode(), func(r *http.Request) {
		r.AddCookie(ls.cookie)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
	})
}

func countRows(t *testing.T, f *fixture, query string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddCharacterAttachesToTheSessionUser(t *testing.T) {
	f := newFixture(t, nil, false)
	ls := startAdd(t, f, f.aliceCookie)
	sessionsBefore := countRows(t, f, `SELECT COUNT(*) FROM sessions`)

	rec := addCallbackRec(f, ls, f.aliceCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := cookieNamed(rec, sessionCookie); c != nil {
		t.Errorf("an add must keep the existing session, got a new cookie %+v", c)
	}
	tok, ok, err := f.st.GetToken(context.Background(), 42)
	if err != nil || !ok {
		t.Fatalf("token: ok=%v err=%v", ok, err)
	}
	if tok.UserID != 1 || tok.RefreshToken != "refresh-tok-1" || tok.CharacterName != "Bob" ||
		strings.Join(tok.Scopes, " ") != strings.Join(sso.WalletScopes(), " ") {
		t.Errorf("stored token = %+v, want it attached to user 1", tok)
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM users WHERE character_id = 42`); n != 0 {
		t.Errorf("users rows for the added character = %d, want 0", n)
	}
	if n := countRows(t, f, `SELECT COUNT(*) FROM sessions`); n != sessionsBefore {
		t.Errorf("sessions = %d, want %d (no new session)", n, sessionsBefore)
	}
	if got := f.loggedIn(); len(got) != 1 || got[0] != 42 {
		t.Errorf("OnLogin calls = %v, want [42]", got)
	}
}

func TestAddCharacterAlreadyOwnedRefreshesTheToken(t *testing.T) {
	f := newFixture(t, nil, false)
	err := f.st.SaveToken(context.Background(), store.Token{
		CharacterID: 42, UserID: 1, CharacterName: "Old Bob", RefreshToken: "old-refresh", Scopes: []string{"old.scope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ls := startAdd(t, f, f.aliceCookie)
	rec := addCallbackRec(f, ls, f.aliceCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	tok, _, _ := f.st.GetToken(context.Background(), 42)
	if tok.UserID != 1 || tok.RefreshToken != "refresh-tok-1" || tok.CharacterName != "Bob" {
		t.Errorf("stored token = %+v, want refreshed and still user 1", tok)
	}
	if got := f.loggedIn(); len(got) != 1 || got[0] != 42 {
		t.Errorf("OnLogin calls = %v, want [42]", got)
	}
}

func TestAddCharacterRefusesADifferentSessionUser(t *testing.T) {
	f := newFixture(t, nil, false)
	f.addUser(t, 2, "Carol")
	carol := f.newSession(t, 2, time.Hour)

	for name, session := range map[string]string{"another user": carol, "no session": "", "unknown session": "nope"} {
		t.Run(name, func(t *testing.T) {
			ls := startAdd(t, f, f.aliceCookie) // flows are single use
			rec := addCallbackRec(f, ls, session)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if _, ok, _ := f.st.GetToken(context.Background(), 42); ok {
				t.Error("a token was written")
			}
			if len(f.loggedIn()) != 0 {
				t.Error("OnLogin ran")
			}
		})
	}
}

func TestAddCharacterRequiresASessionAndSameOrigin(t *testing.T) {
	f := newFixture(t, nil, false)
	tests := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"signed out", sameSite, http.StatusUnauthorized},
		{"unknown session", withSession("nope"), http.StatusUnauthorized},
		{"cross-site", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}, http.StatusForbidden},
		{"no fetch metadata and no origin", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}, http.StatusForbidden},
		{"foreign origin", func(r *http.Request) {
			r.Header.Set("Origin", "http://evil.example")
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(f.anon, http.MethodGet, "/auth/add-character", tt.mutate)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if c := cookieNamed(rec, loginCookie); c != nil && c.Value != "" {
				t.Errorf("a flow was started: %+v", c)
			}
			if rec.Header().Get("Location") != "" {
				t.Errorf("redirected to %q", rec.Header().Get("Location"))
			}
		})
	}
}

func TestAddCharacterOfAnotherUsersCharacterWritesNothing(t *testing.T) {
	f := newFixture(t, nil, false)
	attachCharacter(t, f) // character 42 belongs to user 7
	ls := startAdd(t, f, f.aliceCookie)

	rec := addCallbackRec(f, ls, f.aliceCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/auth/confirm-move" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	mc := cookieNamed(rec, moveCookie)
	if mc == nil || mc.Value == "" {
		t.Fatal("no eve_move cookie")
	}
	if !mc.HttpOnly || mc.SameSite != http.SameSiteLaxMode || mc.Path != "/auth" || mc.MaxAge != int(moveTTL/time.Second) {
		t.Errorf("eve_move cookie = %+v", mc)
	}
	tok, _, _ := f.st.GetToken(context.Background(), 42)
	if tok.UserID != 7 || tok.RefreshToken != "old-refresh" || tok.CharacterName != "Old Bob" {
		t.Errorf("stored token = %+v, want it untouched", tok)
	}
	if len(f.loggedIn()) != 0 {
		t.Error("OnLogin ran before the move was confirmed")
	}
	if strings.Contains(rec.Body.String(), "refresh-tok-1") {
		t.Error("the refresh token reached the page")
	}
}

func TestFailedTokenSaveOnAddTriggersNothing(t *testing.T) {
	f := newFixture(t, nil, false)
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER no_token_inserts BEFORE INSERT ON tokens BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	ls := startAdd(t, f, f.aliceCookie)
	rec := addCallbackRec(f, ls, f.aliceCookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(f.loggedIn()) != 0 {
		t.Error("OnLogin ran after a failed save")
	}
}

func TestPendingMovesAreSingleUseExpiringBoundedAndTargeted(t *testing.T) {
	now := base
	moves := newPendingMoves(func() time.Time { return now })
	mk := func(target int64) pendingMove {
		return pendingMove{characterID: 42, name: "Bob", refreshToken: "r", scopes: []string{"s"}, targetUser: target}
	}

	id, err := moves.add(mk(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := moves.take(id, 2); ok {
		t.Error("a move was taken by a user it was not made for")
	}
	if _, ok := moves.peek(id, 1); !ok {
		t.Error("peek by the target user missed, or the foreign take consumed it")
	}
	if pm, ok := moves.take(id, 1); !ok || pm.characterID != 42 {
		t.Fatalf("take = %+v %v", pm, ok)
	}
	if _, ok := moves.take(id, 1); ok {
		t.Error("move usable twice")
	}

	old, _ := moves.add(mk(1))
	now = now.Add(moveTTL)
	if _, ok := moves.take(old, 1); ok {
		t.Error("expired move accepted")
	}

	var first, last string
	for i := 0; i < maxPendingMoves*3; i++ {
		now = now.Add(time.Millisecond)
		id, _ := moves.add(mk(1))
		if i == 0 {
			first = id
		}
		last = id
	}
	if n := moves.size(); n > maxPendingMoves {
		t.Errorf("moves = %d, want at most %d", n, maxPendingMoves)
	}
	if _, ok := moves.take(first, 1); ok {
		t.Error("oldest move should have been evicted")
	}
	if _, ok := moves.take(last, 1); !ok {
		t.Error("newest move should be kept")
	}
}

func TestPendingMoveNeverFormatsItsSecrets(t *testing.T) {
	pm := pendingMove{characterID: 42, name: "Bob", refreshToken: "super-secret", targetUser: 1}
	for _, got := range []string{pm.String(), fmt.Sprintf("%v %+v %#v", pm, pm, pm)} {
		if strings.Contains(got, "super-secret") {
			t.Errorf("formatted move leaks the refresh token: %q", got)
		}
	}
}
