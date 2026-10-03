package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const (
	sessionCookie = "eve_session"
	loginCookie   = "eve_login"

	sessionTTL = 7 * 24 * time.Hour
	loginTTL   = 10 * time.Minute

	// maxLoginFlows bounds the in-memory map of pending sign-ins.
	maxLoginFlows = 64
)

// SSO is the part of EVE SSO the sign-in flow needs; *sso.Client implements it.
type SSO interface {
	AuthURL(state, challenge string) string
	Exchange(ctx context.Context, code, verifier string) (sso.TokenSet, error)
	Validate(ctx context.Context, accessToken string) (sso.Claims, error)
}

type userKey struct{}

// hashSession is the stored form of a session cookie value: the database never
// holds a value that could be replayed as a cookie.
func hashSession(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// loginFlows holds the PKCE verifier of every sign-in that has not come back
// yet. Entries are single use, expire after loginTTL and the map is bounded.
type loginFlows struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]loginFlow
}

type loginFlow struct {
	verifier string
	expires  time.Time
}

func newLoginFlows(now func() time.Time) *loginFlows {
	return &loginFlows{now: now, m: make(map[string]loginFlow)}
}

func (l *loginFlows) add(state, verifier string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, f := range l.m {
		if !now.Before(f.expires) {
			delete(l.m, k)
		}
	}
	for len(l.m) >= maxLoginFlows { // evict the flow closest to expiring
		var oldest string
		var at time.Time
		for k, f := range l.m {
			if oldest == "" || f.expires.Before(at) {
				oldest, at = k, f.expires
			}
		}
		delete(l.m, oldest)
	}
	l.m[state] = loginFlow{verifier: verifier, expires: now.Add(loginTTL)}
}

// take consumes the flow of state: a second call always misses.
func (l *loginFlows) take(state string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.m[state]
	delete(l.m, state)
	if !ok || !l.now().Before(f.expires) {
		return "", false
	}
	return f.verifier, true
}

func (l *loginFlows) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if s.deps.SSO == nil {
		errorPage(w, http.StatusServiceUnavailable, "Sign-in is not available.")
		return
	}
	state, err1 := sso.NewState()
	verifier, err2 := sso.NewVerifier()
	if err1 != nil || err2 != nil {
		errorPage(w, http.StatusInternalServerError, "Could not start the sign-in.")
		return
	}
	s.flows.add(state, verifier)
	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: state, Path: "/auth", MaxAge: int(loginTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.deps.SSO.AuthURL(state, sso.Challenge(verifier)), http.StatusFound)
}

// callback finishes a sign-in. Messages are fixed strings: nothing from the
// provider, the request or an internal error reaches the page or the logs.
func (s *server) callback(w http.ResponseWriter, r *http.Request) {
	if s.deps.SSO == nil {
		errorPage(w, http.StatusServiceUnavailable, "Sign-in is not available.")
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	verifier, known := s.flows.take(state) // single use, whatever happens next
	cookie, err := r.Cookie(loginCookie)
	bound := err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) == 1
	clearCookie(w, loginCookie, "/auth")
	if state == "" || !known || !bound {
		errorPage(w, http.StatusForbidden, "This sign-in link is not valid or has expired. Start again.")
		return
	}
	if q.Get("error") != "" {
		errorPage(w, http.StatusBadRequest, "EVE SSO did not approve the sign-in.")
		return
	}
	code := q.Get("code")
	if code == "" {
		errorPage(w, http.StatusBadRequest, "The sign-in response is incomplete.")
		return
	}

	ctx := r.Context()
	tokens, err := s.deps.SSO.Exchange(ctx, code, verifier)
	if err != nil || tokens.RefreshToken == "" {
		errorPage(w, http.StatusBadGateway, "EVE SSO could not complete the sign-in. Try again.")
		return
	}
	claims, err := s.deps.SSO.Validate(ctx, tokens.AccessToken)
	if err != nil || claims.CharacterID <= 0 {
		errorPage(w, http.StatusBadGateway, "EVE SSO could not complete the sign-in. Try again.")
		return
	}

	now := s.now()
	// The refresh token is stored before the session exists: a session without
	// a stored token would show an empty page that never fills.
	if err := s.deps.Store.UpsertUser(ctx, claims.CharacterID, claims.CharacterName, now); err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not save the sign-in.")
		return
	}
	err = s.deps.Store.SaveToken(ctx, store.Token{
		CharacterID: claims.CharacterID, UserID: claims.CharacterID, CharacterName: claims.CharacterName,
		RefreshToken: tokens.RefreshToken, Scopes: claims.Scopes, UpdatedAt: now,
	})
	if err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not save the sign-in.")
		return
	}
	_, _ = s.deps.Store.PurgeExpiredSessions(ctx, now) // best effort housekeeping
	value, err := randomValue()
	if err == nil {
		err = s.deps.Store.CreateSession(ctx, hashSession(value), claims.CharacterID, now, now.Add(sessionTTL))
	}
	if err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not create the session.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: int(sessionTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, // no Secure: plain http on localhost
	})
	if s.deps.OnLogin != nil {
		s.deps.OnLogin(claims.CharacterID)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		errorPage(w, http.StatusForbidden, "Cross-site sign-out refused.")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.deps.Store.DeleteSession(r.Context(), hashSession(c.Value))
	}
	clearCookie(w, sessionCookie, "/")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) me(w http.ResponseWriter, r *http.Request, u store.User) {
	writeJSON(w, r, http.StatusOK, map[string]any{"character_id": u.CharacterID, "name": u.Name})
}

func clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// sameOrigin accepts a request whose Origin (or, without one, Referer) names
// the host the request was sent to. A request with neither is refused.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	u, err := url.Parse(src)
	if src == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// withSession resolves the session cookie, when there is one, to a user and
// puts it in the request context. Unknown or expired cookies are ignored: the
// protected handlers answer 401.
func (s *server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			u, ok, err := s.deps.Store.SessionUser(r.Context(), hashSession(c.Value), s.now())
			if err != nil {
				serverError(w, err)
				return
			}
			if ok {
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser wraps a handler that needs the signed-in user.
func requireUser(h func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := r.Context().Value(userKey{}).(store.User)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		h(w, r, u)
	}
}

// allowedHost is the DNS-rebinding defense: only loopback names, with the
// server's port or none, may address this app.
func allowedHost(host, port string) bool {
	name, p := host, ""
	switch {
	case strings.HasPrefix(host, "["):
		end := strings.Index(host, "]")
		if end < 0 {
			return false
		}
		name = host[1:end]
		if rest := host[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return false
			}
			p = rest[1:]
			if p == "" {
				return false
			}
		}
	case strings.Contains(host, ":"):
		var err error
		if name, p, err = net.SplitHostPort(host); err != nil || p == "" {
			return false
		}
	}
	switch strings.ToLower(name) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return false
	}
	return p == "" || (port != "" && p == port)
}

const errorTemplate = `<!doctype html><meta charset="utf-8"><title>eve-wallets</title>` +
	`<body style="font-family:sans-serif;max-width:32rem;margin:3rem auto"><h1>%s</h1><p><a href="/">Back to eve-wallets</a></p>`

func errorPage(w http.ResponseWriter, code int, msg string) {
	// msg is always a constant of this package, so no escaping is needed.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(strings.Replace(errorTemplate, "%s", msg, 1)))
}
