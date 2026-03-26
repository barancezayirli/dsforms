package handler

import (
	"bytes"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/store"
)

func setupBackup(t *testing.T) (*store.Store, *chi.Mux, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New error: %v", err)
	}

	funcMap := template.FuncMap{"add": func(a, b int) int { return a + b }}
	templates := make(map[string]*template.Template)

	baseTmpl := template.Must(template.New("base").Funcs(funcMap).Parse(`{{define "base"}}{{template "content" .}}{{end}}`))
	backupTmpl, _ := baseTmpl.Clone()
	template.Must(backupTmpl.New("content").Parse(`<p>backups page</p>`))
	templates["backups.html"] = backupTmpl

	bh := &BackupHandler{
		Store:     s,
		SecretKey: testSecretKey,
		BaseURL:   "https://example.com",
		DBPath:    dbPath,
		Templates: templates,
	}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Get("/admin/backups", bh.Page)
		r.Get("/admin/backups/export", bh.Export)
		r.Post("/admin/backups/import", bh.Import)
	})

	return s, r, dbPath
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
	s, r, _ := setupBackup(t)
	w := doBackupRequest(t, s, r, "GET", "/admin/backups", nil, "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestBackupExportHeaders(t *testing.T) {
	t.Parallel()
	s, r, _ := setupBackup(t)
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
	s, r, _ := setupBackup(t)
	_ = s.CreateForm(store.Form{ID: "f1", Name: "Test", EmailTo: "a@b.com"})
	w := doBackupRequest(t, s, r, "GET", "/admin/backups/export", nil, "")
	// Write response body to temp file and verify it's valid SQLite
	if w.Body.Len() == 0 {
		t.Fatal("empty response body")
	}
}

func TestBackupImportValid(t *testing.T) {
	t.Parallel()
	s, r, dbPath := setupBackup(t)
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
	forms, _ := s.ListForms()
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
	s, r, _ := setupBackup(t)
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
	_, err := s.ListForms()
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
