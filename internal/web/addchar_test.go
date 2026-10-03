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

// pendingFor leaves character 42 (owned by user 7) waiting for Alice's
// confirmation and returns the eve_move cookie value.
func pendingFor(t *testing.T, f *fixture) string {
	t.Helper()
	attachCharacter(t, f)
	ls := startAdd(t, f, f.aliceCookie)
	rec := addCallbackRec(f, ls, f.aliceCookie)
	mc := cookieNamed(rec, moveCookie)
	if rec.Code != http.StatusSeeOther || mc == nil {
		t.Fatalf("callback status = %d, move cookie = %v", rec.Code, mc)
	}
	return mc.Value
}

func moveRequest(f *fixture, method, target, session, move string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	return request(f.anon, method, target, func(r *http.Request) {
		sameSite(r)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		if move != "" {
			r.AddCookie(&http.Cookie{Name: moveCookie, Value: move})
		}
		for _, m := range mutate {
			m(r)
		}
	})
}

// assertUntouched checks character 42 still belongs to user 7 with its old token.
func assertUntouched(t *testing.T, f *fixture) {
	t.Helper()
	tok, ok, err := f.st.GetToken(context.Background(), 42)
	if err != nil || !ok || tok.UserID != 7 || tok.RefreshToken != "old-refresh" {
		t.Errorf("token = %+v ok=%v err=%v, want it untouched under user 7", tok, ok, err)
	}
	if len(f.loggedIn()) != 0 {
		t.Error("OnLogin ran")
	}
}

func TestConfirmMovePageIsEscapedAndAnonymous(t *testing.T) {
	f := newFixture(t, nil, false)
	f.sso.claims.CharacterName = `Bob <script>alert("x")</script> & co`
	move := pendingFor(t, f)

	rec := moveRequest(f, http.MethodGet, "/auth/confirm-move", f.aliceCookie, move)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache control = %q", rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "Bob &lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; co") {
		t.Errorf("name not escaped: %s", body)
	}
	for _, want := range []string{
		`method="post" action="/auth/move-character"`, `method="post" action="/auth/cancel-move"`,
		"belongs to another user", "other characters",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	for _, leak := range []string{"Owner", "refresh", "old-refresh"} {
		if strings.Contains(body, leak) {
			t.Errorf("page reveals %q", leak)
		}
	}
}

func TestConfirmMoveWithoutAValidPendingMove(t *testing.T) {
	f := newFixture(t, nil, false)
	move := pendingFor(t, f)
	f.addUser(t, 2, "Carol")
	carol := f.newSession(t, 2, time.Hour)

	tests := []struct {
		name            string
		session, cookie string
		advance         time.Duration
	}{
		{"no session", "", move, 0},
		{"no cookie", f.aliceCookie, "", 0},
		{"unknown id", f.aliceCookie, "nope", 0},
		{"other session user", carol, move, 0},
		{"expired", f.aliceCookie, move, moveTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer f.now.Store(f.now.Load())
			f.now.Add(int64(tt.advance / time.Second))
			rec := moveRequest(f, http.MethodGet, "/auth/confirm-move", tt.session, tt.cookie)
			if tt.session == "" {
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", rec.Code)
				}
				return
			}
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "This confirmation expired. Add the character again.") {
				t.Errorf("status = %d, body %q", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "Bob") {
				t.Error("the character name reached a foreign page")
			}
		})
	}
}

