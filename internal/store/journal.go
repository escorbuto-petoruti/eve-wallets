package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// JournalEntry is one stored wallet journal row. AmountCents is signed.
type JournalEntry struct {
	ID          int64 // the ESI journal entry id
	Date        time.Time
	AmountCents int64
	RefType     string
	Description string
}

// JournalCursor is the keyset position after which a page continues: the Date
// and ID of the last entry already seen.
type JournalCursor struct {
	Date time.Time
	ID   int64
}

// JournalFilter selects journal rows of one wallet, newest first. RefType ""
// matches every type; a zero From or To leaves that side open (both are
// inclusive, in seconds). After continues a previous page. Limit is the
// maximum number of rows returned.
type JournalFilter struct {
	WalletID int64
	RefType  string
	From, To time.Time
	After    *JournalCursor
	Limit    int
}

// AddJournalEntries stores entries for a wallet in one transaction and returns
// how many were new. Entries already stored (same wallet and entry id) are
// left untouched, so repeating a call inserts nothing.
func (s *Store) AddJournalEntries(ctx context.Context, walletID int64, entries []JournalEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: add journal entries: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO journal (wallet_id, entry_id, date, amount_cents, ref_type, description)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("store: add journal entries: %w", err)
	}
	defer stmt.Close()
	added := 0
	for _, e := range entries {
		res, err := stmt.ExecContext(ctx, walletID, e.ID, e.Date.Unix(), e.AmountCents, e.RefType, e.Description)
		if err != nil {
			return 0, fmt.Errorf("store: add journal entry %d: %w", e.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: add journal entry %d: %w", e.ID, err)
		}
		added += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: add journal entries: %w", err)
	}
	return added, nil
}

// Journal returns the rows matching f ordered newest first (date, then entry
// id, both descending).
func (s *Store) Journal(ctx context.Context, f JournalFilter) ([]JournalEntry, error) {
	var where []string
	args := []any{f.WalletID}
	where = append(where, "wallet_id = ?")
	if f.RefType != "" {
		where = append(where, "ref_type = ?")
		args = append(args, f.RefType)
	}
	if !f.From.IsZero() {
		where = append(where, "date >= ?")
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		where = append(where, "date <= ?")
		args = append(args, f.To.Unix())
	}
	if f.After != nil {
		where = append(where, "(date < ? OR (date = ? AND entry_id < ?))")
		args = append(args, f.After.Date.Unix(), f.After.Date.Unix(), f.After.ID)
	}
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_id, date, amount_cents, ref_type, description
		FROM journal WHERE `+strings.Join(where, " AND ")+`
		ORDER BY date DESC, entry_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list journal: %w", err)
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.AmountCents, &e.RefType, &e.Description); err != nil {
			return nil, fmt.Errorf("store: scan journal: %w", err)
		}
		e.Date = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// JournalRefTypes returns the distinct reference types stored for a wallet,
// sorted alphabetically.
func (s *Store) JournalRefTypes(ctx context.Context, walletID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ref_type FROM journal WHERE wallet_id = ? ORDER BY ref_type`, walletID)
	if err != nil {
		return nil, fmt.Errorf("store: list journal types: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("store: scan journal type: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
