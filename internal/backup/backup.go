package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// credentialTables are the tables a snapshot is stripped of before it leaves
// the instance.
//
// api_tokens holds long-lived MCP credentials. They were in every snapshot
// until it was noticed that restoring one *resurrects tokens revoked since it
// was taken* — and revocation is a security action, so a restore silently
// undoing it is the wrong direction to fail in, the more so because whoever was
// revoked may still be holding the string. A backup is also a file that gets
// copied to laptops and object stores, and it had no business carrying
// credential material it does not need.
//
// sessions are here for the same reason, and were left out of the first version
// of this on reasoning that did not survive being tested. The claim was that
// dropping them would sign every operator out of a restored instance. Both
// halves were wrong: a restore signs out whoever performs it regardless, since
// their session postdates the snapshot, and keeping them brings back sessions
// that were deliberately destroyed — a logout, a password change, the cascade
// from a deleted user. internal/handler/auth.go calls that last one a guarantee
// the product makes, and a restore was quietly breaking it.
//
// The cost is real and documented: a restore no longer brings live tokens back
// either, so they are re-minted after a recovery, and everyone signs in again.
// Clients and people visibly stopping is the better failure than a revoked
// credential quietly working again.
var credentialTables = []string{"api_tokens", "sessions"}

// Export creates a snapshot of the DB using VACUUM INTO, with the credential
// tables emptied.
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

	// Stripped from the copy rather than excluded from the VACUUM, because
	// VACUUM INTO takes the whole database or nothing. The live one is never
	// touched: an operator taking a backup must not thereby revoke their own
	// tokens.
	if err := stripCredentials(tmpPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

// stripCredentials empties the credential tables in a snapshot and rewrites the
// file so the rows are gone rather than merely unlinked.
//
// The VACUUM is the load-bearing half. SQLite frees a deleted row's page
// instead of rewriting it, so without it the digests stay in the file and a
// grep finds them — this function would have looked like it worked while
// shipping exactly what it was written to remove. Pinned by
// TestDeleteAloneLeavesTheBytesInTheFile.
func stripCredentials(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("export: open snapshot to strip credentials: %w", err)
	}
	defer db.Close()

	for _, table := range credentialTables {
		// The names are this package's own constants, never input.
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("export: clearing %s from the snapshot: %w", table, err)
		}
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("export: rewriting the snapshot after clearing credentials: %w", err)
	}
	return nil
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

// StagedUploadPattern matches the temp files the handler stages beside the
// database while a restore is in flight.
const StagedUploadPattern = "dsforms-import-*.db"

// SweepStagedUploads removes leftover restore uploads from dir.
//
// The upload is staged next to the database so the final rename cannot cross a
// filesystem. The cost is that a process killed between staging and the swap
// leaves the file there — up to the upload limit, on the data volume, with
// nothing to clean it up. Several interrupted restores quietly fill the disk
// that the database needs.
//
// Called at startup, where "in flight" cannot be true: nothing has begun a
// restore yet, so every match is certainly stale. Doing it on a timer instead
// would have to distinguish a live staging file from an abandoned one.
//
// Returns the number removed. A failure to remove one is not fatal — a leftover
// file wastes space, and refusing to boot over it would be the worse trade.
func SweepStagedUploads(dir string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, StagedUploadPattern))
	if err != nil {
		return 0, fmt.Errorf("sweep staged uploads: %w", err)
	}
	var removed int
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			log.Printf("sweep staged uploads: %s: %v", m, err)
			continue
		}
		removed++
	}
	return removed, nil
}

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

// importMu serializes restores.
//
// Two overlapping Imports both close the live handle and then contend for one
// fixed park path: the second can rename the first's parked database away, or
// swap its upload into place between the first's swap and reopen. Every outcome
// is some interleaving of two half-finished swaps over one file.
//
// Admin-only, so the reach is small, but it costs a mutex to remove entirely and
// there is no sensible concurrent behaviour to preserve — a second restore while
// the first is mid-swap should wait, not race.
var importMu sync.Mutex

