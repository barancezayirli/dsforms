package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
	_ "modernc.org/sqlite"
)

// flakyStore fails Reopen a set number of times, then behaves normally.
//
// Import's failure paths were unreachable from a test while it took
// *store.Store: there was no way to make a real store fail to reopen without
// breaking the filesystem underneath it. Store is two methods, so a fake is
// four lines — the narrowing paying for itself.
type flakyStore struct {
	inner       *store.Store
	failReopens int
	reopens     int
}

func (f *flakyStore) DB() *sql.DB { return f.inner.DB() }

func (f *flakyStore) Reopen(path string) error {
	f.reopens++
	if f.reopens <= f.failReopens {
		return errors.New("simulated reopen failure")
	}
	return f.inner.Reopen(path)
}

// vanishingUploadStore removes a file the first time DB() is called.
//
// The only seams into Import are DB() and Reopen(), and both DB() calls happen
// before the park — so deleting the upload there makes the *swap* rename fail
// with the original already parked. That is the one branch that actually renames
// the previous database back, and nothing else reaches it: making the rename
// fail through the filesystem breaks Validate first, because integrity_check
// needs to write.
type vanishingUploadStore struct {
	inner  *store.Store
	remove string
	calls  int
}

func (v *vanishingUploadStore) DB() *sql.DB {
	v.calls++
	if v.calls == 1 && v.remove != "" {
		if err := os.Remove(v.remove); err != nil {
			panic("vanishingUploadStore: " + err.Error())
		}
	}
	return v.inner.DB()
}

func (v *vanishingUploadStore) Reopen(path string) error { return v.inner.Reopen(path) }

// sabotagingStore fails Reopen and, on the way, removes the parked database.
//
// In the Reopen-failure path the order is swap, Reopen, then rename-back — so
// failing Reopen is the one seam that can reach in before the rename-back and
// take the parked file away. That branch is where the operator is told their
// database could not be restored to its place, and it is otherwise unreachable
// without a filesystem fault injector.
type sabotagingStore struct {
	inner    *store.Store
	parkPath string
}

func (b *sabotagingStore) DB() *sql.DB { return b.inner.DB() }

func (b *sabotagingStore) Reopen(string) error {
	_ = os.Remove(b.parkPath)
	return errors.New("simulated reopen failure")
}

// slowReopenStore holds the first Reopen open until released.
//
// Reopen is the last step of a swap and the only slow one, so pausing there
// parks a restore with the previous database already renamed to .rollback and
// the replacement already in place at dbPath. A second restore entering during
// that window is exactly the interleaving the mutex exists to prevent, and
// nothing else in Import is slow enough to produce it reliably — two plain
// concurrent calls simply finish one after the other, which is why the first
// version of this test passed with the mutex removed.
type slowReopenStore struct {
	inner   *store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *slowReopenStore) DB() *sql.DB { return b.inner.DB() }

func (b *slowReopenStore) Reopen(path string) error {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.inner.Reopen(path)
}

// restoreFixture builds a live store holding one form, plus a valid backup file
// holding a different one, so a successful restore is visibly distinguishable
// from a rolled-back one.
func restoreFixture(t *testing.T) (live *store.Store, dbPath, uploadPath string) {
	t.Helper()

	dir := t.TempDir()
	dbPath = filepath.Join(dir, "live.db")
	live, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := live.CreateForm(store.Form{ID: "f1", Name: "Original", EmailTo: "a@b.com"}); err != nil {
		t.Fatalf("seeding live store: %v", err)
	}

	otherDir := t.TempDir()
	otherPath := filepath.Join(otherDir, "other.db")
	other, err := store.New(otherPath)
	if err != nil {
		t.Fatalf("store.New for backup: %v", err)
	}
	if err := other.CreateForm(store.Form{ID: "f2", Name: "Restored", EmailTo: "b@c.com"}); err != nil {
		t.Fatalf("seeding backup store: %v", err)
	}
	other.Close()

	// The upload lands beside the live database, which is what the handler now
	// does and what keeps the swap a same-filesystem rename.
	uploadPath = filepath.Join(dir, "upload.db")
	data, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatalf("reading backup file: %v", err)
	}
	if err := os.WriteFile(uploadPath, data, 0o644); err != nil {
		t.Fatalf("writing upload: %v", err)
	}
	return live, dbPath, uploadPath
}

