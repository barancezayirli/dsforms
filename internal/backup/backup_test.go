package backup

import (
	"database/sql"
	"os"
	"testing"

	"github.com/youruser/dsforms/internal/store"
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
	forms, err := s.ListForms()
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
	forms, err := s.ListForms()
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
	_, err := s.ListForms()
	if err != nil {
		t.Fatalf("store broken after failed import: %v", err)
	}
}
