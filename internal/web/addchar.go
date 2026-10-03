package web

import (
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
