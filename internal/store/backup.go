package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// backupBeforeMigrate copies the database to <path>.bak-v<N>, N being its
// current user_version, when migrations are about to run. It returns the
// backup path, or "" when nothing was written: the database is fresh
// (version 0), already current, newer than supported (migrate refuses it) or
// already has a backup of that version, which is never overwritten.
//
// The copy is made with VACUUM INTO, which yields a consistent snapshot even
// while a WAL file exists, into a private temporary file that is renamed into
// place only when complete.
func backupBeforeMigrate(ctx context.Context, db *sql.DB, path string) (string, error) {
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return "", fmt.Errorf("store: read schema version: %w", err)
	}
	if current == 0 || current >= len(migrations) {
		return "", nil
	}
	bak := fmt.Sprintf("%s.bak-v%d", path, current)
	if fi, err := os.Lstat(bak); err == nil {
		if fi.Mode().IsRegular() {
			return "", nil
		}
		return "", fmt.Errorf("store: backup %s exists and is not a regular file", bak)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("store: check backup %s: %w", bak, err)
	}

	tmp := bak + ".tmp"
	_ = os.Remove(tmp) // leftover of an interrupted backup
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("store: create backup before migrating: %w", err)
	}
	_ = f.Close()
	// VACUUM INTO accepts an empty existing file; the name is a quoted literal.
	quoted := "'" + strings.ReplaceAll(tmp, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("store: back up database before migrating: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("store: restrict backup permissions: %w", err)
	}
	if err := os.Rename(tmp, bak); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("store: finish backup before migrating: %w", err)
	}
	return bak, nil
}
