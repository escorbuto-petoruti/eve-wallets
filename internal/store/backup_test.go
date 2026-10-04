package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeDBAtVersion creates a database holding the first n migrations, as an
// older binary would have left it, with one marker row in a scratch table.
func makeDBAtVersion(t *testing.T, path string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE marker (v TEXT); INSERT INTO marker VALUES ('before')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, n)); err != nil {
		t.Fatal(err)
	}
}

func readVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenBacksUpBeforeMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")
	makeDBAtVersion(t, path, 2)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bak := path + ".bak-v2"
	if got := s.BackupPath(); got != bak {
		t.Errorf("BackupPath = %q, want %q", got, bak)
	}
	info, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", info.Mode().Perm())
	}
	if v := readVersion(t, bak); v != 2 {
		t.Errorf("backup user_version = %d, want 2", v)
	}
	db, err := sql.Open("sqlite", "file:"+bak+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var marker string
	if err := db.QueryRow(`SELECT v FROM marker`).Scan(&marker); err != nil || marker != "before" {
		t.Errorf("backup contents: marker = %q, err = %v", marker, err)
	}
	if v := readVersion(t, path); v != len(migrations) {
		t.Errorf("database user_version = %d, want %d", v, len(migrations))
	}
	if left, _ := filepath.Glob(path + ".bak-*.tmp*"); len(left) != 0 {
		t.Errorf("temporary backup files left: %v", left)
	}
}

func TestOpenNeverOverwritesExistingBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")
	makeDBAtVersion(t, path, 2)
	bak := path + ".bak-v2"
	if err := os.WriteFile(bak, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, _ := os.ReadFile(bak)
	if string(got) != "precious" {
		t.Errorf("existing backup was overwritten: %q", got)
	}
	if s.BackupPath() != "" {
		t.Errorf("BackupPath = %q, want empty when nothing was written", s.BackupPath())
	}
}

func TestOpenNoBackupWhenFreshOrCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wallets.db")

	s, err := Open(path) // no file yet
	if err != nil {
		t.Fatal(err)
	}
	if s.BackupPath() != "" {
		t.Errorf("fresh database: BackupPath = %q", s.BackupPath())
	}
	_ = s.Close()

	s, err = Open(path) // already current
	if err != nil {
		t.Fatal(err)
	}
	if s.BackupPath() != "" {
		t.Errorf("current database: BackupPath = %q", s.BackupPath())
	}
	_ = s.Close()

	empty := filepath.Join(dir, "empty.db") // existing file, user_version 0
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if baks, _ := filepath.Glob(filepath.Join(dir, "*.bak-*")); len(baks) != 0 {
		t.Errorf("unexpected backups: %v", baks)
	}
}

func TestOpenBackupFailureLeavesDatabaseUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")
	makeDBAtVersion(t, path, 2)
	// A directory squatting on the backup name cannot be a backup.
	if err := os.Mkdir(path+".bak-v2", 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded without a backup")
	}
	if v := readVersion(t, path); v != 2 {
		t.Errorf("database was migrated to %d despite the failed backup", v)
	}
}

func TestOpenNewerDatabaseStillRefusedWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")
	makeDBAtVersion(t, path, len(migrations))
	db, _ := sql.Open("sqlite", "file:"+path)
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("a newer database must be refused")
	}
	if baks, _ := filepath.Glob(path + ".bak-*"); len(baks) != 0 {
		t.Errorf("unexpected backups: %v", baks)
	}
}
