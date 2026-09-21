package handler

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/barancezayirli/dsforms/internal/backup"
	"github.com/barancezayirli/dsforms/internal/flash"
)

// BackupStore is what the backup screen needs from storage: the handle to snapshot
// from, and the way to reopen after the file underneath has been replaced.
//
// An alias rather than a second declaration. backup.Import needs the identical
// pair, and this handler's only reason to hold them is to hand them to it — so
// writing the methods out again here produced two copies of one contract that
// happened to typecheck only while they stayed byte-identical. The alias makes
// them the same type, which is what they always were.
//
// DB() is a real hole in the narrowing, not a clean seam: whoever holds it has
// unrestricted SQL, so this constrains who may reach the database rather than
// what they may do with it.
type BackupStore = backup.Store

// BackupHandler handles backup export and import.
type BackupHandler struct {
	Base
	Store BackupStore
}

// backupPageData holds the data passed to backups.html.
type backupPageData struct {
	PageData
}

// Page renders the backups management page.
func (h *BackupHandler) Page(w http.ResponseWriter, r *http.Request) {

	data := backupPageData{
		PageData: h.Shell(w, r, "Backups", "backups"),
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

	// The upload is staged BESIDE the live database, not in the system temp
	// directory.
	//
	// backup.Import finishes with os.Rename, which cannot cross filesystems. The
	// old os.CreateTemp("", …) put the file in /tmp while DB_PATH defaults to
	// /data/dsforms.db — different mounts in any container, so the rename failed
	// with EXDEV on every restore. That was not an edge case, it was the default
	// deployment, and it was live: at the feature's first commit the form field
	// name still matched, so restores reached the rename and failed there. The
	// later field-name mismatch only hid it for the last stretch.
	tmp, err := os.CreateTemp(filepath.Dir(h.DBPath), "dsforms-import-*.db")
	if err != nil {
		log.Printf("backup import: create temp file beside %s: %v", h.DBPath, err)
		flash.Set(w, h.SecretKey, "error",
			"Could not stage the upload next to the database. Your database is unchanged.")
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
		// Three outcomes needing three different responses. One message for all
		// of them told an operator whose database was fine that their file was
		// corrupt, and an operator whose service was down the same thing — so the
		// obvious next step, re-export and re-upload, was aimed at a process that
		// could no longer answer.
		log.Printf("backup import error: %v", err)
		switch {
		case errors.Is(err, backup.ErrUnavailable):
			flash.Set(w, h.SecretKey, "error",
				"Restore failed and the database could not be reopened. "+
					"The service needs to be restarted.")
		case errors.Is(err, backup.ErrRolledBack):
			flash.Set(w, h.SecretKey, "error",
				"Restore failed. Your existing database is unchanged and still in use.")
		case errors.Is(err, backup.ErrNotAttempted):
			// Before ErrRejected, which this also is: nothing was touched. But
			// the obstacle is on this side — either the upload was never reached
			// or SQLite said the failure reading it was the machine's — so "that
			// file was rejected" would send the operator to re-export and
			// re-upload the one thing that was not the problem.
			flash.Set(w, h.SecretKey, "error",
				"The restore could not be started, and not because of the file you "+
					"uploaded. Your database is unchanged. Check the server log for "+
					"what is in the way, then try again.")
		case errors.Is(err, backup.ErrRejected):
			flash.Set(w, h.SecretKey, "error",
				"That file was rejected. Your database is unchanged.")
		default:
			// Never the reassuring message. AGENT.md §4: in a switch over a closed
			// set the safe outcome is not default. Every return from Import carries
			// a sentinel today, so this is unreachable — which is exactly when the
			// next unsentinelled return starts telling an operator their database
			// is fine while nobody has established that.
			flash.Set(w, h.SecretKey, "error",
				"Restore failed in an unexpected way. Check the server log and verify "+
					"the database before retrying.")
		}
		http.Redirect(w, r, "/admin/backups", http.StatusFound)
		return
	}

	flash.Set(w, h.SecretKey, "success", "Database restored successfully.")
	http.Redirect(w, r, "/admin/backups", http.StatusFound)
}
