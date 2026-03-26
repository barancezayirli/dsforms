package backup

import (
	"database/sql"
	"fmt"
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
	// Ignore error — we will remove the WAL files below regardless.
	_, _ = s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	// Remove the stale WAL and SHM files *before* renaming the new DB in place.
	// If we leave them, SQLite would replay the old WAL on top of the new file
	// when the store is reopened, corrupting the import.
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	// Atomic swap: rename the validated uploaded file to the live DB path.
	if err := os.Rename(uploadedPath, dbPath); err != nil {
		return fmt.Errorf("import: rename: %w", err)
	}

	// Reopen closes the old connection and opens the new file.
	if err := s.Reopen(dbPath); err != nil {
		return fmt.Errorf("import: reopen: %w", err)
	}

	return nil
}
