package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User is an EVE character that signed in to the app. UserID is the user id
// that user_wallets.user_id, tokens.user_id and sessions.user_id reference;
// today it equals CharacterID only because users.character_id is the primary
// key those foreign keys point at. It must never be inferred from a character
// id: carry it from the session row and use it to key the wallet links.
type User struct {
	CharacterID int64
	UserID      int64
	Name        string
}

// Token is the stored refresh token of one registered character. UserID is the
// user the character belongs to (equal to CharacterID for a user's own
// character). The refresh token is a secret: never log it or put it in errors.
type Token struct {
	CharacterID   int64
	UserID        int64
	CharacterName string
	RefreshToken  string
	Scopes        []string  // stored space-separated, EVE style
	UpdatedAt     time.Time // set by SaveToken to the current time when zero
}

// UpsertUser creates the user or updates its name; created_at is kept.
func (s *Store) UpsertUser(ctx context.Context, characterID int64, name string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (character_id, name, created_at) VALUES (?, ?, ?)
		ON CONFLICT (character_id) DO UPDATE SET name = excluded.name`,
		characterID, name, now.Unix())
	if err != nil {
		return fmt.Errorf("store: upsert user %d: %w", characterID, err)
	}
	return nil
}

// SaveToken inserts or replaces the token of t.CharacterID in one statement
// (so a rotated refresh token is never half written). The user must exist.
func (s *Store) SaveToken(ctx context.Context, t Token) error {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tokens (character_id, user_id, character_name, refresh_token, scopes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (character_id) DO UPDATE SET
			user_id = excluded.user_id,
			character_name = excluded.character_name,
			refresh_token = excluded.refresh_token,
			scopes = excluded.scopes,
			updated_at = excluded.updated_at`,
		t.CharacterID, t.UserID, t.CharacterName, t.RefreshToken, strings.Join(t.Scopes, " "), t.UpdatedAt.Unix())
	if err != nil {
		// The driver error never carries bound values, but stay explicit.
		return fmt.Errorf("store: save token for character %d: %w", t.CharacterID, err)
	}
	return nil
}

// SaveTokenIfOwner is SaveToken that never takes a character over: in one
// statement it inserts the token, or updates it only when the stored row
// already belongs to t.UserID. applied is false, with nothing written, when the
// character is attached to another user. The user must exist.
func (s *Store) SaveTokenIfOwner(ctx context.Context, t Token) (applied bool, err error) {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO tokens (character_id, user_id, character_name, refresh_token, scopes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (character_id) DO UPDATE SET
			character_name = excluded.character_name,
			refresh_token = excluded.refresh_token,
			scopes = excluded.scopes,
			updated_at = excluded.updated_at
		WHERE tokens.user_id = excluded.user_id`,
		t.CharacterID, t.UserID, t.CharacterName, t.RefreshToken, strings.Join(t.Scopes, " "), t.UpdatedAt.Unix())
	if err != nil {
		return false, fmt.Errorf("store: save token for character %d: %w", t.CharacterID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: save token for character %d: %w", t.CharacterID, err)
	}
	return n > 0, nil
}

const tokenColumns = `character_id, user_id, character_name, refresh_token, scopes, updated_at`

func scanToken(sc interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var scopes string
	var at int64
	if err := sc.Scan(&t.CharacterID, &t.UserID, &t.CharacterName, &t.RefreshToken, &scopes, &at); err != nil {
		return Token{}, err
	}
	t.Scopes = strings.Fields(scopes)
	t.UpdatedAt = time.Unix(at, 0).UTC()
	return t, nil
}

// GetToken returns the token of a character; ok is false when none is stored.
func (s *Store) GetToken(ctx context.Context, characterID int64) (Token, bool, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx,
		`SELECT `+tokenColumns+` FROM tokens WHERE character_id = ?`, characterID))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, false, nil
	}
	if err != nil {
		return Token{}, false, fmt.Errorf("store: get token %d: %w", characterID, err)
	}
	return t, true, nil
}

// Tokens lists every registered token ordered by character id.
func (s *Store) Tokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM tokens ORDER BY character_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken removes a character's token; a missing token is not an error.
func (s *Store) DeleteToken(ctx context.Context, characterID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE character_id = ?`, characterID); err != nil {
		return fmt.Errorf("store: delete token %d: %w", characterID, err)
	}
	return nil
}

// UserCharacter is a character registered under a user. It deliberately has
// no token fields: it is safe to hand to the web layer.
type UserCharacter struct {
	CharacterID int64
	Name        string
}