// seedBackupFile writes a valid dsforms database, holding a distinguishable
// form, to path.
func seedBackupFile(t *testing.T, path string, formName ...string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "seed.db")
	s, err := store.New(src)
	if err != nil {
		t.Fatalf("store.New for backup: %v", err)
	}
	name := "Restored"
	if len(formName) > 0 {
		name = formName[0]
	}
	if err := s.CreateForm(store.Form{ID: "f2", Name: name, EmailTo: "b@c.com"}); err != nil {
		t.Fatalf("seeding backup store: %v", err)
	}
	s.Close()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading backup file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing backup file: %v", err)
	}
}

// formNames is the assertion both outcomes turn on: which database is actually
// being served after Import returns.
func formNames(t *testing.T, s *store.Store) []string {
	t.Helper()
	forms, err := s.ListForms(store.AllForms())
	if err != nil {
		t.Fatalf("the store is not serving after Import returned: %v", err)
	}
	var out []string
	for _, f := range forms {
		out = append(out, f.Name)
	}
	return out
}

func assertNoRollbackFile(t *testing.T, dbPath string) {
	t.Helper()
	if _, err := os.Stat(dbPath + rollbackSuffix); !os.IsNotExist(err) {
		t.Errorf("%s%s still exists; a half-finished restore left the previous "+
			"database lying beside the live one", dbPath, rollbackSuffix)
	}
}

// TestImportRollsBackWhenReopenFails is the headline case, and the one that was
// data loss rather than downtime.
//
// By the time Reopen is called the rename has already overwritten the live
// database, so a failure here used to leave the process holding a closed handle
// AND no original to go back to. Every route 500s until a restart, while
// /healthz reports ok and the operator is told their file was corrupt.
func TestImportRollsBackWhenReopenFails(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)
	s := &flakyStore{inner: live, failReopens: 1}

	err := Import(s, uploadPath, dbPath)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("Import returned %v, want ErrRolledBack", err)
	}

	// The point of the whole change: the process is still serving, and serving
	// the database it had before the operator tried to replace it.
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("after a rolled-back restore the live store holds %v, want [Original]", got)
	}
	assertNoRollbackFile(t, dbPath)
}

// TestImportRefusesALeftoverParkedDatabase guards the worst sequence this
// mechanism can produce.
//
// The park is a rename, and rename overwrites its destination. A process killed
// between the park and the swap leaves the real database at .rollback and
// nothing at dbPath; the container restarts, store.New creates an empty database
// and re-seeds the default admin, and the operator — seeing an empty instance —
// restores a backup. Without this check that restore renames the empty database
// over the last copy of their data, silently and with no error.
//
// Verified against the code before the check existed: Import returned nil and
// the leftover file was simply gone.
func TestImportRefusesALeftoverParkedDatabase(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// A plain file, which is what a crashed restore actually leaves. An earlier
	// version of this test used a directory — which fails the rename for
	// unrelated reasons, and so hid that a file succeeds and is destroyed.
	if err := os.WriteFile(dbPath+rollbackSuffix, []byte("the operator's only database"), 0o644); err != nil {
		t.Fatalf("writing leftover: %v", err)
	}

	err := Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	got, readErr := os.ReadFile(dbPath + rollbackSuffix)
	if readErr != nil {
		t.Fatalf("the leftover parked database was destroyed: %v", readErr)
	}
	if string(got) != "the operator's only database" {
		t.Errorf("the leftover parked database was overwritten: %q", got)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("live store holds %v, want [Original]", got)
	}
}

// TestImportRollsBackWhenTheOriginalCannotBeParked covers a failure before
// anything has moved, so nothing needs putting back.
func TestImportRollsBackWhenTheOriginalCannotBeParked(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// A directory at the park path: os.Stat sees it, so the preflight refuses.
	if err := os.Mkdir(dbPath+rollbackSuffix, 0o755); err != nil {
		t.Fatalf("blocking the park path: %v", err)
	}

	err := Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("live store holds %v, want [Original]", got)
	}
}

