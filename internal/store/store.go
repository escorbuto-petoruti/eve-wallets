// Package store persists wallet balance history in SQLite.
//
// Money is always stored as integer ISK cents, never as floating point.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// Kind is the type of owner of a wallet.
type Kind string

// Wallet kinds.
const (
	KindCharacter   Kind = "character"
	KindCorporation Kind = "corporation"
)

// Sentinel errors.
var (
	// ErrNotFound reports that a wallet id does not exist.
	ErrNotFound = errors.New("store: wallet not found")
	// ErrInvalidName reports a wallet name that is empty, too long or contains
	// control characters.
	ErrInvalidName = errors.New("store: invalid wallet name")
)

// maxNameLen is the longest accepted wallet name, in characters.
const maxNameLen = 64

// NameSource tells where a wallet's displayed name comes from.
type NameSource string

// Name sources, in order of precedence.
const (
	NameCustom  NameSource = "custom"  // chosen by the user
	NameESI     NameSource = "esi"     // reported by ESI
	NameDefault NameSource = "default" // derived from the owner and division
)

// Wallet identifies one EVE wallet: a character wallet (Division 0) or one
// division (1-7) of a corporation wallet. Label and ESIName are empty when
// unset.
type Wallet struct {
	ID        int64
	Kind      Kind
	OwnerID   int64
	OwnerName string
	Division  int
	Label     string // user-chosen name
	ESIName   string // name reported by ESI
}

// DisplayName returns the label if set, else the ESI name, else the default:
// the owner name for a character wallet, "Master Wallet" for the first
// division of a corporation (ESI reports no name for it) and "Division N" for
// the others.
func (w Wallet) DisplayName() string {
	switch {
	case w.Label != "":
		return w.Label
	case w.ESIName != "":
		return w.ESIName
	case w.Kind == KindCorporation && w.Division == 1:
		return "Master Wallet"
	case w.Kind == KindCorporation:
		return fmt.Sprintf("Division %d", w.Division)
	default:
		return w.OwnerName
	}
}

// NameSource reports which name DisplayName returns.
func (w Wallet) NameSource() NameSource {
	switch {
	case w.Label != "":
		return NameCustom
	case w.ESIName != "":
		return NameESI
	default:
		return NameDefault
	}
}

// WalletBalance is a wallet together with its most recent balance.
type WalletBalance struct {
	Wallet Wallet
	At     time.Time
	Cents  int64
}

// Point is one balance observation in a time series.
type Point struct {
	WalletID int64
	At       time.Time
	Cents    int64
}

// SeriesFilter narrows a Series query. Zero values mean "no restriction".
// From and To are inclusive.
type SeriesFilter struct {
	WalletIDs []int64
	From, To  time.Time
}

