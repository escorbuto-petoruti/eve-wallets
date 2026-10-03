package web

import (
	"bytes"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const (
	moveCookie = "eve_move"
	moveTTL    = 10 * time.Minute

	// maxPendingMoves bounds the in-memory map of unconfirmed moves.
	maxPendingMoves = 16
)

// pendingMove is a character the signed-in user asked to take over from
// another user, held until the user confirms. It carries the new refresh
// token, so it lives in memory only and is never logged or rendered.
type pendingMove struct {
	characterID  int64
	name         string
	refreshToken string
	scopes       []string
	targetUser   int64 // a user id, never a character id
	expires      time.Time
}

// String and GoString keep the refresh token out of any accidental formatting.
func (pendingMove) String() string   { return "pendingMove{redacted}" }
func (pendingMove) GoString() string { return "pendingMove{redacted}" }

// pendingMoves holds the unconfirmed moves by random id. Entries are single
// use, expire after moveTTL and the map is bounded.
type pendingMoves struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]pendingMove
}

func newPendingMoves(now func() time.Time) *pendingMoves {
	return &pendingMoves{now: now, m: make(map[string]pendingMove)}
}

// add stores pm and returns the id that names it.
func (p *pendingMoves) add(pm pendingMove) (string, error) {
	id, err := randomValue()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, m := range p.m {
		if !now.Before(m.expires) {
			delete(p.m, k)
		}
	}
	for len(p.m) >= maxPendingMoves { // evict the move closest to expiring
		var oldest string
		var at time.Time
		for k, m := range p.m {
			if oldest == "" || m.expires.Before(at) {
				oldest, at = k, m.expires
			}
		}
		delete(p.m, oldest)
	}
	pm.expires = now.Add(moveTTL)
	p.m[id] = pm
	return id, nil
}

// peek returns the move of id when it is still valid and was made for target.
func (p *pendingMoves) peek(id string, target int64) (pendingMove, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lookup(id, target)
}

// take consumes the move of id for target: a second call always misses. A move
// made for another user is left alone.
func (p *pendingMoves) take(id string, target int64) (pendingMove, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pm, ok := p.lookup(id, target)
	if ok {
		delete(p.m, id)
	}
	return pm, ok
}

// lookup requires p.mu. An expired entry is dropped.
func (p *pendingMoves) lookup(id string, target int64) (pendingMove, bool) {
	pm, ok := p.m[id]
	if !ok {
		return pendingMove{}, false
	}
	if !p.now().Before(pm.expires) {
		delete(p.m, id)
		return pendingMove{}, false
	}
	if pm.targetUser != target {
		return pendingMove{}, false
	}
	return pm, true
}

func (p *pendingMoves) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.m)
}

// sessionUser is the user the session cookie resolved to, if any.
func sessionUser(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userKey{}).(store.User)
	return u, ok
}

// addCharacter starts an EVE SSO round trip whose result is attached to the
// signed-in user. It is a GET that changes nothing itself, but it is still
// limited to same-origin requests so another site cannot start the flow.
func (s *server) addCharacter(w http.ResponseWriter, r *http.Request) {
	u, ok := sessionUser(r)
	if !ok {
		errorPage(w, http.StatusUnauthorized, "Sign in first.")
		return
	}
	if !s.sameOrigin(r) {
		errorPage(w, http.StatusForbidden, "Cross-site request refused.")
		return
	}
	s.beginFlow(w, r, intentAdd, u.UserID)
}