// TestImportRollsBackWhenTheSwapFails exercises the branch that actually puts the
// previous database back, which nothing covered before.
//
// The earlier test of this name did not test the swap at all: it blocked the
// park path, so the failure happened one step earlier and took the
// nothing-has-moved branch. The swap's own failure — the one where the original
// is already parked and must be renamed back — was reachable only by reverting
// the fix, which the whole suite then still passed.
//
// The upload is staged in its own directory, made read-only after validation, so
// the rename cannot unlink it. That is the same class of refusal as the EXDEV
// failure this staging location was changed to avoid.
func TestImportRollsBackWhenTheSwapFails(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)
	s := &vanishingUploadStore{inner: live, remove: uploadPath}

	err := Import(s, uploadPath, dbPath)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("Import returned %v, want ErrRolledBack", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("after the swap failed the live store holds %v, want [Original] — "+
			"the previous database was not put back", got)
	}
	assertNoRollbackFile(t, dbPath)
}

// TestImportReportsUnavailableWhenTheDatabaseIsGone pins the case where Reopen
// would otherwise invent a replacement.
//
// SQLite creates the file if it is missing, so store.Reopen on an absent dbPath
// opens a brand-new empty database and returns nil. Import used to treat that as
// proof the previous database was back, and reported ErrRolledBack — telling the
// operator "your existing database is unchanged and still in use" while serving
// zero forms and zero users, so nobody could even log in to discover it.
func TestImportReportsUnavailableWhenTheDatabaseIsGone(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// Flush and remove the main file, leaving the handle open.
	if _, err := live.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("removing the database: %v", err)
	}

	err := Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Import returned %v, want ErrUnavailable.\nReopen will happily create "+
			"an empty database, so openability is not evidence that anything was "+
			"rolled back.", err)
	}
	if errors.Is(err, ErrRolledBack) {
		t.Error("this must not report as a rollback — nothing was rolled back")
	}
}

// TestImportReportsUnavailableWhenRollbackAlsoFails pins the one genuinely
// unrecoverable state, so it is reported as itself rather than inferred from a
// generic failure. It is the only outcome where the operator must restart.
func TestImportReportsUnavailableWhenRollbackAlsoFails(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)
	s := &flakyStore{inner: live, failReopens: 2}

	err := Import(s, uploadPath, dbPath)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Import returned %v, want ErrUnavailable", err)
	}
	if errors.Is(err, ErrRolledBack) {
		t.Error("ErrUnavailable must not also satisfy ErrRolledBack — they call for " +
			"opposite operator responses")
	}
}

// TestImportRejectsWithoutTouchingTheDatabase covers everything that fails
// before the live handle is closed. Nothing has changed, so the operator should
// be told exactly that rather than that a restore failed.
func TestImportRejectsWithoutTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	live, dbPath, _ := restoreFixture(t)

	bad := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(bad, []byte("not a database"), 0o644); err != nil {
		t.Fatalf("writing bad file: %v", err)
	}

	err := Import(live, bad, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("live store holds %v, want [Original]", got)
	}
	assertNoRollbackFile(t, dbPath)
}

// TestImportSucceedsAndCleansUp is the success path, asserted for the thing that
// is easy to leave behind: a full copy of the previous database sitting next to
// the live one, silently doubling disk use on every restore.
func TestImportSucceedsAndCleansUp(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	if err := Import(live, uploadPath, dbPath); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Restored" {
		t.Errorf("after a successful restore the live store holds %v, want [Restored]", got)
	}
	assertNoRollbackFile(t, dbPath)
}

// TestImportRefusesWhenTheWALCannotBeFullyFlushed is the case the first version
// of this guard missed entirely.
//
// Import removes the write-ahead log before swapping, because SQLite would
// otherwise replay the old database's frames over the restored one. That is only
// safe if every frame has first been checkpointed into the main file.
//
// `PRAGMA wal_checkpoint(TRUNCATE)` does NOT report contention as an error. It
// returns a row — (busy, log, checkpointed) — and a reader holding a snapshot
// produces `busy=1, log=53, checkpointed=52` with a nil error. Checking only the
// Exec error therefore passed in exactly the situation where frames are left
// behind, which is the situation the check exists for. A concurrent admin
// request is enough to cause it.
func TestImportRefusesWhenTheWALCannotBeFullyFlushed(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// A second connection holding a read snapshot pins the WAL, so the
	// checkpoint cannot copy the newest frames into the main database.
	reader, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("opening reader: %v", err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	var id string
	if err := tx.QueryRow("SELECT id FROM forms LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("read in tx: %v", err)
	}
	defer tx.Rollback()

	// Write after the snapshot, so there is a frame the checkpoint cannot flush.
	for i := 0; i < 50; i++ {
		if err := live.CreateForm(store.Form{
			ID: "pending-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Name: "Pending", EmailTo: "p@q.com",
		}); err != nil {
			t.Fatalf("seeding pending writes: %v", err)
		}
	}

	err = Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected.\nThe write-ahead log still "+
			"holds frames that are not in the main database. Removing it — which the "+
			"swap does next — discards them, and rolling back would restore a database "+
			"missing its own most recent writes.", err)
	}
	if got := formNames(t, live); len(got) == 0 {
		t.Error("the live store is not serving after a refused restore")
	}
}