// migrations are applied in order; the number applied is tracked with
// PRAGMA user_version. Never edit an existing entry, append a new one.
var migrations = []string{
	`CREATE TABLE wallets (
		id         INTEGER PRIMARY KEY,
		kind       TEXT    NOT NULL CHECK (kind IN ('character', 'corporation')),
		owner_id   INTEGER NOT NULL,
		owner_name TEXT    NOT NULL,
		division   INTEGER NOT NULL DEFAULT 0,
		UNIQUE (kind, owner_id, division),
		CHECK (
			(kind = 'character'   AND division = 0) OR
			(kind = 'corporation' AND division BETWEEN 1 AND 7)
		)
	);
	CREATE TABLE balances (
		id          INTEGER PRIMARY KEY,
		wallet_id   INTEGER NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
		taken_at    INTEGER NOT NULL,
		cents       INTEGER NOT NULL,
		source      TEXT    NOT NULL CHECK (source IN ('snapshot', 'journal')),
		journal_ref INTEGER,
		CHECK ((source = 'journal') = (journal_ref IS NOT NULL))
	);
	CREATE UNIQUE INDEX balances_journal_uq
		ON balances (wallet_id, journal_ref) WHERE journal_ref IS NOT NULL;
	CREATE UNIQUE INDEX balances_snapshot_uq
		ON balances (wallet_id, taken_at) WHERE source = 'snapshot';
	CREATE INDEX balances_wallet_time ON balances (wallet_id, taken_at);`,
	// 2: wallet names. label is chosen by the user, esi_name comes from ESI.
	`ALTER TABLE wallets ADD COLUMN label TEXT;
	ALTER TABLE wallets ADD COLUMN esi_name TEXT;`,
	// 3: users (EVE characters that signed in), their stored refresh tokens,
	// which wallets each user may see, and login sessions. A token's user_id is
	// separate from its character_id: today a user owns only their own
	// character, later they may register more. The user id is the one key that
	// tokens.user_id, user_wallets.user_id and sessions.user_id reference:
	// today it is users.character_id, but never infer it from a character id —
	// carry it from the session row (store.User.UserID).
	`CREATE TABLE users (
		character_id INTEGER PRIMARY KEY,
		name         TEXT    NOT NULL,
		created_at   INTEGER NOT NULL
	);
	CREATE TABLE tokens (
		character_id   INTEGER PRIMARY KEY,
		user_id        INTEGER NOT NULL REFERENCES users(character_id) ON DELETE CASCADE,
		character_name TEXT    NOT NULL,
		refresh_token  TEXT    NOT NULL,
		scopes         TEXT    NOT NULL,
		updated_at     INTEGER NOT NULL
	);
	CREATE INDEX tokens_user ON tokens (user_id);
	CREATE TABLE user_wallets (
		user_id   INTEGER NOT NULL REFERENCES users(character_id) ON DELETE CASCADE,
		wallet_id INTEGER NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, wallet_id)
	);
	CREATE INDEX user_wallets_wallet ON user_wallets (wallet_id);
	CREATE TABLE sessions (
		id_hash    TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(character_id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX sessions_expires ON sessions (expires_at);`,
	// 6: the wallet journal. date is unix seconds, amount_cents is signed
	// integer cents; (wallet_id, entry_id) makes inserts idempotent.
	`CREATE TABLE journal (
		id           INTEGER PRIMARY KEY,
		wallet_id    INTEGER NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
		entry_id     INTEGER NOT NULL,
		date         INTEGER NOT NULL,
		amount_cents INTEGER NOT NULL,
		ref_type     TEXT    NOT NULL,
		description  TEXT    NOT NULL,
		UNIQUE (wallet_id, entry_id)
	);
	CREATE INDEX journal_wallet_date ON journal (wallet_id, date DESC, entry_id DESC);`,
	// 5: the latest loyalty points snapshot per character and issuing
	// corporation (fetched_at is unix seconds), removed with the character's
	// token, and a cache of corporation names.
	`CREATE TABLE loyalty_points (
		character_id   INTEGER NOT NULL REFERENCES tokens(character_id) ON DELETE CASCADE,
		corporation_id INTEGER NOT NULL,
		points         INTEGER NOT NULL,
		fetched_at     INTEGER NOT NULL,
		PRIMARY KEY (character_id, corporation_id)
	);
	CREATE TABLE corporation_names (
		corporation_id INTEGER PRIMARY KEY,
		name           TEXT    NOT NULL,
		fetched_at     INTEGER NOT NULL
	);`,
	// 6: the append-only history of loyalty points per character and issuing
	// corporation (taken_at is unix seconds). It follows the token like the
	// snapshot. The primary key doubles as the (character, corporation, time)
	// index and makes a pair's rows unique per instant. It is seeded with the
	// current snapshot so existing values are not lost.
	`CREATE TABLE loyalty_history (
		character_id   INTEGER NOT NULL REFERENCES tokens(character_id) ON DELETE CASCADE,
		corporation_id INTEGER NOT NULL,
		taken_at       INTEGER NOT NULL,
		points         INTEGER NOT NULL,
		PRIMARY KEY (character_id, corporation_id, taken_at)
	);
	INSERT OR IGNORE INTO loyalty_history (character_id, corporation_id, taken_at, points)
		SELECT character_id, corporation_id, fetched_at, points FROM loyalty_points;`,
}

// Store is a SQLite-backed wallet history.
type Store struct {
	db         *sql.DB
	backupPath string
}

// BackupPath is the backup Open wrote before migrating, or "" when it wrote none.
func (s *Store) BackupPath() string { return s.backupPath }

