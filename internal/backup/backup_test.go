package backup

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	err := Validate(path)
	if err == nil {
		t.Fatal("expected error for missing required tables")
	}
	// Named as missing, so the NOCASE lookup cannot pass by reporting every
	// failure the same way.
	if !strings.Contains(err.Error(), "missing required table") {
		t.Errorf("a missing table reported as %v, want it named as missing", err)
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

// TestExportLeavesTheSessionsBehind.
//
// Sessions were kept out of the first version of this on the reasoning that
// dropping them would sign every operator out of a restored instance. Both
// halves of that turned out to be wrong when tested: a restore signs out the
// operator performing it regardless (their session postdates the snapshot), and
// keeping them resurrects sessions that were deliberately destroyed — a logout,
// a password change, a deleted user's cascade. internal/handler/auth.go calls
// that last one a guarantee.
func TestExportLeavesTheSessionsBehind(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	seedWithToken(t, s)

	u, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if _, err := s.CreateSession(u.ID, 24*time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var hash string
	if err := s.DB().QueryRow("SELECT token_hash FROM sessions LIMIT 1").Scan(&hash); err != nil {
		t.Fatalf("reading the stored session hash: %v", err)
	}

	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer os.Remove(path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if bytes.Contains(raw, []byte(hash)) {
		t.Error("a session hash is still in the snapshot's bytes")
	}
}

// TestEveryCredentialTableIsEmptyInASnapshot ranges the list rather than naming
// tables, so a table added to it later is covered without anyone remembering
// this test — and one added to the schema but not to the list is the gap this
// cannot see, which is why the list is short and commented.
func TestEveryCredentialTableIsEmptyInASnapshot(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	seedWithToken(t, s)
	u, _ := s.GetUserByUsername("admin")
	if _, err := s.CreateSession(u.ID, 24*time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Every one has a row before the export, or the assertion below is vacuous.
	for _, table := range credentialTables {
		var n int
		if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("counting %s in the live database: %v", table, err)
		}
		if n == 0 {
			t.Fatalf("%s is empty before the export, so this test proves nothing", table)
		}
	}

	path, err := Export(s.DB())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer os.Remove(path)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	for _, table := range credentialTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("counting %s in the snapshot: %v", table, err)
		}
		if n != 0 {
			t.Errorf("snapshot holds %d %s rows, want none", n, table)
		}
	}
}

// TestImportStripsCredentialsFromAnOldSnapshot.
//
// The export-side defence only ever runs on files this build wrote. A snapshot
// taken before it existed still carries credentials, and operations.md tells
// people they may drop in a raw copy of the database file — either would walk a
// revoked token or a logged-out session straight back into a live instance.
func TestImportStripsCredentialsFromAnOldSnapshot(t *testing.T) {
	t.Parallel()

	// A "snapshot" with credentials in it, exactly as an older build produced.
	old, oldPath := testStore(t)
	seedWithToken(t, old)
	u, err := old.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if _, err := old.CreateSession(u.ID, 24*time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	legacy := filepath.Join(t.TempDir(), "legacy-snapshot.db")
	in, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read the old database: %v", err)
	}
	if err := os.WriteFile(legacy, in, 0o600); err != nil {
		t.Fatalf("write the legacy snapshot: %v", err)
	}

	// It really does carry them, or the assertion below proves nothing.
	src, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatalf("open legacy snapshot: %v", err)
	}
	for _, table := range credentialTables {
		var n int
		if err := src.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if n == 0 {
			t.Fatalf("the legacy snapshot has no %s rows, so this test proves nothing", table)
		}
	}
	if err := src.Close(); err != nil {
		t.Fatalf("close legacy snapshot: %v", err)
	}

	live, livePath := testStore(t)
	if err := Import(live, legacy, livePath); err != nil {
		t.Fatalf("Import: %v", err)
	}

	restored, err := store.New(livePath)
	if err != nil {
		t.Fatalf("reopen the restored database: %v", err)
	}
	defer restored.Close()
	for _, table := range credentialTables {
		var n int
		if err := restored.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("counting %s after the restore: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%d %s rows survived the restore of a legacy snapshot", n, table)
		}
	}

	// And the data the restore was for did come back.
	var forms int
	if err := restored.DB().QueryRow("SELECT COUNT(*) FROM forms").Scan(&forms); err != nil {
		t.Fatalf("counting forms: %v", err)
	}
	if forms != 1 {
		t.Errorf("forms = %d after the restore, want 1", forms)
	}
}

