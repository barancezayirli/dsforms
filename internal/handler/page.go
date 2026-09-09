package handler

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/store"
)

// Base is the state every admin handler needs. AdminHandler, AuthHandler,
// UsersHandler, BackupHandler and WaitlistHandler each embed it.
//
// These five fields were previously repeated field-for-field across all five
// handlers. The sidebar shell needs several more values on every page (nav
// badge counts, the database card, the version chip), and copying those into
// five more places — and into thirteen per-page data structs — is how the
// counts would drift apart. One struct, embedded.
type Base struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	DBPath    string
	Version   string
	AssetVer  string // content hash appended to /static URLs for cache busting
	Templates map[string]*template.Template
}

// NavCounts are the sidebar badge numbers.
type NavCounts struct {
	Unread   int
	Held     int
	Waitlist int
}

// DBStatus fills the sidebar's database card.
type DBStatus struct {
	Name    string
	Size    string
	Journal string
}

// PageData is the shell state every page rendered through base.html needs.
// Page-specific data structs embed it.
type PageData struct {
	Title       string
	Active      string // which sidebar item is current
	Crumb       string // the small line above the title in the header
	Query       string // current search term, so the header input keeps it
	Version     string
	AssetVer    string
	CurrentUser store.User
	Flash       *FlashData
	Nav         NavCounts
	DB          DBStatus
}

// navGroups maps a sidebar item to the group heading above it. The header
// breadcrumb shows that group over the page title, so deriving it here keeps
// the two in step instead of asking sixteen call sites to remember which group
// their page belongs to.
var navGroups = map[string]string{
	"home":       "Overview",
	"forms":      "Collect",
	"waitlists":  "Collect",
	"quarantine": "Moderate",
	"rules":      "Moderate",
	"users":      "System",
	"backups":    "System",
}

// Shell builds the shell state for one request.
//
// Named Shell rather than Page because Base is embedded into every admin
// handler and BackupHandler already has a Page method — an embedded method
// with that name would be silently shadowed by it.
//
// Reading the flash has the side effect of clearing its cookie, so this must be
// called exactly once per response — the same contract the individual handlers
// already had with flash.Get.
//
// A failure to read the nav counts or stat the database is logged and left at
// zero rather than returned: a sidebar badge is not worth turning a working
// page into a 500.
func (b *Base) Shell(w http.ResponseWriter, r *http.Request, title, active string) PageData {
	user, _ := auth.UserFromContext(r.Context())
	flashType, flashMsg := flash.Get(r, w, b.SecretKey)

	data := PageData{
		Title:       title,
		Active:      active,
		Crumb:       navGroups[active],
		Query:       r.URL.Query().Get("q"),
		Version:     b.Version,
		AssetVer:    b.AssetVer,
		CurrentUser: user,
		Flash:       newFlash(flashType, flashMsg),
		DB:          b.dbStatus(),
	}

	if b.Store != nil {
		counts, err := b.Store.NavCounts()
		if err != nil {
			log.Printf("page: nav counts: %v", err)
		} else {
			data.Nav = NavCounts{Unread: counts.Unread, Held: counts.Held, Waitlist: counts.Waitlist}
		}
	}
	return data
}

// dbStatus describes the database file for the sidebar card.
func (b *Base) dbStatus() DBStatus {
	status := DBStatus{Name: "dsforms.db", Journal: "WAL"}
	if b.DBPath == "" {
		return status
	}
	status.Name = filepath.Base(b.DBPath)

	info, err := os.Stat(b.DBPath)
	if err != nil {
		// An in-memory database in tests, or a path we cannot stat. Say nothing
		// rather than something wrong.
		return status
	}
	status.Size = humanBytes(info.Size())
	return status
}

// humanBytes formats a file size for display. Deliberately coarse — the sidebar
// card is a glance, not a metric.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// Render executes a page template through the shell and handles the error in
// the one way every handler already handled it.
func (b *Base) Render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := b.Templates[name]
	if !ok {
		log.Printf("render: no template %q", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("%s template error: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
