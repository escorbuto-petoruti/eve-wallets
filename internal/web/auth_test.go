package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// fakeSSO is a hand-written SSO. AuthURL is the real (pure) URL builder so the
// tests see the exact URL a browser would get.
type fakeSSO struct {
	real *sso.Client

	mu          sync.Mutex
	tokens      sso.TokenSet
	claims      sso.Claims
	exchangeErr error
	validateErr error
	codes       []string
	verifiers   []string
}

func newFakeSSO() *fakeSSO {
	return &fakeSSO{
		real:   sso.NewClient(sso.DefaultConfig()),
		tokens: sso.TokenSet{AccessToken: "access-tok", RefreshToken: "refresh-tok-1", ExpiresIn: 20 * time.Minute},
		claims: sso.Claims{CharacterID: 42, CharacterName: "Bob", Scopes: sso.WalletScopes()},
	}
}

func (f *fakeSSO) AuthURL(state, challenge string) string { return f.real.AuthURL(state, challenge) }

func (f *fakeSSO) Exchange(_ context.Context, code, verifier string) (sso.TokenSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes = append(f.codes, code)
	f.verifiers = append(f.verifiers, verifier)
	if f.exchangeErr != nil {
		return sso.TokenSet{}, f.exchangeErr
	}
	return f.tokens, nil
}

func (f *fakeSSO) Validate(_ context.Context, accessToken string) (sso.Claims, error) {
	if f.validateErr != nil {
		return sso.Claims{}, f.validateErr
	}
	if accessToken != f.tokens.AccessToken {
		return sso.Claims{}, errors.New("unexpected access token")
	}
	return f.claims, nil
}

func (f *fakeSSO) exchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.codes)
}