// TestImportAcceptsADatabaseFromBeforeTheseTablesExisted.
//
// Stripping on the way in is worth nothing if it refuses the files it was added
// for. A database from before the MCP work has no api_tokens table at all, and
// an unguarded DELETE against it fails the whole restore — closing the recovery
// path for every older backup, and for the raw copy of a database file that
// operations.md explicitly invites.
func TestImportAcceptsADatabaseFromBeforeTheseTablesExisted(t *testing.T) {
	t.Parallel()

	legacy := filepath.Join(t.TempDir(), "ancient.db")
	db, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The pre-quarantine schema, as internal/store's own upgrade test writes it:
	// no api_tokens and no sessions, but enough that migrations can run.
	if _, err := db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE forms (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, email_to TEXT NOT NULL DEFAULT '',
			redirect TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE submissions (
			id TEXT PRIMARY KEY,
			form_id TEXT NOT NULL REFERENCES forms(id) ON DELETE CASCADE,
			data TEXT NOT NULL, ip TEXT NOT NULL DEFAULT '',
			read INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO users (id, username) VALUES ('u1', 'admin');
		INSERT INTO forms (id, name) VALUES ('f1', 'Contact');
		INSERT INTO submissions (id, form_id, data) VALUES ('s1', 'f1', '{"message":"old"}');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	live, livePath := testStore(t)
	if err := Import(live, legacy, livePath); err != nil {
		t.Fatalf("Import refused a pre-MCP database: %v", err)
	}

	restored, err := store.New(livePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer restored.Close()
	var forms int
	if err := restored.DB().QueryRow("SELECT COUNT(*) FROM forms").Scan(&forms); err != nil {
		t.Fatalf("counting forms: %v", err)
	}
	if forms != 1 {
		t.Errorf("forms = %d, want the restored one", forms)
	}
}

// TestSweepRemovesTheSidecarsToo. Stripping opens the staged upload, and a raw
// copy of a live database is in WAL mode — so the open creates sidecars beside
// it. A sweep that matches only *.db leaves a file up to the size of the
// database behind, forever, on the volume the sweep exists to protect.
func TestSweepRemovesTheSidecarsToo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	leftovers := []string{
		"dsforms-import-123.db",
		"dsforms-import-123.db-wal",
		"dsforms-import-123.db-shm",
		"dsforms-import-456.db-journal",
	}
	keep := filepath.Join(dir, "dsforms.db")
	for _, name := range append(append([]string{}, leftovers...), "dsforms.db") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	removed, err := SweepStagedUploads(dir)
	if err != nil {
		t.Fatalf("SweepStagedUploads: %v", err)
	}
	if removed != len(leftovers) {
		t.Errorf("removed %d files, want %d", removed, len(leftovers))
	}
	for _, name := range leftovers {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", name)
		}
	}
	// And it did not take the live database with it.
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the sweep removed the live database: %v", err)
	}
}