// Open opens (creating if needed) the database at path and applies pending
// migrations. Use ":memory:" for a private in-memory database.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	} else {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	existed := false
	if path != ":memory:" {
		if _, err := os.Stat(path); err == nil {
			existed = true
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if path == ":memory:" {
		// Each connection would otherwise get its own empty database.
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db}
	if existed {
		bak, err := backupBeforeMigrate(context.Background(), db, path)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		s.backupPath = bak
	}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("store: database schema version %d is newer than supported %d", current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		if err := s.applyMigration(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, v int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", v+1, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return fmt.Errorf("store: migration %d: %w", v+1, err)
	}
	// PRAGMA does not accept bound parameters.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
		return fmt.Errorf("store: set schema version %d: %w", v+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", v+1, err)
	}
	return nil
}

// UpsertWallet inserts the wallet or, if (kind, owner, division) already
// exists, updates its owner name. It returns the wallet id, which is stable.
func (s *Store) UpsertWallet(ctx context.Context, w Wallet) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO wallets (kind, owner_id, owner_name, division)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (kind, owner_id, division) DO UPDATE SET owner_name = excluded.owner_name
		RETURNING id`,
		string(w.Kind), w.OwnerID, w.OwnerName, w.Division).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert wallet: %w", err)
	}
	return id, nil
}

// SetLabel sets the user-chosen name of a wallet. The name is trimmed and must
// be 1-64 characters without control characters (ErrInvalidName otherwise).
func (s *Store) SetLabel(ctx context.Context, walletID int64, name string) error {
	return s.setName(ctx, "label", walletID, name)
}

// ClearLabel removes the user-chosen name of a wallet.
func (s *Store) ClearLabel(ctx context.Context, walletID int64) error {
	return s.writeName(ctx, "label", walletID, nil)
}

// SetESIName stores the name ESI reports for a wallet, with the same
// validation as SetLabel.
func (s *Store) SetESIName(ctx context.Context, walletID int64, name string) error {
	return s.setName(ctx, "esi_name", walletID, name)
}

// ClearESIName removes the stored ESI name of a wallet.
func (s *Store) ClearESIName(ctx context.Context, walletID int64) error {
	return s.writeName(ctx, "esi_name", walletID, nil)
}

func (s *Store) setName(ctx context.Context, column string, walletID int64, name string) error {
	name, err := validateName(name)
	if err != nil {
		return err
	}
	return s.writeName(ctx, column, walletID, name)
}

// writeName updates one of the fixed name columns; column is never user input.
func (s *Store) writeName(ctx context.Context, column string, walletID int64, value any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE wallets SET `+column+` = ? WHERE id = ?`, value, walletID)
	if err != nil {
		return fmt.Errorf("store: set %s: %w", column, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set %s: %w", column, err)
	}
	if n == 0 {
		return fmt.Errorf("store: wallet %d: %w", walletID, ErrNotFound)
	}
	return nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", fmt.Errorf("%w: must be 1-%d characters, got %d", ErrInvalidName, maxNameLen, n)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidName)
		}
	}
	return name, nil
}

// AddSnapshot records a polled balance. Repeating the same wallet and instant
// is a no-op.
func (s *Store) AddSnapshot(ctx context.Context, walletID int64, takenAt time.Time, cents int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO balances (wallet_id, taken_at, cents, source)
		VALUES (?, ?, ?, 'snapshot')`, walletID, takenAt.Unix(), cents)
	if err != nil {
		return fmt.Errorf("store: add snapshot: %w", err)
	}
	return nil
}

// AddJournalBalance records the running balance carried by a journal entry.
// Repeating the same entry id for a wallet is a no-op.
func (s *Store) AddJournalBalance(ctx context.Context, walletID, entryID int64, at time.Time, cents int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO balances (wallet_id, taken_at, cents, source, journal_ref)
		VALUES (?, ?, ?, 'journal', ?)`, walletID, at.Unix(), cents, entryID)
	if err != nil {
		return fmt.Errorf("store: add journal balance: %w", err)
	}
	return nil
}

// Wallets lists all wallets ordered by kind, owner id and division.
func (s *Store) Wallets(ctx context.Context) ([]Wallet, error) {
	return s.wallets(ctx, "", nil)
}

