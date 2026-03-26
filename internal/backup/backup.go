package backup

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/youruser/dsforms/internal/store"
	_ "modernc.org/sqlite"
)

// Export creates a snapshot of the DB using VACUUM INTO.
// Returns the path to the temp file. Caller must delete it.
func Export(db *sql.DB) (string, error) {
	tmpFile, err := os.CreateTemp("", "dsforms-backup-*.db")
	if err != nil {
		return "", fmt.Errorf("export: create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// Remove the file first — VACUUM INTO requires the destination to not exist.
	if err := os.Remove(tmpPath); err != nil {
		return "", fmt.Errorf("export: remove temp file before vacuum: %w", err)
	}

	_, err = db.Exec("VACUUM INTO ?", tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("export: vacuum into: %w", err)
	}
	return tmpPath, nil
}

// Validate checks that a file is a valid DSForms SQLite database.
func Validate(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("validate: open: %w", err)
	}
	defer db.Close()

	// Integrity check
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("validate: integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("validate: integrity check failed: %s", result)
	}

	// Required tables must all be present
	for _, table := range []string{"users", "forms", "submissions"} {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			return fmt.Errorf("validate: missing required table %q", table)
		}
	}

	return nil
}

// Import validates the file, atomically renames it to dbPath, and reopens the store.
func Import(s *store.Store, uploadedPath, dbPath string) error {
	if err := Validate(uploadedPath); err != nil {
		return fmt.Errorf("import: %w", err)
	}

	// Checkpoint the live DB so all WAL frames are written to the main file.
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("import: WAL checkpoint failed (recent unflushed data may be lost): %v", err)
	}

	// Close the old connection before filesystem operations so no writes race
	// with the rename.
	if err := s.DB().Close(); err != nil {
		log.Printf("import: warning: close old db: %v", err)
	}

	// Remove stale WAL file — if it exists and cannot be removed, abort to
	// prevent SQLite replaying old WAL frames over the new database.
	walPath := dbPath + "-wal"
	if _, statErr := os.Stat(walPath); statErr == nil {
		if err := os.Remove(walPath); err != nil {
			return fmt.Errorf("import: cannot remove old WAL file (aborting to prevent corruption): %w", err)
		}
	}
	// SHM is reconstructable — best-effort removal only.
	if err := os.Remove(dbPath + "-shm"); err != nil && !os.IsNotExist(err) {
		log.Printf("import: warning: could not remove SHM file: %v", err)
	}

	// Atomic swap: rename the validated uploaded file to the live DB path.
	if err := os.Rename(uploadedPath, dbPath); err != nil {
		return fmt.Errorf("import: rename: %w", err)
	}

	// Reopen opens a fresh connection to the new file (old is already closed).
	if err := s.Reopen(dbPath); err != nil {
		return fmt.Errorf("import: reopen: %w", err)
	}

	return nil
}