func request(h http.Handler, method, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "localhost"
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

type loginStart struct {
	state, challenge string
	cookie           *http.Cookie
}

func startLogin(t *testing.T, f *fixture) loginStart {
	t.Helper()
	rec := request(f.anon, http.MethodGet, "/auth/login", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d", rec.Code)
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

func callback(f *fixture, query url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	return request(f.anon, http.MethodGet, "/auth/callback?"+query.Encode(), func(r *http.Request) {
		if cookie != nil {
			r.AddCookie(cookie)
		}
	})
}

func okQuery(state string) url.Values { return url.Values{"state": {state}, "code": {"auth-code-123"}} }

func TestLoginRedirect(t *testing.T) {
	f := newFixture(t, nil, false)
	rec := request(f.anon, http.MethodGet, "/auth/login", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	assertSecurityHeaders(t, rec, "/auth/login")
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme != "https" || loc.Host != "login.eveonline.com" || loc.Path != "/v2/oauth/authorize" {
		t.Errorf("location = %s", loc.Redacted())
	}
	q := loc.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != sso.DefaultClientID ||
		q.Get("redirect_uri") != sso.DefaultRedirectURL || q.Get("code_challenge_method") != "S256" ||
		q.Get("scope") != strings.Join(sso.WalletScopes(), " ") {
		t.Errorf("query = %v", q)
	}
	state := q.Get("state")
	if len(state) < 40 || len(q.Get("code_challenge")) != 43 {
		t.Errorf("state %q or challenge %q too weak", state, q.Get("code_challenge"))
	}
	c := cookieNamed(rec, loginCookie)
	if c == nil {
		t.Fatal("no eve_login cookie")
	}
	if c.Value != state || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/auth" ||
		c.MaxAge != int(loginTTL/time.Second) || c.Secure {
		t.Errorf("eve_login cookie = %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if second := startLogin(t, f); second.state == state {
		t.Error("two logins share a state")
	}
}

func TestLoginWithoutSSOIsUnavailable(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := request(New(Deps{Store: st}), http.MethodGet, "/auth/login", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestCallbackRejects(t *testing.T) {
	tests := []struct {
		name string
		call func(f *fixture, ls loginStart) *httptest.ResponseRecorder
		want int
	}{
		{"missing state", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			return callback(f, url.Values{"code": {"c"}}, ls.cookie)
		}, http.StatusForbidden},
		{"unknown state", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			return callback(f, okQuery("never-issued"), &http.Cookie{Name: loginCookie, Value: "never-issued"})
		}, http.StatusForbidden},
		{"state without the login cookie", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			return callback(f, okQuery(ls.state), nil)
		}, http.StatusForbidden},
		{"cookie of another flow", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			other := startLogin(t, f)
			return callback(f, okQuery(ls.state), other.cookie)
		}, http.StatusForbidden},
		{"replayed state", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			if rec := callback(f, okQuery(ls.state), ls.cookie); rec.Code != http.StatusSeeOther {
				t.Fatalf("first use status = %d", rec.Code)
			}
			return callback(f, okQuery(ls.state), ls.cookie)
		}, http.StatusForbidden},
		{"expired flow", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			f.now.Add(int64(loginTTL/time.Second) + 1)
			return callback(f, okQuery(ls.state), ls.cookie)
		}, http.StatusForbidden},
		{"provider error", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			return callback(f, url.Values{"state": {ls.state}, "error": {"access_denied"},
				"error_description": {"LEAKED-DESCRIPTION"}}, ls.cookie)
		}, http.StatusBadRequest},
		{"missing code", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			return callback(f, url.Values{"state": {ls.state}}, ls.cookie)
		}, http.StatusBadRequest},
		{"exchange failure", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			f.sso.exchangeErr = errors.New("sso: POST /v2/oauth/token: status 400: SECRET-PROVIDER-BODY")
			return callback(f, okQuery(ls.state), ls.cookie)
		}, http.StatusBadGateway},
		{"invalid access token", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			f.sso.validateErr = errors.New("sso: bad signature access-tok")
			return callback(f, okQuery(ls.state), ls.cookie)
		}, http.StatusBadGateway},
		{"no refresh token", func(f *fixture, ls loginStart) *httptest.ResponseRecorder {
			f.sso.tokens.RefreshToken = ""
			return callback(f, okQuery(ls.state), ls.cookie)
		}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, nil, false)
			ls := startLogin(t, f)
			rec := tt.call(f, ls)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tt.want, rec.Body.String())
			}
			assertSecurityHeaders(t, rec, "/auth/callback")
			if c := cookieNamed(rec, sessionCookie); c != nil && c.Value != "" {
				t.Errorf("a session cookie was issued: %+v", c)
			}
			signedInBefore := tt.name == "replayed state" // its first, valid callback logged in
			if len(f.loggedIn()) != 0 && !signedInBefore {
				t.Error("OnLogin ran for a rejected callback")
			}
			body := rec.Body.String()
			for _, leak := range []string{"LEAKED", "SECRET", "access-tok", "refresh-tok", "auth-code", ls.state, "goroutine", ".go:"} {
				if strings.Contains(body, leak) || strings.Contains(rec.Header().Get("Location"), leak) {
					t.Errorf("error page leaks %q: %q", leak, body)
				}
			}
			if _, ok, err := f.st.GetToken(context.Background(), 42); err != nil || (ok && !signedInBefore) {
				t.Errorf("token stored for a rejected callback (ok=%v err=%v)", ok, err)
			}
		})
	}
}

func TestCallbackRejectsWithoutExchangingTheCode(t *testing.T) {
	f := newFixture(t, nil, false)
	ls := startLogin(t, f)
	callback(f, okQuery("never-issued"), nil)
	callback(f, okQuery(ls.state), nil)
	callback(f, url.Values{"state": {ls.state}, "error": {"access_denied"}}, ls.cookie)
	if n := f.sso.exchanges(); n != 0 {
		t.Errorf("code exchanged %d times for rejected callbacks", n)
	}
}