// finishAdd completes an add flow: the SSO round trip as the character proves
// control of it. It never creates a user row or a session; the existing session
// stays. Messages are fixed strings, like the rest of the callback.
func (s *server) finishAdd(w http.ResponseWriter, r *http.Request, flow loginFlow, tokens sso.TokenSet, claims sso.Claims) {
	ctx := r.Context()
	u, ok := sessionUser(r)
	if !ok || u.UserID != flow.userID {
		errorPage(w, http.StatusForbidden, "Sign in as the same user that started this, then add the character again.")
		return
	}
	owner, attached, err := s.deps.Store.TokenOwner(ctx, claims.CharacterID)
	if err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not save the character.")
		return
	}
	if attached && owner != u.UserID {
		// Another user's character is never taken silently: hold it until the
		// signed-in user confirms the move.
		id, err := s.moves.add(pendingMove{
			characterID: claims.CharacterID, name: claims.CharacterName, refreshToken: tokens.RefreshToken,
			scopes: claims.Scopes, targetUser: u.UserID,
		})
		if err != nil {
			errorPage(w, http.StatusInternalServerError, "Could not start the move.")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: moveCookie, Value: id, Path: "/auth", MaxAge: int(moveTTL / time.Second),
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/auth/confirm-move", http.StatusSeeOther)
		return
	}
	// Not attached yet, or already this user's: attach or refresh under the
	// session user's id.
	err = s.deps.Store.SaveToken(ctx, store.Token{
		CharacterID: claims.CharacterID, UserID: u.UserID, CharacterName: claims.CharacterName,
		RefreshToken: tokens.RefreshToken, Scopes: claims.Scopes, UpdatedAt: s.now(),
	})
	if err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not save the character.")
		return
	}
	if s.deps.OnLogin != nil {
		s.deps.OnLogin(claims.CharacterID)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

const expiredMovePage = "This confirmation expired. Add the character again."

var confirmMoveTemplate = template.Must(template.New("confirm-move").Parse(`<!doctype html><meta charset="utf-8"><title>eve-wallets</title>
<body style="font-family:sans-serif;max-width:32rem;margin:3rem auto">
<h1>Move {{.Name}} to your account?</h1>
<p>{{.Name}} belongs to another user. Moving it removes it from that account; that account's other characters are not affected.</p>
<form method="post" action="/auth/move-character"><button type="submit">Move</button></form>
<form method="post" action="/auth/cancel-move"><button type="submit">Cancel</button></form>
`))

// moveCookieValue is the id of the pending move the browser holds, or "".
func moveCookieValue(r *http.Request) string {
	if c, err := r.Cookie(moveCookie); err == nil {
		return c.Value
	}
	return ""
}

// confirmMove asks the signed-in user to confirm taking over a character that
// belongs to another user. The page never says who that user is.
func (s *server) confirmMove(w http.ResponseWriter, r *http.Request) {
	u, ok := sessionUser(r)
	if !ok {
		errorPage(w, http.StatusUnauthorized, "Sign in first.")
		return
	}
	pm, ok := s.moves.peek(moveCookieValue(r), u.UserID)
	if !ok {
		errorPage(w, http.StatusBadRequest, expiredMovePage)
		return
	}
	var page bytes.Buffer
	if err := confirmMoveTemplate.Execute(&page, struct{ Name string }{pm.name}); err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not show the confirmation.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page.Bytes())
}

// takeMove authorizes a confirmation POST and consumes the pending move before
// anything is written. It answers the error page itself when it refuses.
func (s *server) takeMove(w http.ResponseWriter, r *http.Request) (store.User, pendingMove, bool) {
	if !s.sameOrigin(r) {
		errorPage(w, http.StatusForbidden, "Cross-site request refused.")
		return store.User{}, pendingMove{}, false
	}
	u, ok := sessionUser(r)
	if !ok {
		errorPage(w, http.StatusUnauthorized, "Sign in first.")
		return store.User{}, pendingMove{}, false
	}
	pm, ok := s.moves.take(moveCookieValue(r), u.UserID)
	if !ok {
		errorPage(w, http.StatusBadRequest, expiredMovePage)
		return store.User{}, pendingMove{}, false
	}
	return u, pm, true
}

// moveCharacter re-parents the pending character to the session user and
// stores the refresh token the SSO round trip just produced.
func (s *server) moveCharacter(w http.ResponseWriter, r *http.Request) {
	u, pm, ok := s.takeMove(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	clearCookie(w, moveCookie, "/auth")
	if err := s.deps.Store.MoveToken(ctx, pm.characterID, u.UserID); err != nil {
		errorPage(w, http.StatusInternalServerError, "Could not move the character. Nothing was changed.")
		return
	}
	err := s.deps.Store.SaveToken(ctx, store.Token{
		CharacterID: pm.characterID, UserID: u.UserID, CharacterName: pm.name,
		RefreshToken: pm.refreshToken, Scopes: pm.scopes, UpdatedAt: s.now(),
	})
	if err != nil {
		errorPage(w, http.StatusInternalServerError,
			"The character was moved, but its new sign-in could not be saved. The previous token is kept; add the character again.")
		return
	}
	if s.deps.OnLogin != nil {
		s.deps.OnLogin(pm.characterID)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// cancelMove drops the pending move without writing anything.
func (s *server) cancelMove(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.takeMove(w, r); !ok {
		return
	}
	clearCookie(w, moveCookie, "/auth")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