// Import replaces the live database with an uploaded one, and guarantees that a
// failure leaves a working database behind.
//
// The old implementation closed the live handle and then returned from several
// failure sites before it reopened anything. store.Reopen only assigns on
// success, so any of them left the process holding a closed handle: every route
// 500s until someone restarts the container, while /healthz still reported ok.
// The Reopen path was worse than downtime — the rename had already overwritten
// the live database, so there was no original left to go back to.
//
// So the previous database is parked under rollbackSuffix and only deleted once
// the replacement has actually opened. Every failure after the handle is closed
// funnels through rollBack, and Import cannot return without either a working
// database or ErrUnavailable.
func Import(s Store, uploadedPath, dbPath string) error {
	importMu.Lock()
	defer importMu.Unlock()

	if err := Validate(uploadedPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}

	// Stripped again on the way in, not only on the way out.
	//
	// Export is the wrong place for this to live alone: a snapshot taken before
	// that existed still carries credentials, and operations.md explicitly tells
	// people they can drop in a raw copy of the database file. Either would walk
	// a revoked token or a logged-out session straight back into a live
	// instance, past a defence that only ever ran on files this build wrote.
	//
	// It is the uploaded file being modified, before anything is swapped, so a
	// rejected restore leaves the live database untouched as before.
	if err := stripCredentials(uploadedPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}

	// Refuse if a previous restore left its parked database behind.
	//
	// The park below is a rename, and rename overwrites its destination, so
	// without this check the next restore silently destroys that file. That is
	// the whole disaster: a process killed between the park and the swap leaves
	// the real database at .rollback and nothing at dbPath; the container
	// restarts, store.New creates an empty database and re-seeds admin/admin, and
	// the operator — seeing an empty instance — restores a backup, which renames
	// that empty database over the last copy of their data.
	//
	// Nothing else in the codebase looks at this file, so refusing here is what
	// makes it survivable.
	if _, err := os.Stat(dbPath + rollbackSuffix); err == nil {
		return fmt.Errorf("%w: %s already exists, which means a previous restore did "+
			"not finish. That file may be your database — move it somewhere safe (or "+
			"back to %s) before restoring again",
			ErrRejected, dbPath+rollbackSuffix, dbPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: cannot check for a leftover %s: %w",
			ErrRejected, dbPath+rollbackSuffix, err)
	}

	// Flush the write-ahead log into the main database before anything is
	// touched, and refuse the restore if it cannot be flushed completely.
	//
	// The swap removes the WAL, because SQLite would otherwise replay the old
	// database's frames over the restored one. That is only safe once every frame
	// is in the main file. This used to log "recent unflushed data may be lost"
	// and delete the WAL anyway, which is what lost it; with a rollback in play it
	// would be worse, restoring a database missing its own most recent writes.
	//
	// The result row is what has to be checked, not the error. wal_checkpoint does
	// not report contention as a failure: a reader holding a snapshot returns
	// (busy=1, log=53, checkpointed=52) with a nil error, so an Exec-and-check-err
	// guard passes in precisely the case it exists to catch. One concurrent admin
	// request is enough to produce it.
	var busy, walFrames, flushed sql.NullInt64
	if err := s.DB().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &walFrames, &flushed); err != nil {
		return fmt.Errorf("%w: cannot flush the current database to disk, so it "+
			"cannot be safely set aside: %w", ErrRejected, err)
	}
	// A database not in WAL mode reports -1 for both counts: nothing to flush.
	if walFrames.Int64 > 0 && flushed.Int64 < walFrames.Int64 {
		return fmt.Errorf("%w: %d of %d write-ahead frames could not be written to "+
			"the database file, most likely because another request is holding a read "+
			"snapshot. Removing the log now would discard those writes; retry the "+
			"restore", ErrRejected, walFrames.Int64-flushed.Int64, walFrames.Int64)
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
		parkPath := dbPath + rollbackSuffix
		if parked {
			// One atomic rename, straight over the failed replacement.
			//
			// An earlier version removed dbPath first, on the theory that the
			// rename needed somewhere to land. It does not — rename replaces its
			// destination — and the removal opened the worst window in the
			// function: between it and a rename that then failed, there was no
			// database at dbPath at all and the operator's only copy was a file
			// nothing mentions.
			if err := os.Rename(parkPath, dbPath); err != nil {
				return fmt.Errorf("%w: restore failed (%w) and the previous database "+
					"could not be moved back: it is still at %s. Do not restart until "+
					"it has been moved back to %s — a restart with no database there "+
					"creates an empty one and re-enables the default admin login: %w",
					ErrUnavailable, cause, parkPath, dbPath, err)
			}
		}

		// Reopen alone is not evidence the previous database is back. SQLite
		// creates the file if it is missing, so Reopen on an absent dbPath
		// manufactures an empty database, returns nil, and this would report a
		// successful rollback while serving zero forms and zero users — nobody
		// could even log in to notice. Verified before trusting it.
		if _, err := os.Stat(dbPath); err != nil {
			return fmt.Errorf("%w: restore failed (%w) and there is no database at %s. "+
				"Check for %s before restarting, since starting with no database "+
				"creates an empty one and re-enables the default admin login: %w",
				ErrUnavailable, cause, dbPath, parkPath, err)
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
