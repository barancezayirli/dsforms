package backup

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

// formNames is the assertion both outcomes turn on: which database is actually
// being served after Import returns.
func formNames(t *testing.T, s *store.Store) []string {
	t.Helper()
	forms, err := s.ListForms()
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

// TestImportRollsBackWhenTheSwapFails covers a failure before anything has
// moved. A directory sitting at the rollback path makes the first rename fail,
// which stands in for any filesystem refusal at that step.
func TestImportRollsBackWhenTheSwapFails(t *testing.T) {
	t.Parallel()
	live, dbPath, uploadPath := restoreFixture(t)

	if err := os.Mkdir(dbPath+rollbackSuffix, 0o755); err != nil {
		t.Fatalf("blocking the rollback path: %v", err)
	}

	err := Import(live, uploadPath, dbPath)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("Import returned %v, want ErrRolledBack", err)
	}
	if got := formNames(t, live); len(got) != 1 || got[0] != "Original" {
		t.Errorf("live store holds %v, want [Original]", got)
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