// wallets runs the wallet listing with an optional extra join (scope) and its
// arguments; scope is a fixed SQL fragment, never user input.
func (s *Store) wallets(ctx context.Context, scope string, args []any) ([]Wallet, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.kind, w.owner_id, w.owner_name, w.division, w.label, w.esi_name
		FROM wallets w `+scope+`
		ORDER BY w.kind, w.owner_id, w.division`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list wallets: %w", err)
	}
	defer rows.Close()
	var out []Wallet
	for rows.Next() {
		var w Wallet
		var kind string
		var label, esiName sql.NullString
		if err := rows.Scan(&w.ID, &kind, &w.OwnerID, &w.OwnerName, &w.Division, &label, &esiName); err != nil {
			return nil, fmt.Errorf("store: scan wallet: %w", err)
		}
		w.Kind = Kind(kind)
		w.Label, w.ESIName = label.String, esiName.String
		out = append(out, w)
	}
	return out, rows.Err()
}

// LatestBalances returns, for every wallet that has at least one balance, its
// most recent balance across snapshots and journal entries, in Wallets order.
func (s *Store) LatestBalances(ctx context.Context) ([]WalletBalance, error) {
	return s.latestBalances(ctx, "", nil)
}

// latestBalances is LatestBalances with an optional extra join (scope).
func (s *Store) latestBalances(ctx context.Context, scope string, args []any) ([]WalletBalance, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.kind, w.owner_id, w.owner_name, w.division, w.label, w.esi_name, b.taken_at, b.cents
		FROM wallets w `+scope+`
		JOIN balances b ON b.id = (
			SELECT id FROM balances
			WHERE wallet_id = w.id
			ORDER BY taken_at DESC, id DESC LIMIT 1)
		ORDER BY w.kind, w.owner_id, w.division`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: latest balances: %w", err)
	}
	defer rows.Close()
	var out []WalletBalance
	for rows.Next() {
		var wb WalletBalance
		var kind string
		var at int64
		var label, esiName sql.NullString
		if err := rows.Scan(&wb.Wallet.ID, &kind, &wb.Wallet.OwnerID, &wb.Wallet.OwnerName,
			&wb.Wallet.Division, &label, &esiName, &at, &wb.Cents); err != nil {
			return nil, fmt.Errorf("store: scan latest balance: %w", err)
		}
		wb.Wallet.Kind = Kind(kind)
		wb.Wallet.Label, wb.Wallet.ESIName = label.String, esiName.String
		wb.At = time.Unix(at, 0).UTC()
		out = append(out, wb)
	}
	return out, rows.Err()
}

// Series returns balance points ordered by wallet id and then time.
func (s *Store) Series(ctx context.Context, f SeriesFilter) ([]Point, error) {
	return s.series(ctx, nil, f)
}

// series is Series, restricted to the wallets linked to userID when it is not
// nil. The restriction is ANDed with the filter, so a filter naming a wallet
// the user cannot see yields nothing.
func (s *Store) series(ctx context.Context, userID *int64, f SeriesFilter) ([]Point, error) {
	var (
		where []string
		args  []any
	)
	if userID != nil {
		where = append(where, "wallet_id IN (SELECT wallet_id FROM user_wallets WHERE user_id = ?)")
		args = append(args, *userID)
	}
	if len(f.WalletIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.WalletIDs)), ",")
		where = append(where, "wallet_id IN ("+marks+")")
		for _, id := range f.WalletIDs {
			args = append(args, id)
		}
	}
	if !f.From.IsZero() {
		where = append(where, "taken_at >= ?")
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		where = append(where, "taken_at <= ?")
		args = append(args, f.To.Unix())
	}
	q := "SELECT wallet_id, taken_at, cents FROM balances"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY wallet_id, taken_at, id"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: series: %w", err)
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		var at int64
		if err := rows.Scan(&p.WalletID, &at, &p.Cents); err != nil {
			return nil, fmt.Errorf("store: scan point: %w", err)
		}
		p.At = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ISKToCents converts an ISK amount to integer cents, rounding to the nearest
// cent (halves away from zero).
func ISKToCents(isk float64) int64 { return int64(math.Round(isk * 100)) }

// CentsToISK converts integer cents to an ISK amount for display or charting.
func CentsToISK(cents int64) float64 { return float64(cents) / 100 }
