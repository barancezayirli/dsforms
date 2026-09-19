package backup

import (
	"bytes"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/store"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/test.db"
	s, err := store.New(path)
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}
	return s, path
}

func TestExportCreatesValidFile(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}
	defer os.Remove(path)
	// Open the exported file and verify it's a valid SQLite DB
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open exported file: %v", err)
	}
	defer db.Close()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM forms").Scan(&count)
	if err != nil {
		t.Fatalf("query exported DB: %v", err)
	}
	if count != 1 {
		t.Errorf("forms count = %d, want 1", count)
	}
}

func TestExportContainsTables(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}
	defer os.Remove(path)
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	for _, table := range []string{"users", "forms", "submissions"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found in export: %v", table, err)
		}
	}
}

func TestExportDoesNotAffectLiveDB(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}
	defer os.Remove(path)
	// Live DB should still work
	forms, err := s.ListForms(store.AllForms())
	if err != nil {
		t.Fatalf("ListForms after export: %v", err)
	}
	if len(forms) != 1 {
		t.Errorf("forms = %d, want 1", len(forms))
	}
}

func TestValidateValidDB(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	path, _ := Export(s.DB())
	defer os.Remove(path)
	if err := Validate(path); err != nil {
		t.Errorf("Validate valid DB: %v", err)
	}
}

func TestValidateInvalidFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/notadb.txt"
	os.WriteFile(path, []byte("this is not a database"), 0644)
	if err := Validate(path); err == nil {
		t.Fatal("expected error for invalid file")
	}
}

func TestValidateMissingTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/empty.db"
	// Create a valid but empty SQLite DB (no required tables)
	db, _ := sql.Open("sqlite", path)
	db.Exec("CREATE TABLE dummy (id TEXT)")
	db.Close()
	if err := Validate(path); err == nil {
		t.Fatal("expected error for missing required tables")
	}
}

func TestImportValidFile(t *testing.T) {
	t.Parallel()
	// Create store A with data
	s, dbPath := testStore(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Original", EmailTo: "a@b.com"})

	// Create store B with different data (this will be the "backup")
	dir := t.TempDir()
	pathB := dir + "/backup.db"
	sB, err := store.New(pathB)
	if err != nil {
		t.Fatalf("store.New for backup: %v", err)
	}
	_ = sB.CreateForm(store.Form{ID: "f2", Name: "Restored", EmailTo: "b@c.com"})
	sB.Close()

	// Copy pathB as our import source
	importPath := dir + "/import.db"
	data, _ := os.ReadFile(pathB)
	os.WriteFile(importPath, data, 0644)

	// Import into store A
	if err := Import(s, importPath, dbPath); err != nil {
		t.Fatalf("Import error: %v", err)
	}

	// Store A should now have the restored data
	forms, err := s.ListForms(store.AllForms())
	if err != nil {
		t.Fatalf("ListForms after import: %v", err)
	}
	found := false
	for _, f := range forms {
		if f.Name == "Restored" {
			found = true
		}
	}
	if !found {
		t.Error("imported data not found after import")
	}
}

func TestImportInvalidFile(t *testing.T) {
	t.Parallel()
	s, dbPath := testStore(t)
	dir := t.TempDir()
	badPath := dir + "/bad.txt"
	os.WriteFile(badPath, []byte("not a database"), 0644)
	if err := Import(s, badPath, dbPath); err == nil {
		t.Fatal("expected error importing invalid file")
	}
	// Store should still work after failed import
	_, err := s.ListForms(store.AllForms())
	if err != nil {
		t.Fatalf("store broken after failed import: %v", err)
	}
}

// seedWithToken puts a form, a submission and an API token in a store, and
// returns the token's stored hash so a test can look for it in a snapshot.
func seedWithToken(t *testing.T, s *store.Store) string {
	t.Helper()
	if err := s.CreateForm(store.Form{ID: "f1", Name: "Contact", EmailTo: "a@b.com"}); err != nil {
		t.Fatalf("CreateForm: %v", err)
	}
	if err := s.CreateSubmission(store.Submission{
		ID: "s1", FormID: "f1", RawData: `{"message":"keep me"}`, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if _, _, err := s.CreateAPIToken(u.ID, "laptop", []string{"read"}, nil, 0); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	var hash string
	if err := s.DB().QueryRow("SELECT token_hash FROM api_tokens LIMIT 1").Scan(&hash); err != nil {
		t.Fatalf("reading the stored hash: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("stored hash is %d chars, want a SHA-256 hex digest", len(hash))
	}
	return hash
}

// TestExportLeavesTheAPITokensBehind.
//
// A snapshot is a copy of the whole database, so it used to carry every token's
// hash — and, worse, restoring one resurrected tokens revoked since it was
// taken. Revocation is a security action and a restore silently undoing it is
// the wrong direction to fail in, especially as whoever was revoked may still
// be holding the string.
//
// The cost is stated in the docs: a restore no longer brings your live tokens
// back either, so they are re-minted after a recovery.
func TestExportLeavesTheAPITokensBehind(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	hash := seedWithToken(t, s)

	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer os.Remove(path)

	// Not merely unlinked: gone from the bytes. A DELETE without a VACUUM
	// leaves the page intact and a grep still finds the digest — see
	// TestDeleteAloneLeavesTheBytesInTheFile.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if bytes.Contains(raw, []byte(hash)) {
		t.Error("the token's hash is still in the snapshot's bytes")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()

	// The table is still there, empty. Dropping it would make the snapshot a
	// different shape from the schema, and Import re-runs migrations anyway.
	var tokens int
	if err := db.QueryRow("SELECT COUNT(*) FROM api_tokens").Scan(&tokens); err != nil {
		t.Fatalf("counting api_tokens in the snapshot: %v", err)
	}
	if tokens != 0 {
		t.Errorf("snapshot holds %d api_tokens rows, want none", tokens)
	}

	// And nothing else went with them.
	for _, tc := range []struct {
		table string
		want  int
	}{{"forms", 1}, {"submissions", 1}, {"users", 1}} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tc.table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", tc.table, err)
		}
		if n != tc.want {
			t.Errorf("%s has %d rows in the snapshot, want %d", tc.table, n, tc.want)
		}
	}
}

// TestExportDoesNotTouchTheLiveDatabase. Export is a read of the instance, and
// an operator taking a backup must not thereby revoke their own tokens.
func TestExportDoesNotTouchTheLiveDatabase(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	seedWithToken(t, s)

	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer os.Remove(path)

	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	tokens, err := s.ListAPITokens(u.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("the live instance has %d tokens after a backup, want 1", len(tokens))
	}
}

// TestASnapshotWithoutTokensStillValidates, because Import refuses a file that
// does not look like a dsforms database and a stripped one still has to.
func TestASnapshotWithoutTokensStillValidates(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	seedWithToken(t, s)

	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer os.Remove(path)

	if err := Validate(path); err != nil {
		t.Errorf("a stripped snapshot no longer validates: %v", err)
	}
}
