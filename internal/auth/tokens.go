package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// expiryMargin is how long before expiry a cached access token stops being
// reused.
const expiryMargin = 60 * time.Second

// ErrReauthRequired reports that EVE SSO rejected the stored grant (revoked or
// expired): the person must sign in again. Use errors.Is; the concrete error is
// a *ReauthError.
var ErrReauthRequired = errors.New("auth: sign in again")

// ReauthError names the character that has to sign in again. It never carries
// a token or any SSO response text.
type ReauthError struct {
	CharacterID int64
	Name        string
}

func (e *ReauthError) Error() string {
	return fmt.Sprintf("auth: character %d (%s) must sign in again", e.CharacterID, e.Name)
}

// Is makes errors.Is(err, ErrReauthRequired) true.
func (e *ReauthError) Is(target error) bool { return target == ErrReauthRequired }

// TokenStore is the subset of *store.Store that StoreTokens needs.
type TokenStore interface {
	Tokens(ctx context.Context) ([]store.Token, error)
	GetToken(ctx context.Context, characterID int64) (store.Token, bool, error)
	SaveToken(ctx context.Context, t store.Token) error
}

// Refresher trades a refresh token for new tokens; *sso.Client implements it.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (sso.TokenSet, error)
}

var _ TokenSource = (*StoreTokens)(nil)

// StoreTokens is a TokenSource backed by the refresh tokens in the local
// SQLite store. Access tokens are cached in memory only.
type StoreTokens struct {
	store TokenStore
	sso   Refresher
	now   func() time.Time

	mu    sync.Mutex // guards chars
	chars map[int64]*charState
}

// charState serializes the refreshes of one character and holds its cached
// access token.
type charState struct {
	mu      sync.Mutex
	access  string
	expires time.Time
}

// NewStoreTokens returns a StoreTokens. A nil now means time.Now.
func NewStoreTokens(st TokenStore, rf Refresher, now func() time.Time) *StoreTokens {
	if now == nil {
		now = time.Now
	}
	return &StoreTokens{store: st, sso: rf, now: now, chars: make(map[int64]*charState)}
}

// Characters lists every registered token as a Character.
func (s *StoreTokens) Characters(ctx context.Context) ([]Character, error) {
	toks, err := s.store.Tokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: list characters: %w", err)
	}
	out := make([]Character, 0, len(toks))
	for _, t := range toks {
		out = append(out, Character{ID: t.CharacterID, Name: t.CharacterName, Scopes: t.Scopes, UserID: t.UserID})
	}
	return out, nil
}

func (s *StoreTokens) state(id int64) *charState {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.chars[id]
	if cs == nil {
		cs = &charState{}
		s.chars[id] = cs
	}
	return cs
}

// Token returns a valid access token for the character, refreshing it when the
// cached one is missing or within 60 seconds of expiry.
//
// Refreshes are serialized per character, so two callers never present the
// same refresh token (EVE may rotate it, which would invalidate the second
// use). A rotated refresh token is saved before the access token is returned.
func (s *StoreTokens) Token(ctx context.Context, characterID int64) (string, error) {
	cs := s.state(characterID)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.access != "" && s.now().Before(cs.expires.Add(-expiryMargin)) {
		return cs.access, nil
	}
	cs.access = ""

	tok, ok, err := s.store.GetToken(ctx, characterID)
	if err != nil {
		return "", fmt.Errorf("auth: token for character %d: %w", characterID, err)
	}
	if !ok {
		return "", fmt.Errorf("auth: token for character %d: character is not registered", characterID)
	}

	ts, err := s.sso.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		return "", refreshError(tok, err)
	}
	if ts.RefreshToken != "" && ts.RefreshToken != tok.RefreshToken {
		next := tok
		next.RefreshToken = ts.RefreshToken
		next.UpdatedAt = s.now()
		if err := s.store.SaveToken(ctx, next); err != nil {
			// The new refresh token could not be kept, so the grant may be lost
			// on the next restart: do not hand out the access token.
			return "", fmt.Errorf("auth: persist rotated token for character %d: %s",
				characterID, scrub(err.Error(), tok.RefreshToken, ts.RefreshToken, ts.AccessToken))
		}
	}
	cs.access = ts.AccessToken
	cs.expires = s.now().Add(ts.ExpiresIn)
	return cs.access, nil
}

// refreshError converts an SSO failure into an error without token material.
func refreshError(tok store.Token, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("auth: refresh token for character %d: %w", tok.CharacterID, ctxErr(err))
	}
	if strings.Contains(err.Error(), "invalid_grant") {
		return &ReauthError{CharacterID: tok.CharacterID, Name: tok.CharacterName}
	}
	return fmt.Errorf("auth: refresh token for character %d: %s", tok.CharacterID, scrub(err.Error(), tok.RefreshToken))
}

// ctxErr returns the context sentinel inside err, dropping its wrapping text.
func ctxErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return context.DeadlineExceeded
}

// scrub makes msg a short single line without any of the secrets.
func scrub(msg string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "[redacted]")
		}
	}
	msg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxErrDetail {
		msg = strings.ToValidUTF8(msg[:maxErrDetail], "") + "..."
	}
	return msg
}
