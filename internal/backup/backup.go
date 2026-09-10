package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

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

// rollbackSuffix names the file the previous database is parked under while the
// replacement is proven to open. It lives beside the database deliberately: the
// rename that moves it must be on one filesystem to be atomic.
const rollbackSuffix = ".rollback"

// Whether the previous database has been moved to the rollback path yet, named
// so the failure sites read as English rather than as a bare true/false.
const (
	nothingMoved   = false
	originalParked = true
)

// The three outcomes of a restore, which need three different responses and used
// to be one error value.
//
// Callers must be able to tell "your file was refused and nothing changed" from
// "the restore failed but your database is still serving" from "there is no
// working database now". The handler used to print "the uploaded file may be
// invalid or corrupted" for all three — wrong for two of them, since the file
// had already passed Validate, and it sends the operator to re-export and
// re-upload against a process that can no longer answer.
var (
	// ErrRejected: refused before the live database was touched.
	ErrRejected = errors.New("backup: restore rejected")
	// ErrRolledBack: the swap failed and the previous database is back in service.
	ErrRolledBack = errors.New("backup: restore rolled back")
	// ErrUnavailable: the swap failed and so did the recovery. The process has no
	// working database and only a restart will fix it.
	ErrUnavailable = errors.New("backup: database unavailable")
)

// Store is what Import needs to swap the database file underneath a running
// process: the handle to close, and the way to open the replacement.
//
// Declared here rather than taking *store.Store because those two methods are
// the whole dependency. Naming them drops store from this package's non-test
// build entirely — only the tests still import it, since swapping a database
// needs a real one — and the pair is small enough to read as what it is, which
// matters when the operation is "replace the database".
type Store interface {
	DB() *sql.DB
	Reopen(path string) error
}

// Import replaces the live database with an uploaded one, and guarantees that a
// failure leaves a working database behind.
//
// The old implementation closed the live handle and then had three returns
// before it reopened anything. store.Reopen only assigns on success, so any of
// them left the process holding a closed handle: every route 500s until someone
// restarts the container, while /healthz still reported ok. The Reopen path was
// worse than downtime — the rename had already overwritten the live database, so
// there was no original left to go back to.
//
// So the previous database is parked under rollbackSuffix and only deleted once
// the replacement has actually opened. Every failure after the handle is closed
// funnels through rollBack, and Import cannot return without either a working
// database or ErrUnavailable.
func Import(s Store, uploadedPath, dbPath string) error {
	if err := Validate(uploadedPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}

	// Checkpoint before anything is touched, and abort if it fails.
	//
	// This used to log "recent unflushed data may be lost" and carry on to delete
	// the WAL, which is what actually lost it. It matters more now: rolling back
	// to a database whose unflushed frames were discarded would be a quiet data
	// loss dressed as a recovery. Aborting here costs a refused restore and
	// nothing else, because nothing has changed yet.
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("%w: cannot flush the current database to disk, so it "+
			"cannot be safely set aside: %w", ErrRejected, err)
	}

	// Close before any filesystem operation so no write races the rename.
	if err := s.DB().Close(); err != nil {
		log.Printf("import: warning: close old db: %v", err)
	}

	// rollBack puts the previous database back and returns the right sentinel.
	// One implementation rather than one per failure site: four copies of "undo
	// it" is how three of them end up subtly different.
	//
	// Not named recover — that is a builtin, and shadowing it inside a function
	// that may one day want a deferred recover is a trap for the next reader.
	rollBack := func(cause error, parked bool) error {
		if parked {
			// The replacement is in place and did not work. Remove it before
			// moving the original back, or the rename has nowhere to land.
			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				log.Printf("import: rollback: removing failed replacement: %v", err)
			}
			if err := os.Rename(dbPath+rollbackSuffix, dbPath); err != nil {
				return fmt.Errorf("%w: restore failed (%w) and the previous database "+
					"could not be moved back from %s: %w",
					ErrUnavailable, cause, dbPath+rollbackSuffix, err)
			}
		}
		if err := s.Reopen(dbPath); err != nil {
			return fmt.Errorf("%w: restore failed (%w) and the previous database "+
				"could not be reopened: %w", ErrUnavailable, cause, err)
		}
		return fmt.Errorf("%w: %w", ErrRolledBack, cause)
	}

	// Stale WAL and SHM belong to the database being replaced. The WAL must go,
	// or SQLite replays its frames over the new file; SHM is reconstructable.
	walPath := dbPath + "-wal"
	if _, statErr := os.Stat(walPath); statErr == nil {
		if err := os.Remove(walPath); err != nil {
			return rollBack(fmt.Errorf("cannot remove the old WAL file, which would be "+
				"replayed over the restored database: %w", err), nothingMoved)
		}
	}
	if err := os.Remove(dbPath + "-shm"); err != nil && !os.IsNotExist(err) {
		log.Printf("import: warning: could not remove SHM file: %v", err)
	}

	// Park the previous database rather than letting the next rename destroy it.
	if err := os.Rename(dbPath, dbPath+rollbackSuffix); err != nil {
		return rollBack(fmt.Errorf("cannot set the current database aside: %w", err), nothingMoved)
	}

	// The swap. Same filesystem by construction — the handler writes the upload
	// beside the database precisely so this cannot fail with EXDEV.
	if err := os.Rename(uploadedPath, dbPath); err != nil {
		return rollBack(fmt.Errorf("cannot move the uploaded database into place: %w", err), originalParked)
	}

	if err := s.Reopen(dbPath); err != nil {
		return rollBack(fmt.Errorf("the restored database could not be opened: %w", err), originalParked)
	}

	// Only now is the previous database genuinely disposable. Left behind it is a
	// full second copy of the database sitting next to the live one, doubling
	// disk use on every restore.
	if err := os.Remove(dbPath + rollbackSuffix); err != nil && !os.IsNotExist(err) {
		log.Printf("import: warning: could not remove %s: %v", dbPath+rollbackSuffix, err)
	}
	return nil
}