// TestImportSaysWhereTheDatabaseIsWhenItCannotBePutBack covers the worst state
// the function can reach, and the message is the point of the test.
//
// The replacement failed, and the previous database could not be renamed back.
// Telling the operator only "restart the service" is actively harmful here:
// starting with no database at dbPath makes store.New create an empty one and
// re-seed the default admin login, on a public URL, while their real data sits
// under a suffix nothing else in the codebase mentions.
func TestImportSaysWhereTheDatabaseIsWhenItCannotBePutBack(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)
	s := &sabotagingStore{inner: live, parkPath: dbPath + rollbackSuffix}

	err := Import(s, uploadPath, dbPath)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Import returned %v, want ErrUnavailable", err)
	}
	if errors.Is(err, ErrRolledBack) {
		t.Fatal("nothing was rolled back; reporting this as a rollback tells the " +
			"operator their database is still in use when it is not")
	}
	for _, want := range []string{rollbackSuffix, "default admin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so the operator is not told where "+
				"their database is or why restarting is dangerous:\n  %v", want, err)
		}
	}
}

// TestImportDoesNotRaceWithReaders is the regression test for a data race this
// line of work made continuous.
//
// store.Reopen replaces Store.db while the server is serving, and every store
// method reads that field. Before the health check it was a coincidence — a race
// only when a request happened to overlap a restore. /healthz reads it every few
// seconds forever, and a restore is precisely when the write lands.
//
// Verified failing before the lock: -race reported a write at store.go:470
// against a read at store.go:435.
func TestImportDoesNotRaceWithReaders(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// Exactly what the health check does on every probe.
				_ = live.DB()
			}
		}
	}()

	err := Import(live, uploadPath, dbPath)
	close(stop)
	wg.Wait()

	if err != nil {
		t.Fatalf("Import: %v", err)
	}
}

// TestSweepStagedUploadsRemovesOnlyAbandonedUploads covers the cleanup for a
// file the restore path now deliberately leaves on the data volume.
//
// Staging beside the database is what makes the final rename same-filesystem, so
// it fixed EXDEV — at the cost that a process killed between staging and the
// swap leaves the upload there, up to the 100 MB limit, with nothing to remove
// it. Several interrupted restores fill the disk the database needs.
//
// The discrimination matters as much as the removal: a sweep that took the
// database, its WAL, or an operator's own backup file with it would be a far
// worse bug than the litter.
func TestSweepStagedUploadsRemovesOnlyAbandonedUploads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stale := []string{"dsforms-import-123.db", "dsforms-import-abc.db"}
	keep := []string{
		"dsforms.db", "dsforms.db-wal", "dsforms.db-shm",
		"dsforms.db.rollback",      // a parked database: the last copy of real data
		"my-backup.db",             // an operator's own file
		"dsforms-import-notes.txt", // right prefix, wrong kind
	}
	for _, n := range append(append([]string{}, stale...), keep...) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", n, err)
		}
	}

	removed, err := SweepStagedUploads(dir)
	if err != nil {
		t.Fatalf("SweepStagedUploads: %v", err)
	}
	if removed != len(stale) {
		t.Errorf("removed %d files, want %d", removed, len(stale))
	}

	for _, n := range stale {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", n)
		}
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("the sweep removed %s, which it must never touch: %v", n, err)
		}
	}
}