func TestCallbackSuccess(t *testing.T) {
	f := newFixture(t, nil, false)
	ls := startLogin(t, f)
	rec := callback(f, okQuery(ls.state), ls.cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	assertSecurityHeaders(t, rec, "/auth/callback")

	// The code was exchanged with the verifier whose challenge was sent.
	if len(f.sso.codes) != 1 || f.sso.codes[0] != "auth-code-123" || sso.Challenge(f.sso.verifiers[0]) != ls.challenge {
		t.Errorf("exchange got code %q verifier-matches-challenge=%v", f.sso.codes,
			len(f.sso.verifiers) == 1 && sso.Challenge(f.sso.verifiers[0]) == ls.challenge)
	}

	ctx := context.Background()
	tok, ok, err := f.st.GetToken(ctx, 42)
	if err != nil || !ok {
		t.Fatalf("token: ok=%v err=%v", ok, err)
	}
	if tok.RefreshToken != "refresh-tok-1" || tok.UserID != 42 || tok.CharacterName != "Bob" ||
		strings.Join(tok.Scopes, " ") != strings.Join(sso.WalletScopes(), " ") {
		t.Errorf("stored token = %+v", tok)
	}

	sc := cookieNamed(rec, sessionCookie)
	if sc == nil {
		t.Fatal("no eve_session cookie")
	}
	if !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.Secure ||
		sc.MaxAge != int(sessionTTL/time.Second) {
		t.Errorf("eve_session cookie = %+v", sc)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sc.Value)
	if err != nil || len(raw) != 32 {
		t.Errorf("session value is not 32 random bytes in base64url: %v len=%d", err, len(raw))
	}
	// Only the hash is stored.
	if u, ok, _ := f.st.SessionUser(ctx, hashSession(sc.Value), f.clock()); !ok || u.CharacterID != 42 || u.Name != "Bob" {
		t.Errorf("session by hash = %+v ok=%v", u, ok)
	}
	if _, ok, _ := f.st.SessionUser(ctx, sc.Value, f.clock()); ok {
		t.Error("the cookie value itself is a stored session id")
	}
	if _, ok, _ := f.st.SessionUser(ctx, hashSession(sc.Value), f.clock().Add(sessionTTL-time.Second)); !ok {
		t.Error("session should still be valid just before 7 days")
	}
	if _, ok, _ := f.st.SessionUser(ctx, hashSession(sc.Value), f.clock().Add(sessionTTL)); ok {
		t.Error("session should expire after 7 days")
	}

	lc := cookieNamed(rec, loginCookie)
	if lc == nil || lc.MaxAge >= 0 || lc.Path != "/auth" {
		t.Errorf("eve_login cookie not cleared: %+v", lc)
	}
	if got := f.loggedIn(); len(got) != 1 || got[0] != 42 {
		t.Errorf("OnLogin calls = %v, want [42]", got)
	}

	me := request(f.anon, http.MethodGet, "/api/me", func(r *http.Request) { r.AddCookie(sc) })
	if me.Code != http.StatusOK || strings.TrimSpace(me.Body.String()) != `{"character_id":42,"name":"Bob"}` {
		t.Errorf("/api/me = %d %s", me.Code, me.Body.String())
	}
}

func TestCallbackUpdatesAnExistingUser(t *testing.T) {
	f := newFixture(t, nil, false)
	f.addUser(t, 42, "Old Name")
	ls := startLogin(t, f)
	rec := callback(f, okQuery(ls.state), ls.cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	me := request(f.anon, http.MethodGet, "/api/me", func(r *http.Request) { r.AddCookie(cookieNamed(rec, sessionCookie)) })
	if got := strings.TrimSpace(me.Body.String()); got != `{"character_id":42,"name":"Bob"}` {
		t.Errorf("/api/me = %s, want the name refreshed from the claims", got)
	}
}

func TestCallbackWithoutOnLoginHook(t *testing.T) {
	f := newFixture(t, nil, false)
	h := New(Deps{Store: f.st, SSO: f.sso, Now: f.clock}) // OnLogin nil
	rec := request(h, http.MethodGet, "/auth/login", nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	rec = request(h, http.MethodGet, "/auth/callback?"+okQuery(loc.Query().Get("state")).Encode(), func(r *http.Request) {
		r.AddCookie(cookieNamed(rec, loginCookie))
	})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestFailedTokenSaveCreatesNoSession(t *testing.T) {
	f := newFixture(t, nil, false)
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER no_tokens BEFORE INSERT ON tokens BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	ls := startLogin(t, f)
	rec := callback(f, okQuery(ls.state), ls.cookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %q", rec.Code, rec.Body.String())
	}
	if c := cookieNamed(rec, sessionCookie); c != nil && c.Value != "" {
		t.Errorf("session cookie issued: %+v", c)
	}
	if len(f.loggedIn()) != 0 {
		t.Error("OnLogin ran after a failed token save")
	}
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = 42`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions for user 42 = %d (err %v), want 0", n, err)
	}
	if strings.Contains(rec.Body.String(), "disk full") || strings.Contains(rec.Body.String(), "refresh-tok") {
		t.Errorf("error page leaks internals: %q", rec.Body.String())
	}
}

func TestLoginPurgesExpiredSessions(t *testing.T) {
	f := newFixture(t, nil, false)
	f.newSession(t, 1, time.Hour)
	f.now.Add(int64((2 * time.Hour) / time.Second)) // expires it (and nothing else yet)
	ls := startLogin(t, f)
	callback(f, okQuery(ls.state), ls.cookie)
	n, err := f.st.PurgeExpiredSessions(context.Background(), f.clock())
	if err != nil || n != 0 {
		t.Errorf("expired sessions left after a login = %d (err %v), want 0", n, err)
	}
}

func TestAPIsRequireASession(t *testing.T) {
	f := newFixture(t, nil, true)
	expired := f.newSession(t, 1, time.Hour)
	f.now.Add(int64((2 * time.Hour) / time.Second))
	cookies := map[string]*http.Cookie{
		"no cookie":      nil,
		"garbage cookie": {Name: sessionCookie, Value: "garbage"},
		"empty cookie":   {Name: sessionCookie, Value: ""},
		"expired":        {Name: sessionCookie, Value: expired},
	}
	for name, c := range cookies {
		for _, target := range []string{"/api/wallets", "/api/series", "/api/status", "/api/me"} {
			rec := request(f.anon, http.MethodGet, target, func(r *http.Request) {
				if c != nil {
					r.AddCookie(c)
				}
			})
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", name, target, rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"not signed in"}` {
				t.Errorf("%s %s: body = %s", name, target, got)
			}
			assertSecurityHeaders(t, rec, target)
		}
	}
}

func TestPublicPathsNeedNoSession(t *testing.T) {
	f := newFixture(t, nil, false)
	for _, target := range []string{"/", "/static/app.js", "/static/style.css", "/auth/login"} {
		rec := request(f.anon, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK && rec.Code != http.StatusFound {
			t.Errorf("%s status = %d", target, rec.Code)
		}
	}
}

func TestAPIsAreScopedToTheSessionUser(t *testing.T) {
	f := newFixture(t, nil, true) // Alice owns charID and corpID
	ctx := context.Background()
	f.addUser(t, 2, "Bob")
	bobWallet, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 2, OwnerName: "Bob", Division: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.LinkWallet(ctx, 2, bobWallet); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddSnapshot(ctx, bobWallet, base, 777); err != nil {
		t.Fatal(err)
	}
	bob := &http.Cookie{Name: sessionCookie, Value: f.newSession(t, 2, time.Hour)}
	alice := &http.Cookie{Name: sessionCookie, Value: f.aliceCookie}
	get := func(c *http.Cookie, target string) *httptest.ResponseRecorder {
		return request(f.anon, http.MethodGet, target, func(r *http.Request) { r.AddCookie(c) })
	}

	type walletsResp struct {
		Wallets []struct {
			ID int64 `json:"id"`
		} `json:"wallets"`
	}
	var aw, bw walletsResp
	decode(t, get(alice, "/api/wallets"), &aw)
	decode(t, get(bob, "/api/wallets"), &bw)
	if len(aw.Wallets) != 2 {
		t.Errorf("alice sees %d wallets, want 2", len(aw.Wallets))
	}
	for _, w := range aw.Wallets {
		if w.ID == bobWallet {
			t.Error("alice sees bob's wallet in /api/wallets")
		}
	}
	if len(bw.Wallets) != 1 || bw.Wallets[0].ID != bobWallet {
		t.Errorf("bob wallets = %+v, want only %d", bw.Wallets, bobWallet)
	}

	// Without ids: only the user's own series.
	var as seriesResp
	decode(t, get(alice, "/api/series?total=1"), &as)
	for _, s := range as.Series {
		if s.WalletID == bobWallet {
			t.Error("alice sees bob's series")
		}
	}
	for _, p := range as.Total {
		if p.Cents == 777 || p.Cents == 777+2000 {
			t.Errorf("alice total includes bob's balance: %+v", as.Total)
		}
	}

	// Asking for bob's wallet id explicitly yields no data for alice.
	var forged seriesResp
	decode(t, get(alice, "/api/series?wallet_ids="+itoa(bobWallet)), &forged)
	for _, s := range forged.Series {
		if len(s.Points) != 0 {
			t.Errorf("alice read bob's points through wallet_ids: %+v", s)
		}
	}
	var mixed seriesResp
	decode(t, get(alice, "/api/series?total=1&wallet_ids="+itoa(bobWallet)+","+itoa(f.charID)), &mixed)
	for _, s := range mixed.Series {
		if s.WalletID == bobWallet && len(s.Points) != 0 {
			t.Errorf("mixed request leaks bob's points: %+v", s)
		}
	}
	for _, p := range mixed.Total {
		if p.Cents == 777 || p.Cents == 2777 {
			t.Errorf("mixed total includes bob: %+v", mixed.Total)
		}
	}

	var bs seriesResp
	decode(t, get(bob, "/api/series?wallet_ids="+itoa(f.charID)), &bs)
	for _, s := range bs.Series {
		if len(s.Points) != 0 {
			t.Errorf("bob read alice's points: %+v", s)
		}
	}
}

func TestLogout(t *testing.T) {
	post := func(f *fixture, mutate func(*http.Request)) *httptest.ResponseRecorder {
		return request(f.anon, http.MethodPost, "/auth/logout", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
			if mutate != nil {
				mutate(r)
			}
		})
	}
	stillSignedIn := func(f *fixture) bool {
		return request(f.anon, http.MethodGet, "/api/me", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}).Code == http.StatusOK
	}

	t.Run("same origin deletes the session", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := post(f, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
		}
		assertSecurityHeaders(t, rec, "/auth/logout")
		if c := cookieNamed(rec, sessionCookie); c == nil || c.MaxAge >= 0 || c.Value != "" || c.Path != "/" || !c.HttpOnly {
			t.Errorf("session cookie not expired: %+v", c)
		}
		if stillSignedIn(f) {
			t.Error("session survives logout")
		}
	})
	t.Run("referer fallback", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := post(f, func(r *http.Request) { r.Header.Set("Referer", "http://localhost/some/page") })
		if rec.Code != http.StatusSeeOther || stillSignedIn(f) {
			t.Errorf("status = %d, signed in = %v", rec.Code, stillSignedIn(f))
		}
	})
	for name, mutate := range map[string]func(*http.Request){
		"no origin or referer": nil,
		"foreign origin":       func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"null origin":          func(r *http.Request) { r.Header.Set("Origin", "null") },
		"other port":           func(r *http.Request) { r.Header.Set("Origin", "http://localhost:9999") },
		"foreign referer":      func(r *http.Request) { r.Header.Set("Referer", "http://evil.example/x") },
		"origin wins over refer": func(r *http.Request) {
			r.Header.Set("Origin", "http://evil.example")
			r.Header.Set("Referer", "http://localhost/")
		},
		"malformed origin": func(r *http.Request) { r.Header.Set("Origin", "http://[::1") },
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			f := newFixture(t, nil, false)
			rec := post(f, mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			assertSecurityHeaders(t, rec, "/auth/logout")
			if !stillSignedIn(f) {
				t.Error("a rejected logout deleted the session")
			}
		})
	}
	t.Run("GET is not allowed", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := request(f.anon, http.MethodGet, "/auth/logout", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
			r.Header.Set("Origin", "http://localhost")
		})
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
		assertSecurityHeaders(t, rec, "/auth/logout")
		if !stillSignedIn(f) {
			t.Error("GET /auth/logout ended the session")
		}
	})
	t.Run("without a session it still redirects", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := request(f.anon, http.MethodPost, "/auth/logout", func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestLogoutFetchMetadata(t *testing.T) {
	post := func(f *fixture, mutate func(*http.Request)) *httptest.ResponseRecorder {
		return request(f.anon, http.MethodPost, "/auth/logout", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
			if mutate != nil {
				mutate(r)
			}
		})
	}
	stillSignedIn := func(f *fixture) bool {
		return request(f.anon, http.MethodGet, "/api/me", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}).Code == http.StatusOK
	}

	t.Run("accepts null origin when fetch metadata says same-origin", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := post(f, func(r *http.Request) {
			r.Header.Set("Origin", "null")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
		})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
			t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
		}
		assertSecurityHeaders(t, rec, "/auth/logout")
		if c := cookieNamed(rec, sessionCookie); c == nil || c.MaxAge >= 0 || c.Value != "" || c.Path != "/" || !c.HttpOnly {
			t.Errorf("session cookie not expired: %+v", c)
		}
		if stillSignedIn(f) {
			t.Error("session survives logout")
		}
	})
	t.Run("accepts a missing origin when fetch metadata says same-origin", func(t *testing.T) {
		f := newFixture(t, nil, false)
		rec := post(f, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
		if rec.Code != http.StatusSeeOther || stillSignedIn(f) {
			t.Errorf("status = %d, signed in = %v", rec.Code, stillSignedIn(f))
		}
	})
	for name, site := range map[string]string{
		"cross-site": "cross-site",
		"none":       "none",
		"same-site":  "same-site",
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			f := newFixture(t, nil, false)
			rec := post(f, func(r *http.Request) {
				r.Header.Set("Origin", "http://localhost") // matching origin does not help
				r.Header.Set("Sec-Fetch-Site", site)
			})
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			assertSecurityHeaders(t, rec, "/auth/logout")
			if !stillSignedIn(f) {
				t.Error("a rejected logout deleted the session")
			}
		})
	}
}

