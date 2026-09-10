package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The assumptions Import makes about SQLite and the filesystem, written down as
// tests.
//
// Every defect in the restore path this session came from getting one of these
// wrong while the code read as though it were right — and none of them is
// visible at a package boundary. internal/backup imports nothing from this
// project; sealing it bought a small file to review and told us nothing about
// what os.Rename does.
//
// Reading the docs is not the same as pinning the behaviour. A four-line script
// that printed the checkpoint's actual return values found a live data-loss bug
// in seconds, after the code had been reviewed and committed. That script is
// this file: assumptions belong in tests that fail when the assumption is wrong,
// the same argument as the decision golden, aimed at dependencies rather than at
// our own behaviour.
//
// If one of these fails after a dependency upgrade, Import needs re-reading —
// that is the whole point.

// TestCheckpointReportsContentionInARowNotAnError is the assumption that cost
// the most.
//
// Import must know whether the write-ahead log was fully flushed before it
// deletes it. The obvious way to ask — Exec the pragma and check the error — is
// wrong: SQLite reports contention as a value in the result row and returns no
// error at all. So the guard passed in exactly the case it existed for, and the
// code went on to delete a log still holding un-checkpointed frames.
func TestCheckpointReportsContentionInARowNotAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	dsn := path + "?_pragma=journal_mode(WAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A second connection holding a read snapshot, then a commit after it. That
	// is one admin page load overlapping one form submission — ordinary traffic.
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("read in tx: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := db.Exec("INSERT INTO t VALUES (?)", i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Exec: no error, even though the checkpoint could not complete.
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("Exec reported an error (%v).\nIf SQLite has started reporting "+
			"contention as an error, Import's row check is no longer the only way to "+
			"detect it — but read the row anyway, because this test is why it does.", err)
	}

	// The row is where the truth is.
	var busy, logFrames, flushed sql.NullInt64
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &flushed); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if flushed.Int64 >= logFrames.Int64 {
		t.Fatalf("expected un-flushed frames with a reader holding a snapshot, got "+
			"busy=%d log=%d checkpointed=%d.\nImport refuses a restore when "+
			"checkpointed < log; if that can no longer happen, the guard is dead code "+
			"and the reason for it needs revisiting.",
			busy.Int64, logFrames.Int64, flushed.Int64)
	}
	t.Logf("under contention: busy=%d log=%d checkpointed=%d, Exec err=nil",
		busy.Int64, logFrames.Int64, flushed.Int64)
}

// TestCheckpointOnANonWALDatabaseReportsMinusOne pins the value Import treats as
// "nothing to flush". Reading -1 as "no frames written" would refuse every
// restore on a non-WAL database.
func TestCheckpointOnANonWALDatabaseReportsMinusOne(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	var busy, logFrames, flushed sql.NullInt64
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &flushed); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if logFrames.Int64 != -1 || flushed.Int64 != -1 {
		t.Errorf("non-WAL checkpoint reported log=%d checkpointed=%d, want -1/-1.\n"+
			"Import skips its flush check when log is not positive; a different "+
			"sentinel here changes which restores it refuses.", logFrames.Int64, flushed.Int64)
	}
}

// TestOpeningAMissingDatabaseCreatesIt is why rollBack stats the file rather
// than trusting Reopen.
//
// Import used to treat a successful Reopen as proof the previous database was
// back in place. It is not: SQLite creates the file, so reopening an absent path
// manufactures an empty database and returns nil — and the operator was told
// their database was "unchanged and still in use" while it served zero rows.
func TestOpeningAMissingDatabaseCreatesIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping on a missing file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("this driver did not create the file on open (%v); Import's stat "+
			"check is then belt-and-braces rather than load-bearing", err)
	}
	t.Log("opening a missing database created it — openability proves nothing " +
		"about identity, so rollBack stats the path before claiming a rollback")
}

// TestRenameOverwritesAnExistingFile is the contract behind two decisions.
//
// It is why Import must refuse when a parked database already exists — nothing
// would otherwise stop the park silently destroying it — and why rollBack does
// NOT remove the failed replacement first. An earlier comment claimed the rename
// "has nowhere to land"; that removal opened a window with no database at all.
func TestRenameOverwritesAnExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(src, dst); err != nil {
		t.Fatalf("rename over an existing file failed: %v.\nIf this platform "+
			"refuses, rollBack must remove the destination first — which is the "+
			"opposite of what it does now, deliberately.", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("destination holds %q, want %q", got, "new")
	}
}

// TestRenameOntoADirectoryFails records the asymmetry, because a test once
// relied on it by accident: blocking a path with a directory makes a rename
// fail, while blocking it with a file succeeds silently and destroys it. That
// difference hid the data-loss bug this file's neighbour now guards.
func TestRenameOntoADirectoryFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err == nil {
		t.Error("renaming a file onto a directory succeeded; a test that injects " +
			"failure that way is no longer injecting anything")
	}
}

// TestPingFailsOnAClosedHandle is the assumption /healthz rests on: a closed
// database must be reportable. It is the narrow thing Ping does prove — note it
// does NOT prove the database is readable, which is a separate, recorded gap.
func TestPingFailsOnAClosedHandle(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping on an open handle: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err = db.PingContext(context.Background())
	if err == nil {
		t.Fatal("PingContext succeeded on a closed handle, so /healthz cannot " +
			"detect the state it was added for")
	}
	if !errors.Is(err, sql.ErrConnDone) && err.Error() != "sql: database is closed" {
		t.Logf("closed-handle ping returned %v (not the expected sentinel, but it "+
			"does fail, which is what /healthz needs)", err)
	}
}