func TestMoveCharacterMovesTheToken(t *testing.T) {
	tests := []struct {
		name         string
		ownerKeeps   bool // the previous user has another character
		wantOwnerRow int
	}{
		{"previous user left empty is deleted", false, 0},
		{"previous user with other characters is kept", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil, false)
			ctx := context.Background()
			move := pendingFor(t, f)
			if tt.ownerKeeps {
				err := f.st.SaveToken(ctx, store.Token{CharacterID: 43, UserID: 7, CharacterName: "Other", RefreshToken: "r43"})
				if err != nil {
					t.Fatal(err)
				}
			}
			wid, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 42, OwnerName: "Bob", Division: 0})
			if err != nil {
				t.Fatal(err)
			}
			f.link(t, 7, wid)

			rec := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
				t.Fatalf("status = %d, location = %q, body %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
			tok, ok, err := f.st.GetToken(ctx, 42)
			if err != nil || !ok || tok.UserID != 1 || tok.RefreshToken != "refresh-tok-1" || tok.CharacterName != "Bob" ||
				strings.Join(tok.Scopes, " ") != strings.Join(sso.WalletScopes(), " ") {
				t.Errorf("token = %+v ok=%v err=%v, want user 1 with the new secrets", tok, ok, err)
			}
			if n := countRows(t, f, `SELECT COUNT(*) FROM users WHERE character_id = 7`); n != tt.wantOwnerRow {
				t.Errorf("previous user rows = %d, want %d", n, tt.wantOwnerRow)
			}
			if chars, _ := f.st.CharactersForUser(ctx, 7); tt.ownerKeeps && (len(chars) != 1 || chars[0].CharacterID != 43) {
				t.Errorf("previous user characters = %+v, want only 43", chars)
			}
			ws, err := f.st.WalletsForUser(ctx, 1)
			if err != nil || len(ws) != 1 || ws[0].ID != wid {
				t.Errorf("wallets of the new user = %+v err=%v, want the personal wallet", ws, err)
			}
			if got := f.loggedIn(); len(got) != 1 || got[0] != 42 {
				t.Errorf("OnLogin calls = %v, want [42]", got)
			}
			if c := cookieNamed(rec, moveCookie); c == nil || c.MaxAge >= 0 || c.Path != "/auth" {
				t.Errorf("eve_move cookie not cleared: %+v", c)
			}

			again := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move)
			if again.Code != http.StatusBadRequest {
				t.Errorf("second move status = %d, want 400", again.Code)
			}
			if len(f.loggedIn()) != 1 {
				t.Error("the second move triggered a collection")
			}
		})
	}
}

func TestCancelMoveWritesNothingAndConsumesTheMove(t *testing.T) {
	f := newFixture(t, nil, false)
	move := pendingFor(t, f)
	rec := moveRequest(f, http.MethodPost, "/auth/cancel-move", f.aliceCookie, move)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := cookieNamed(rec, moveCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("eve_move cookie not cleared: %+v", c)
	}
	assertUntouched(t, f)
	if rec := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move); rec.Code != http.StatusBadRequest {
		t.Errorf("move after cancel status = %d, want 400", rec.Code)
	}
	assertUntouched(t, f)
}

func TestMoveCharacterWithoutAValidPendingMoveWritesNothing(t *testing.T) {
	f := newFixture(t, nil, false)
	move := pendingFor(t, f)
	f.addUser(t, 2, "Carol")
	carol := f.newSession(t, 2, time.Hour)

	tests := []struct {
		name            string
		session, cookie string
		advance         time.Duration
		want            int
	}{
		{"no session", "", move, 0, http.StatusUnauthorized},
		{"no cookie", f.aliceCookie, "", 0, http.StatusBadRequest},
		{"other session user", carol, move, 0, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer f.now.Store(f.now.Load())
			f.now.Add(int64(tt.advance / time.Second))
			rec := moveRequest(f, http.MethodPost, "/auth/move-character", tt.session, tt.cookie)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			assertUntouched(t, f)
		})
	}
	// The foreign attempt did not consume Alice's move.
	if rec := moveRequest(f, http.MethodGet, "/auth/confirm-move", f.aliceCookie, move); rec.Code != http.StatusOK {
		t.Errorf("Alice's move was consumed by another user: status %d", rec.Code)
	}

	t.Run("expired", func(t *testing.T) {
		f.now.Add(int64(moveTTL / time.Second))
		rec := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		assertUntouched(t, f)
	})
}