func TestOtherPostsStayRejected(t *testing.T) {
	f := newFixture(t, nil, true)
	for _, target := range []string{"/auth/login", "/auth/callback", "/api/me", "/api/wallets", "/"} {
		rec := request(f.h, http.MethodPost, target, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want 405", target, rec.Code)
		}
	}
}

func TestHostCheck(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	withPort := New(Deps{Store: st, SSO: newFakeSSO(), AllowedPort: "8088"})
	noPort := New(Deps{Store: st, SSO: newFakeSSO()})
	tests := []struct {
		host string
		h    http.Handler
		ok   bool
	}{
		{"localhost", withPort, true}, {"127.0.0.1", withPort, true}, {"[::1]", withPort, true},
		{"localhost:8088", withPort, true}, {"127.0.0.1:8088", withPort, true}, {"[::1]:8088", withPort, true},
		{"LOCALHOST:8088", withPort, true},
		{"localhost:9999", withPort, false}, {"127.0.0.1:80", withPort, false},
		{"example.com", withPort, false}, {"example.com:8088", withPort, false},
		{"localhost.evil.com", withPort, false}, {"evil.com#localhost", withPort, false},
		{"127.0.0.2", withPort, false}, {"0.0.0.0:8088", withPort, false}, {"", withPort, false},
		{"localhost", noPort, true}, {"localhost:8088", noPort, false},
	}
	for _, tt := range tests {
		for _, target := range []string{"/", "/api/me", "/auth/login", "/static/app.js"} {
			rec := request(tt.h, http.MethodGet, target, func(r *http.Request) { r.Host = tt.host })
			if tt.ok && rec.Code == http.StatusForbidden {
				t.Errorf("Host %q %s: rejected", tt.host, target)
			}
			if !tt.ok {
				if rec.Code != http.StatusForbidden {
					t.Errorf("Host %q %s: status = %d, want 403", tt.host, target, rec.Code)
				}
				assertSecurityHeaders(t, rec, target)
			}
		}
	}
}

