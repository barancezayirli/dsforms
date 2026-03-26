package handler

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/backup"
	"github.com/youruser/dsforms/internal/flash"
	"github.com/youruser/dsforms/internal/store"
)

// BackupHandler handles backup export and import.
type BackupHandler struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	DBPath    string
	Templates map[string]*template.Template
}

// backupPageData holds the data passed to backups.html.
type backupPageData struct {
	Title       string
	Active      string
	CurrentUser store.User
	Flash       *FlashData
}

// Page renders the backups management page.
func (h *BackupHandler) Page(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, h.SecretKey)

	data := backupPageData{
		Title:       "Backups",
		Active:      "backups",
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
	}

	if err := h.Templates["backups.html"].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("backups template error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// Export streams the database as a downloadable backup file.
func (h *BackupHandler) Export(w http.ResponseWriter, r *http.Request) {
	tmpPath, err := backup.Export(h.Store.DB())
	if err != nil {
		log.Printf("backup export error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpPath)

	f, err := os.Open(tmpPath)
	if err != nil {
		log.Printf("backup export: open temp file: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Printf("backup export: stat error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("dsforms-backup-%s.db", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))

	if _, err := io.Copy(w, f); err != nil {
		log.Printf("backup export: stream to response: %v", err)
	}
}

// Import handles uploading a .db file and restoring it as the live database.
func (h *BackupHandler) Import(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		log.Printf("backup import: parse multipart: %v", err)
		flash.Set(w, h.SecretKey, "error", "Failed to read uploaded file.")
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		log.Printf("backup import: get form file: %v", err)
		flash.Set(w, h.SecretKey, "error", "No file uploaded.")
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}
	defer file.Close()

	// Write uploaded file to a temp location.
	tmp, err := os.CreateTemp("", "dsforms-import-*.db")
	if err != nil {
		log.Printf("backup import: create temp file: %v", err)
		flash.Set(w, h.SecretKey, "error", "Internal error during restore.")
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		log.Printf("backup import: write temp file: %v", err)
		flash.Set(w, h.SecretKey, "error", "Failed to save uploaded file.")
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}
	tmp.Close()

	if err := backup.Import(h.Store, tmpPath, h.DBPath); err != nil {
		log.Printf("backup import error: %v", err)
		flash.Set(w, h.SecretKey, "error", "Restore failed. The uploaded file may be invalid or corrupted.")
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}

	flash.Set(w, h.SecretKey, "success", "Database restored successfully.")
	http.Redirect(w, r, "/admin/backups", http.StatusFound)
}
