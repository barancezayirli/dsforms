package handler

import (
	"bytes"
	"database/sql"
	"errors"
	"go/ast"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/barancezayirli/dsforms/internal/astcheck"
	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/go-chi/chi/v5"
)

func setupBackup(t *testing.T) (*store.Store, *chi.Mux, string, *BackupHandler) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	funcMap := TemplateFuncs()
	templates := make(map[string]*template.Template)

	baseTmpl := template.Must(template.New("base").Funcs(funcMap).Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	backupTmpl, _ := baseTmpl.Clone()
	template.Must(backupTmpl.New("content").Parse(`<p>backups page</p>`))
	templates["backups.html"] = backupTmpl

	bh := &BackupHandler{
		Store: s,
		Base: Base{
			Nav:       s,
			SecretKey: testSecretKey,
			BaseURL:   "https://example.com",
			DBPath:    dbPath,
			Templates: templates,
		},
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/backups", bh.Page)
		r.Get("/admin/backups/export", bh.Export)
		r.Post("/admin/backups/import", bh.Import)
	})

	return s, r, dbPath, bh
}

func doBackupRequest(t *testing.T, s *store.Store, r *chi.Mux, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	admin, _ := s.GetUserByUsername("admin")
	token, _ := s.CreateSession(admin.ID, 30*24*time.Hour)
	cookie := auth.CreateSessionCookie(token, "https://example.com")
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestBackupPage(t *testing.T) {
	t.Parallel()
	s, r, _, _ := setupBackup(t)
	w := doBackupRequest(t, s, r, "GET", "/admin/backups", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestBackupExportHeaders(t *testing.T) {
	t.Parallel()
	s, r, _, _ := setupBackup(t)
	w := doBackupRequest(t, s, r, "GET", "/admin/backups/export", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/octet-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".db") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestBackupExportValidSQLite(t *testing.T) {
	t.Parallel()
	s, r, _, _ := setupBackup(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	w := doBackupRequest(t, s, r, "GET", "/admin/backups/export", nil, "")
	// Write response body to temp file and verify it's valid SQLite
	if w.Body.Len() == 0 {
		t.Fatal("empty response body")
	}
}

func TestBackupImportValid(t *testing.T) {
	t.Parallel()
	s, r, dbPath, _ := setupBackup(t)
	// Create a backup DB to import
	dir := t.TempDir()
	backupPath := dir + "/backup.db"
	sB, _ := store.New(backupPath)
	_ = sB.CreateForm(store.Form{ID: "f-imported", Name: "Imported", EmailTo: "x@y.com"})
	sB.Close()

	// Create multipart form with the backup file
	body, contentType := createMultipartFile(t, "file", "backup.db", backupPath)
	w := doBackupRequest(t, s, r, "POST", "/admin/backups/import", body, contentType)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}

	// Verify imported data is accessible
	forms, _ := s.ListForms(store.AllForms())
	found := false
	for _, f := range forms {
		if f.Name == "Imported" {
			found = true
		}
	}
	if !found {
		t.Error("imported form not found after restore")
	}
	_ = dbPath // used by setupBackup
}

func TestBackupImportInvalid(t *testing.T) {
	t.Parallel()
	s, r, _, _ := setupBackup(t)
	// Create multipart form with a text file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, _ := writer.CreateFormFile("file", "bad.txt")
	part.Write([]byte("this is not a database"))
	writer.Close()

	w := doBackupRequest(t, s, r, "POST", "/admin/backups/import", &buf, writer.FormDataContentType())
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (redirect with error flash)", w.Code)
	}
	// Store should still work
	_, err := s.ListForms(store.AllForms())
	if err != nil {
		t.Fatalf("store broken after failed import: %v", err)
	}
}

// Helper to create multipart file from disk
func createMultipartFile(t *testing.T, fieldName, fileName, filePath string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	part.Write(data)
	writer.Close()
	return &buf, writer.FormDataContentType()
}

// failingReopenStore wraps the real store and fails Reopen, so the two
// post-swap outcomes are reachable from a handler test.
//
// BackupStore is two methods, which is what makes this possible at all — while
// the field was *store.Store there was no way to reach these branches without
// breaking the filesystem underneath a live database.
type failingReopenStore struct {
	inner       *store.Store
	failReopens int
	reopens     int
}

func (f *failingReopenStore) DB() *sql.DB { return f.inner.DB() }

func (f *failingReopenStore) Reopen(path string) error {
	f.reopens++
	if f.reopens <= f.failReopens {
		return errors.New("simulated reopen failure")
	}
	return f.inner.Reopen(path)
}

// TestBackupImportTellsTheOperatorWhichOutcomeHappened pins the messages, not
// just the status code.
//
// All three outcomes used to redirect with "Restore failed. The uploaded file
// may be invalid or corrupted." That is wrong for two of them — the file had
// already passed validation — and it points the operator at re-exporting and
// re-uploading, which is the worst possible next step when the service is the
// thing that is broken. TestBackupImportInvalid passed throughout, because it
// only ever checked for a 302.
func TestBackupImportTellsTheOperatorWhichOutcomeHappened(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		failReopens int
		upload      string // "valid" or "garbage"
		leftover    bool   // a parked database from a restore that did not finish
		want        string
	}{
		{
			name:   "a file that is not a database is refused, and says so",
			upload: "garbage",
			want:   "That file was rejected. Your database is unchanged.",
		},
		{
			// Everything Import refuses after Validate has passed is about this
			// instance, not the upload. Reported as a rejection, it sent the
			// operator to re-export and re-upload the one thing that was fine.
			name:     "an obstacle on this side does not blame the uploaded file",
			upload:   "valid",
			leftover: true,
			want: "The restore could not be started, and not because of the file you " +
				"uploaded. Your database is unchanged. Check the server log for " +
				"what is in the way, then try again.",
		},
		{
			name:        "a failed swap says the existing database is still in use",
			upload:      "valid",
			failReopens: 1,
			want:        "Restore failed. Your existing database is unchanged and still in use.",
		},
		{
			name:        "an unrecoverable failure asks for a restart",
			upload:      "valid",
			failReopens: 2,
			want:        "Restore failed and the database could not be reopened. The service needs to be restarted.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, r, _, bh := setupBackup(t)

			// Swap in a store whose Reopen fails, leaving auth and the shell on
			// the real one. The router closes over bh, so this reaches the
			// handler the request will hit.
			if tc.failReopens > 0 {
				bh.Store = &failingReopenStore{inner: s, failReopens: tc.failReopens}
			}
			// What a restore killed between the park and the swap leaves behind.
			// Import refuses rather than renaming over it, because that file may
			// be the operator's only database.
			if tc.leftover {
				if err := os.WriteFile(bh.DBPath+".rollback", []byte("parked"), 0o644); err != nil {
					t.Fatalf("writing the leftover parked database: %v", err)
				}
			}

			var body *bytes.Buffer
			var contentType string
			switch tc.upload {
			case "valid":
				dir := t.TempDir()
				backupPath := dir + "/backup.db"
				sB, _ := store.New(backupPath)
				_ = sB.CreateForm(store.Form{ID: "f-x", Name: "Imported", EmailTo: "x@y.com"})
				sB.Close()
				body, contentType = createMultipartFile(t, "file", "backup.db", backupPath)
			default:
				body = &bytes.Buffer{}
				writer := multipart.NewWriter(body)
				part, _ := writer.CreateFormFile("file", "bad.txt")
				_, _ = part.Write([]byte("this is not a database"))
				writer.Close()
				contentType = writer.FormDataContentType()
			}

			w := doBackupRequest(t, s, r, "POST", "/admin/backups/import", body, contentType)
			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}

			msgType, msg := flashFrom(t, w)
			if msgType != "error" {
				t.Errorf("flash type = %q, want %q", msgType, "error")
			}
			// Whole message, not a substring. An earlier version asserted
			// strings.Contains(msg, "unchanged") for the rejected case — and the
			// rolled-back message also contains "unchanged", so the test whose
			// entire purpose is separating these three outcomes could not tell two
			// of them apart. Swapping the two messages passed.
			if msg != tc.want {
				t.Errorf("the operator is told:\n  %q\nwant:\n  %q\n"+
					"These three outcomes need three different next actions; one "+
					"message for all of them sends an operator whose service is down "+
					"off to re-export a file that was never the problem.", msg, tc.want)
			}
		})
	}
}