// TokenOwner returns the user that owns the character's token; ok is false
// when the character has no token.
func (s *Store) TokenOwner(ctx context.Context, characterID int64) (userID int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id FROM tokens WHERE character_id = ?`, characterID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: token owner %d: %w", characterID, err)
	}
	return userID, true, nil
}

// CharactersForUser lists the characters whose token belongs to the user,
// ordered by character id. Refresh tokens are never read.
func (s *Store) CharactersForUser(ctx context.Context, userID int64) ([]UserCharacter, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT character_id, character_name FROM tokens WHERE user_id = ? ORDER BY character_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: characters for user %d: %w", userID, err)
	}
	defer rows.Close()
	var out []UserCharacter
	for rows.Next() {
		var c UserCharacter
		if err := rows.Scan(&c.CharacterID, &c.Name); err != nil {
			return nil, fmt.Errorf("store: scan character: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MoveToken re-parents a character's token to toUser in one transaction,
// keeping the refresh token and scopes. The previous user loses the link to
// the character's personal wallet and toUser gains it; if the previous user is
// left with no token its row is deleted (sessions and links cascade, wallets
// stay). All corporation wallet links of the previous user are dropped too: the
// store cannot tell which corporation the character gave access to, so the next
// collection restores the links of every user whose characters can still read
// the corporation. The new user's corporation links also come from collection.
// A missing token or target
// user wraps ErrNotFound; moving to the current owner is a no-op.
func (s *Store) MoveToken(ctx context.Context, characterID, toUser int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin move of character %d: %w", characterID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var from int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM tokens WHERE character_id = ?`, characterID).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: move character %d: no token: %w", characterID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: move character %d: %w", characterID, err)
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE character_id = ?`, toUser).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: move character %d: user %d: %w", characterID, toUser, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: move character %d: %w", characterID, err)
	}
	if from == toUser {
		return nil
	}

	const personal = `SELECT id FROM wallets WHERE kind = 'character' AND owner_id = ?`
	const corporations = `SELECT id FROM wallets WHERE kind = 'corporation'`
	steps := []struct {
		query string
		args  []any
	}{
		{`UPDATE tokens SET user_id = ? WHERE character_id = ?`, []any{toUser, characterID}},
		{`DELETE FROM user_wallets WHERE user_id = ? AND wallet_id IN (` + personal + `)`, []any{from, characterID}},
		{`DELETE FROM user_wallets WHERE user_id = ? AND wallet_id IN (` + corporations + `)`, []any{from}},
		{`INSERT OR IGNORE INTO user_wallets (user_id, wallet_id) ` +
			`SELECT ?, id FROM wallets WHERE kind = 'character' AND owner_id = ?`, []any{toUser, characterID}},
		{`DELETE FROM users WHERE character_id = ? AND NOT EXISTS (SELECT 1 FROM tokens WHERE user_id = ?)`, []any{from, from}},
	}
	for _, st := range steps {
		if _, err := tx.ExecContext(ctx, st.query, st.args...); err != nil {
			return fmt.Errorf("store: move character %d: %w", characterID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit move of character %d: %w", characterID, err)
	}
	return nil
}

// LinkWallet lets a user see a wallet. Linking twice is a no-op.
func (s *Store) LinkWallet(ctx context.Context, userID, walletID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO user_wallets (user_id, wallet_id) VALUES (?, ?)`, userID, walletID)
	if err != nil {
		return fmt.Errorf("store: link wallet %d to user %d: %w", walletID, userID, err)
	}
	return nil
}

// CreateSession stores a login session under the hash of its id.
func (s *Store) CreateSession(ctx context.Context, idHash string, userID int64, createdAt, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		idHash, userID, createdAt.Unix(), expiresAt.Unix())
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionUser returns the user of a session that has not expired at now
// (a session is valid while now < expires_at), with UserID taken from the
// session row: it keys the wallet links and must not be inferred from the
// character id.
func (s *Store) SessionUser(ctx context.Context, idHash string, now time.Time) (User, bool, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.character_id, u.name, s.user_id
		FROM sessions s JOIN users u ON u.character_id = s.user_id
		WHERE s.id_hash = ? AND s.expires_at > ?`, idHash, now.Unix()).Scan(&u.CharacterID, &u.Name, &u.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("store: session user: %w", err)
	}
	return u, true, nil
}

// DeleteSession removes a session; a missing one is not an error.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions deletes sessions expired at now and returns how many.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	return n, nil
}

const userScope = `JOIN user_wallets uw ON uw.wallet_id = w.id AND uw.user_id = ?`

// WalletsForUser lists the wallets linked to the user, in Wallets order. A
// user with no links gets an empty list, never every wallet.
func (s *Store) WalletsForUser(ctx context.Context, userID int64) ([]Wallet, error) {
	return s.wallets(ctx, userScope, []any{userID})
}

// LatestBalancesForUser is LatestBalances restricted to the user's wallets.
func (s *Store) LatestBalancesForUser(ctx context.Context, userID int64) ([]WalletBalance, error) {
	return s.latestBalances(ctx, userScope, []any{userID})
}

// SeriesForUser is Series restricted to the user's wallets; wallet ids in the
// filter that the user cannot see are ignored.
func (s *Store) SeriesForUser(ctx context.Context, userID int64, f SeriesFilter) ([]Point, error) {
	return s.series(ctx, &userID, f)
}