func TestMoveEndpointsRefuseCrossSitePosts(t *testing.T) {
	f := newFixture(t, nil, false)
	move := pendingFor(t, f)
	for _, target := range []string{"/auth/move-character", "/auth/cancel-move"} {
		for name, mutate := range map[string]func(*http.Request){
			"cross-site":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			"no evidence": func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") },
		} {
			rec := moveRequest(f, http.MethodPost, target, f.aliceCookie, move, mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s status = %d, want 403", target, name, rec.Code)
			}
			assertUntouched(t, f)
		}
	}
	// A refused request does not burn the pending move.
	if rec := moveRequest(f, http.MethodGet, "/auth/confirm-move", f.aliceCookie, move); rec.Code != http.StatusOK {
		t.Errorf("pending move lost after refused posts: status %d", rec.Code)
	}
}

func TestGuardAllowsPostOnlyOnTheMoveEndpoints(t *testing.T) {
	f := newFixture(t, nil, true)
	for _, target := range []string{"/auth/add-character", "/auth/confirm-move", "/auth/move-character/x", "/auth/cancel-move/x"} {
		rec := request(f.h, http.MethodPost, target, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want 405", target, rec.Code)
		}
	}
	for _, target := range []string{"/auth/move-character", "/auth/cancel-move"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := request(f.h, method, target, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
			if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
				t.Errorf("%s %s status = %d, want 405", method, target, rec.Code)
			}
		}
	}
}

func TestMoveFailuresAreNeverSilent(t *testing.T) {
	trigger := func(t *testing.T, f *fixture, stmt string) {
		t.Helper()
		raw, err := sql.Open("sqlite", f.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("move fails, nothing is saved", func(t *testing.T) {
		f := newFixture(t, nil, false)
		move := pendingFor(t, f)
		trigger(t, f, `CREATE TRIGGER no_moves BEFORE UPDATE ON tokens BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
		rec := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		assertUntouched(t, f)
	})

	t.Run("save fails after the move", func(t *testing.T) {
		f := newFixture(t, nil, false)
		move := pendingFor(t, f)
		// The move only changes user_id; the save is the one that rewrites the secret.
		trigger(t, f, `CREATE TRIGGER no_secret_updates BEFORE UPDATE ON tokens WHEN NEW.refresh_token <> OLD.refresh_token `+
			`BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
		rec := moveRequest(f, http.MethodPost, "/auth/move-character", f.aliceCookie, move)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "moved") ||
			!strings.Contains(rec.Body.String(), "previous token") {
			t.Fatalf("status = %d, body %q, want an error saying it moved but did not refresh", rec.Code, rec.Body.String())
		}
		tok, _, _ := f.st.GetToken(context.Background(), 42)
		if tok.UserID != 1 || tok.RefreshToken != "old-refresh" {
			t.Errorf("token = %+v, want moved to user 1 with the previous secret", tok)
		}
		if len(f.loggedIn()) != 0 {
			t.Error("OnLogin ran after a failed save")
		}
		if c := cookieNamed(rec, moveCookie); c == nil || c.MaxAge >= 0 {
			t.Errorf("eve_move cookie not cleared: %+v", c)
		}
	})
}

// seedingSSO makes a foreign owner appear for the character while the callback
// is validating it: after the session was resolved and before any save.
type seedingSSO struct {
	*fakeSSO
	seed func()
}

func (s seedingSSO) Validate(ctx context.Context, accessToken string) (sso.Claims, error) {
	if s.seed != nil {
		s.seed()
	}
	return s.fakeSSO.Validate(ctx, accessToken)
}

func TestAddCharacterLosingARaceEndsInAPendingMove(t *testing.T) {
	f := newFixture(t, nil, false)
	h := New(Deps{Store: f.st, SSO: seedingSSO{f.sso, func() { attachCharacter(t, f) }}, Now: f.clock,
		OnLogin: func(id int64) { f.mu.Lock(); f.logins = append(f.logins, id); f.mu.Unlock() }})
	f.anon = h
	ls := startAdd(t, f, f.aliceCookie)

	rec := addCallbackRec(f, ls, f.aliceCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/auth/confirm-move" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if cookieNamed(rec, moveCookie) == nil {
		t.Error("no eve_move cookie")
	}
	assertUntouched(t, f)
}
