package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LoyaltyPoints is the points a character holds with one issuing corporation.
type LoyaltyPoints struct {
	CorporationID int64
	Points        int64
}

// CharacterLoyalty is one stored loyalty row of a character.
type CharacterLoyalty struct {
	CharacterID   int64
	CorporationID int64
	Points        int64
	FetchedAt     time.Time
}

// ReplaceLoyalty makes rows the whole snapshot of the character in one
// transaction: corporations that are no longer listed are removed. The
// character must have a token (a foreign key), and a corporation listed twice
// fails the whole replace, leaving the previous snapshot untouched.
func (s *Store) ReplaceLoyalty(ctx context.Context, characterID int64, rows []LoyaltyPoints, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin loyalty replace for character %d: %w", characterID, err)
	}
	defer func() { _ = tx.Rollback() }()
	previous, err := previousLoyalty(ctx, tx, characterID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM loyalty_points WHERE character_id = ?`, characterID); err != nil {
		return fmt.Errorf("store: clear loyalty of character %d: %w", characterID, err)
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO loyalty_points (character_id, corporation_id, points, fetched_at) VALUES (?, ?, ?, ?)`,
			characterID, r.CorporationID, r.Points, at.Unix()); err != nil {
			return fmt.Errorf("store: save loyalty of character %d: %w", characterID, err)
		}
		if err := appendLoyaltyHistory(ctx, tx, characterID, r.CorporationID, r.Points, at); err != nil {
			return err
		}
		delete(previous, r.CorporationID)
	}
	// Corporations that held points and are no longer returned dropped to zero.
	for corp, pts := range previous {
		if pts == 0 {
			continue
		}
		if err := appendLoyaltyHistory(ctx, tx, characterID, corp, 0, at); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit loyalty of character %d: %w", characterID, err)
	}
	return nil
}

// previousLoyalty reads the stored snapshot of a character as points by
// corporation, inside the replace transaction.
func previousLoyalty(ctx context.Context, tx *sql.Tx, characterID int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT corporation_id, points FROM loyalty_points WHERE character_id = ?`, characterID)
	if err != nil {
		return nil, fmt.Errorf("store: read loyalty of character %d: %w", characterID, err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var corp, pts int64
		if err := rows.Scan(&corp, &pts); err != nil {
			return nil, fmt.Errorf("store: scan loyalty: %w", err)
		}
		out[corp] = pts
	}
	return out, rows.Err()
}

// appendLoyaltyHistory stores points for the pair at the given time, unless
// they equal the last stored value. A second value at the same instant
// replaces the first.
func appendLoyaltyHistory(ctx context.Context, tx *sql.Tx, characterID, corporationID, points int64, at time.Time) error {
	var last int64
	err := tx.QueryRowContext(ctx, `
		SELECT points FROM loyalty_history WHERE character_id = ? AND corporation_id = ?
		ORDER BY taken_at DESC LIMIT 1`, characterID, corporationID).Scan(&last)
	if err == nil && last == points {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: read loyalty history of character %d: %w", characterID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO loyalty_history (character_id, corporation_id, taken_at, points) VALUES (?, ?, ?, ?)
		ON CONFLICT (character_id, corporation_id, taken_at) DO UPDATE SET points = excluded.points`,
		characterID, corporationID, at.Unix(), points); err != nil {
		return fmt.Errorf("store: save loyalty history of character %d: %w", characterID, err)
	}
	return nil
}

// LoyaltyForUser lists the loyalty rows of the characters whose token belongs
// to the user, by character id and then points descending. A user with no
// characters gets an empty list.
func (s *Store) LoyaltyForUser(ctx context.Context, userID int64) ([]CharacterLoyalty, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.character_id, l.corporation_id, l.points, l.fetched_at
		FROM loyalty_points l JOIN tokens t ON t.character_id = l.character_id
		WHERE t.user_id = ?
		ORDER BY l.character_id, l.points DESC, l.corporation_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: loyalty for user %d: %w", userID, err)
	}
	defer rows.Close()
	var out []CharacterLoyalty
	for rows.Next() {
		var l CharacterLoyalty
		var at int64
		if err := rows.Scan(&l.CharacterID, &l.CorporationID, &l.Points, &at); err != nil {
			return nil, fmt.Errorf("store: scan loyalty: %w", err)
		}
		l.FetchedAt = time.Unix(at, 0).UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpsertCorporationNames caches corporation names, replacing earlier ones.
func (s *Store) UpsertCorporationNames(ctx context.Context, names map[int64]string, at time.Time) error {
	if len(names) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin corporation names: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for id, name := range names {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO corporation_names (corporation_id, name, fetched_at) VALUES (?, ?, ?)
			ON CONFLICT (corporation_id) DO UPDATE SET name = excluded.name, fetched_at = excluded.fetched_at`,
			id, name, at.Unix()); err != nil {
			return fmt.Errorf("store: save corporation name %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit corporation names: %w", err)
	}
	return nil
}

// CorporationNames returns the cached names of the given corporations; ids with
// no cached name are absent from the map.
func (s *Store) CorporationNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.db.QueryContext(ctx,
		`SELECT corporation_id, name FROM corporation_names WHERE corporation_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: corporation names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("store: scan corporation name: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ScopesForUser returns the granted scopes of every character whose token
// belongs to the user, keyed by character id. Refresh tokens are never read.
func (s *Store) ScopesForUser(ctx context.Context, userID int64) (map[int64][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT character_id, scopes FROM tokens WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: scopes for user %d: %w", userID, err)
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var scopes string
		if err := rows.Scan(&id, &scopes); err != nil {
			return nil, fmt.Errorf("store: scan scopes: %w", err)
		}
		out[id] = strings.Fields(scopes)
	}
	return out, rows.Err()
}