// TestImportSerializesConcurrentRestores pins that two restores cannot interleave.
//
// Both close the live handle and then contend for one fixed park path: the
// second can rename the first's parked database away, or swap its upload into
// place between the first's swap and its reopen. Every interleaving is some
// combination of two half-finished swaps over one file, and the loser is the
// only copy of the operator's data.
func TestImportSerializesConcurrentRestores(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// A distinguishable payload. The first version of this test seeded both
	// uploads with the same form name, so it could not tell which restore won —
	// and removing the mutex left it passing, which is the exact shape of guard
	// this repo keeps having to fix.
	second := filepath.Join(filepath.Dir(dbPath), "upload2.db")
	seedBackupFile(t, second, "SecondRestore")

	s := &slowReopenStore{
		inner:   live,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- Import(s, uploadPath, dbPath) }()

	// Wait until the first restore is parked inside Reopen: its previous database
	// is at .rollback and its replacement is already at dbPath.
	<-s.entered

	secondDone := make(chan error, 1)
	go func() { secondDone <- Import(s, second, dbPath) }()

	// With the mutex, the second restore is still blocked at the top of Import
	// and cannot have touched anything. Without it, it has had a clear run at the
	// same three files.
	select {
	case err := <-secondDone:
		t.Fatalf("a second restore ran to completion (%v) while the first was "+
			"mid-swap.\nBoth close the live handle and contend for one park path, so "+
			"whatever is on disk now is some interleaving of two half-finished "+
			"swaps.", err)
	case <-time.After(250 * time.Millisecond):
		// Correctly blocked.
	}

	close(s.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first restore: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second restore: %v", err)
	}

	// Whatever order they ran in, the process must be serving exactly one of the
	// two uploads — never a mixture, never the original, never nothing.
	got := formNames(t, live)
	if len(got) != 1 || (got[0] != "Restored" && got[0] != "SecondRestore") {
		t.Errorf("after two restores the live store holds %v, want exactly one of "+
			"[Restored] or [SecondRestore]", got)
	}
	assertNoRollbackFile(t, dbPath)
}

// TestARefusalAfterValidateIsNotTheFilesFault pins the boundary the two
// sentinels draw.
//
// Once the upload has been validated and stripped, nothing Import can still
// refuse for is about the file — a leftover parked database, a write-ahead log
// another request is pinning on the live side. Reported as a plain rejection
// they all told the operator their file was bad, and the obvious next step,
// re-export and re-upload, redoes the one thing that was already fine.
//
// The step in between, stripCredentials, goes either way: see
// TestStrippingFailuresStayTheFilesFault and its sorted counterpart below.
//
// Both sentinels, deliberately: nothing was touched is still true, and a caller
// that only wants to know whether its data survived must not have to learn a
// second name for it.
func TestARefusalAfterValidateIsNotTheFilesFault(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	if err := os.WriteFile(dbPath+rollbackSuffix, []byte("the operator's only database"), 0o644); err != nil {
		t.Fatalf("writing leftover: %v", err)
	}

	err := Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrNotAttempted) {
		t.Errorf("Import returned %v, want it to carry ErrNotAttempted: the "+
			"uploaded file passed Validate, so it is not what stopped the restore", err)
	}
	if !errors.Is(err, ErrRejected) {
		t.Errorf("Import returned %v, want it to carry ErrRejected too: nothing "+
			"was touched, and that is what a caller asks first", err)
	}
}