// walFile writes a database in WAL mode holding one recognisable secret, as a
// raw copy of a live instance would be.
func walFile(t *testing.T, table string) (path, secret string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "raw-copy.db")
	secret = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE);
		CREATE TABLE forms (id TEXT PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE submissions (id TEXT PRIMARY KEY, form_id TEXT NOT NULL, data TEXT NOT NULL, read INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE ` + table + ` (id TEXT PRIMARY KEY, token_hash TEXT NOT NULL);
		INSERT INTO users (id, username) VALUES ('u1', 'admin');
		INSERT INTO forms (id, name) VALUES ('f1', 'Contact');
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO "+table+" (id, token_hash) VALUES ('t1', ?)", secret); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("fixture is in %q mode, not wal, so it does not exercise the path it is for", mode)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path, secret
}

// TestStrippingLeavesNoWriteAheadLogBehind.
//
// A raw copy of a live database arrives in WAL mode, and the first version of
// this relied on Close checkpointing the DELETE and VACUUM into the main file —
// which is the file Import renames into place. A checkpoint that fails is not
// reported as an error by SQLite, so that left a restore able to report success
// while renaming an unstripped file; the same lesson Import's own
// wal_checkpoint handling records one screen above.
//
// Taking the file out of WAL mode removes the failure rather than detecting it:
// there is no sidecar to checkpoint, and nothing can be left outside the file
// that gets renamed. store.New reopens with journal_mode(WAL), so the change is
// only for the duration of the strip.
func TestStrippingLeavesNoWriteAheadLogBehind(t *testing.T) {
	t.Parallel()
	path, secret := walFile(t, "api_tokens")

	if err := stripCredentials(path); err != nil {
		t.Fatalf("stripCredentials: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Errorf("%s survived the strip", filepath.Base(path+suffix))
		}
	}

	// Structurally, not incidentally: the file is out of WAL mode, so there is
	// no checkpoint that could fail silently and leave the main file stale.
	after, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var mode string
	if err := after.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if err := after.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if strings.EqualFold(mode, "wal") {
		t.Error("the stripped file is still in WAL mode, so the cleared rows may sit " +
			"in a sidecar that Import does not rename")
	}

	// Everything has to be in the file Import renames, because that is the only
	// one it renames.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Error("the hash is still in the main database file")
	}
}

// TestStrippingMatchesTableNamesTheWaySQLiteDoes. The presence check and the
// DELETE it guards have to agree: sqlite_master compares with BINARY collation
// while DELETE resolves table names case-insensitively, so a schema declaring
// API_Tokens was skipped as absent and the restore reported success with the
// credentials intact.
func TestStrippingMatchesTableNamesTheWaySQLiteDoes(t *testing.T) {
	t.Parallel()
	path, secret := walFile(t, "API_Tokens")

	if err := stripCredentials(path); err != nil {
		t.Fatalf("stripCredentials: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Error("a table spelled API_Tokens was treated as absent and left intact")
	}
}

// TestValidateMatchesTableNamesTheWaySQLiteDoes is the same defect as
// TestStrippingMatchesTableNamesTheWaySQLiteDoes, one function over: the
// required-table check compared with BINARY collation while every query that
// then uses those tables resolves their names case-insensitively. A database
// declaring Users/Forms/Submissions works perfectly and was refused as missing
// them — closing the raw-database-copy recovery path operations.md advertises.
func TestValidateMatchesTableNamesTheWaySQLiteDoes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mixed-case.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE Users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE);
		CREATE TABLE Forms (id TEXT PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE Submissions (id TEXT PRIMARY KEY, form_id TEXT NOT NULL, data TEXT NOT NULL);
		INSERT INTO Users (id, username) VALUES ('u1', 'admin');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The tables really are usable under the lowercase names the store uses.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		t.Fatalf("the fixture is not actually case-insensitive here: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := Validate(path); err != nil {
		t.Errorf("Validate refused a usable database over table-name casing: %v", err)
	}
}

// A schema that could not be read is not a schema that is missing a table. The
// two used to be the same error, which told an admin to re-export a good file.
func TestValidateTellsAnUnreadableSchemaFromAMissingTable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "closed.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE users (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Closed, so the query fails for a reason that has nothing to do with what
	// the schema declares — the table it asks about is right there.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err = requireTables(db, "users")
	if err == nil {
		t.Fatal("requireTables accepted a database it could not query")
	}
	if strings.Contains(err.Error(), "missing required table") {
		t.Errorf("an unreadable schema reported as a missing table: %v", err)
	}
	// What went wrong is carried, not replaced. Asserted against the wrapped
	// error's own text rather than a literal, which would be database/sql's
	// unexported wording and not this package's to depend on.
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("underlying error dropped rather than wrapped: %v", err)
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Errorf("error does not say what went wrong: %v", err)
	}
}