func TestSecurityHeadersOnAuthResponses(t *testing.T) {
	f := newFixture(t, nil, false)
	ls := startLogin(t, f)
	for target, rec := range map[string]*httptest.ResponseRecorder{
		"/auth/login":            request(f.anon, http.MethodGet, "/auth/login", nil),
		"/auth/callback (bad)":   callback(f, url.Values{"state": {"x"}}, nil),
		"/auth/callback (good)":  callback(f, okQuery(ls.state), ls.cookie),
		"/auth/logout (foreign)": request(f.anon, http.MethodPost, "/auth/logout", nil),
		"/api/me (401)":          request(f.anon, http.MethodGet, "/api/me", nil),
	} {
		assertSecurityHeaders(t, rec, target)
	}
}

func TestLoginFlowsAreSingleUseExpiringAndBounded(t *testing.T) {
	now := base
	flows := newLoginFlows(func() time.Time { return now })
	flows.add("a", "verifier-a")
	if v, ok := flows.take("a"); !ok || v != "verifier-a" {
		t.Fatalf("take = %q %v", v, ok)
	}
	if _, ok := flows.take("a"); ok {
		t.Error("state usable twice")
	}
	flows.add("old", "v")
	now = now.Add(loginTTL)
	if _, ok := flows.take("old"); ok {
		t.Error("expired state accepted")
	}
	for i := 0; i < maxLoginFlows*3; i++ {
		now = now.Add(time.Millisecond)
		flows.add("s"+itoa(int64(i)), "v")
	}
	if n := flows.size(); n > maxLoginFlows {
		t.Errorf("flows = %d, want at most %d", n, maxLoginFlows)
	}
	if _, ok := flows.take("s0"); ok {
		t.Error("oldest flow should have been evicted")
	}
	if _, ok := flows.take("s" + itoa(int64(maxLoginFlows*3-1))); !ok {
		t.Error("newest flow should be kept")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestSameOriginFetchMetadata(t *testing.T) {
	s := &server{deps: Deps{AllowedPort: "8088"}}
	for _, tc := range []struct {
		host, site, origin string
		want               bool
	}{
		{"localhost:8088", "same-origin", "", true},
		{"evil.example:8088", "same-origin", "", false},
		{"localhost:9999", "same-origin", "", false},
		{"localhost:8088", "cross-site", "", false},
		{"localhost:8088", "", "http://localhost:8088", true},
	} {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		req.Host = tc.host
		if tc.site != "" {
			req.Header.Set("Sec-Fetch-Site", tc.site)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := s.sameOrigin(req); got != tc.want {
			t.Errorf("sameOrigin(host %q, site %q, origin %q) = %v, want %v", tc.host, tc.site, tc.origin, got, tc.want)
		}
	}
}