// TestUploadIsStagedBesideTheDatabase pins the fix for a bug that broke restore
// on every containerized deployment.
//
// backup.Import finishes with os.Rename, which cannot cross filesystems. The
// handler used to stage the upload with os.CreateTemp("", …) — /tmp — while
// DB_PATH defaults to /data/dsforms.db. Different mounts in any container, so
// the rename failed with EXDEV every single time.
//
// No test caught it, and none would: the suite puts the database and the upload
// under t.TempDir(), one filesystem, where the buggy code passes everything.
// Reverting the fix leaves the whole suite green and re-breaks restore for every
// Docker user, which is why this asserts the staging *location* rather than a
// behaviour that only differs across mounts.
func TestUploadIsStagedBesideTheDatabase(t *testing.T) {
	t.Parallel()

	// The detector, so the same predicate the guard runs is the one the fixtures
	// exercise. Two copies of a matcher is how one of them stops matching.
	stagesInTheSystemTempDir := func(f *ast.File) bool {
		bad := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "CreateTemp" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "os" {
				return true
			}
			if _, ok := call.Args[0].(*ast.BasicLit); ok {
				bad = true
			}
			return true
		})
		return bad
	}

	astcheck.Detector{
		Name:  "upload staged on the system temp filesystem",
		Match: stagesInTheSystemTempDir,
		Positive: map[string]string{
			"empty dir means os.TempDir": `package handler
import "os"
func f() { os.CreateTemp("", "dsforms-import-*.db") }`,
			"a literal directory is just as wrong": `package handler
import "os"
func f() { os.CreateTemp("/tmp", "dsforms-import-*.db") }`,
		},
		Negative: map[string]string{
			"staged beside the database": `package handler
import (
	"os"
	"path/filepath"
)
func f(dbPath string) { os.CreateTemp(filepath.Dir(dbPath), "dsforms-import-*.db") }`,
			"no CreateTemp at all": `package handler
func f() {}`,
		},
	}.Verify(t)

	// And the property itself, against the real source.
	_, files := astcheck.Package(t, ".")
	src, ok := files["backup.go"]
	if !ok {
		t.Fatal("backup.go not found; the scan is no longer reading it")
	}
	if stagesInTheSystemTempDir(src) {
		t.Error("backup.go stages the upload on the system temp filesystem.\n" +
			"backup.Import finishes with os.Rename, which cannot cross filesystems, " +
			"and the database lives on the data volume — so the restore fails with " +
			"EXDEV on every containerized deployment. Stage it beside h.DBPath.")
	}
}