// And the other side of the boundary: a file that cannot be validated is the
// file's fault, and must not claim otherwise.
func TestARefusedFileIsTheFilesFault(t *testing.T) {
	t.Parallel()
	live, dbPath, _ := restoreFixture(t)

	bad := filepath.Join(t.TempDir(), "not-a-database.db")
	if err := os.WriteFile(bad, []byte("this is not a database"), 0o644); err != nil {
		t.Fatalf("writing the bad upload: %v", err)
	}

	err := Import(live, bad, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	if errors.Is(err, ErrNotAttempted) {
		t.Errorf("Import returned %v, but this one really is the uploaded file — "+
			"telling the operator otherwise sends them looking at the wrong thing", err)
	}
}

// TestStrippingFailuresStayTheFilesFault is where the boundary between the two
// sentinels actually lies, which is one step earlier than it first looks.
//
// stripCredentials runs after Validate has passed, so it seems to belong with
// the refusals that are about this instance. It does not: it is the last step
// that still operates on the uploaded file, and the file can be what stops it —
// a trigger on api_tokens, a schema that will not come out of WAL mode.
// ErrNotAttempted asserts something positive, that the operator's file is fine,
// so it may only be used where that has been established. Here it has not, and
// claiming it sends them looking for a server-side obstacle that is not there.
func TestStrippingFailuresStayTheFilesFault(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// A database that passes Validate and then refuses to be stripped.
	upload, err := sql.Open("sqlite", uploadPath)
	if err != nil {
		t.Fatalf("opening the upload: %v", err)
	}
	if _, err := upload.Exec(
		`CREATE TRIGGER no_delete BEFORE DELETE ON api_tokens
		 BEGIN SELECT RAISE(ABORT, 'nope'); END`,
	); err != nil {
		t.Fatalf("arming the upload: %v", err)
	}
	if _, err := upload.Exec(
		`INSERT INTO api_tokens (id, user_id, name, token_hash, scopes, created_at)
		 VALUES ('t1', 'u1', 'laptop', 'deadbeef', 'read', CURRENT_TIMESTAMP)`,
	); err != nil {
		t.Fatalf("seeding a token to strip: %v", err)
	}
	if err := upload.Close(); err != nil {
		t.Fatalf("closing the upload: %v", err)
	}

	// It really does get past Validate, or this proves nothing about the step
	// after it.
	if err := Validate(uploadPath); err != nil {
		t.Fatalf("the fixture does not reach stripCredentials: %v", err)
	}

	err = Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	if errors.Is(err, ErrNotAttempted) {
		t.Errorf("Import returned %v, but the uploaded file is exactly what "+
			"stopped the restore — telling the operator to go looking at the "+
			"server sends them after an obstacle that is not there", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("after a refused restore the live store holds %v, want [Original]", got)
	}
}

// TestStrippingFailuresAreSortedByWhoseFaultTheyAre is the other half of
// TestStrippingFailuresStayTheFilesFault.
//
// stripCredentials is the one step that can fail either way, so it is the one
// step that asks. Its VACUUM writes a second copy of the whole database, which
// is where a full disk lands, and reporting that as "that file was rejected"
// sends the operator to re-upload a file that was never the problem — while
// reporting a trigger in their schema as a server obstacle sends them looking
// for something that does not exist.
func TestStrippingFailuresAreSortedByWhoseFaultTheyAre(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	// Something else holding the staged file. Not the file's contents, so the
	// operator's copy is fine and re-uploading it cannot help.
	holder, err := sql.Open("sqlite", uploadPath)
	if err != nil {
		t.Fatalf("opening a second connection: %v", err)
	}
	defer holder.Close()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO forms (id, name, email_to) VALUES ('pin', 'Pin', 'p@q.com')`,
	); err != nil {
		t.Fatalf("taking the write lock: %v", err)
	}
	defer tx.Rollback()

	err = Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Import returned %v, want ErrRejected", err)
	}
	if !errors.Is(err, ErrNotAttempted) {
		t.Errorf("Import returned %v, want it to carry ErrNotAttempted: the file "+
			"is locked, not malformed, so re-exporting and re-uploading it is the "+
			"one thing that cannot help", err)
	}
}

// machineFault decides a claim, so what it does with an answer it does not
// recognise is the whole of it.
func TestMachineFaultClaimsNothingItDoesNotKnow(t *testing.T) {
	t.Parallel()

	coded := func(code int) error {
		return fmt.Errorf("wrapped: %w", &codedError{code: code})
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"busy is the machine", coded(5), true},
		{"disk full is the machine", coded(13), true},
		{"io error is the machine", coded(10), true},
		// The extended codes SQLite actually returns, not the bare primaries.
		{"an extended io error is still the machine", coded(10 | 12<<8), true},
		{"an extended full is still the machine", coded(13 | 2<<8), true},
		{"a constraint is the file", coded(19), false},
		{"corruption is the file", coded(11), false},
		{"not a database is the file", coded(26), false},
		{"a code nobody listed is the file", coded(99), false},
		{"an error with no code at all is the file", errors.New("something"), false},
		{"and nil claims nothing", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := machineFault(tc.err); got != tc.want {
				t.Errorf("machineFault(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// codedError carries a SQLite result code the way the driver's own error does.
type codedError struct{ code int }

func (e *codedError) Error() string { return fmt.Sprintf("sqlite error (%d)", e.code) }
func (e *codedError) Code() int     { return e.code }
