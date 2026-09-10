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

// Base is the state every admin handler needs, and every admin handler embeds
// it.
//
// These fields were previously repeated field-for-field in each handler. The
// sidebar shell needs several more values on every page (nav badge counts, the
// database card, the version chip), and copying those into every handler — and
// into every per-page data struct — is how they drift apart.
type Base struct {
	Store     *store.Store
	SecretKey string
	BaseURL   string
	DBPath    string
	Version   string
	AssetVer  string // content hash appended to /static URLs for cache busting
	Templates map[string]*template.Template
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
	Nav         store.NavCounts
	DB          DBStatus

	// Degraded means some of the data on this page could not be loaded, so the
	// page can say so instead of presenting the gaps as facts.
	//
	// Each individual log-and-continue is defensible — one dead panel should not
	// 500 a whole screen — but together they render a fully-formed dashboard
	// reading zero everywhere, pixel-identical to a fresh install.
	//
	// Every handler that degrades a value sets this, and Shell sets it when its
	// own NavCounts query fails. TestNoStructShadowsAnEmbeddedField is what keeps
	// it connected: declaring a field of this name on a struct that embeds
	// PageData silently disconnects the banner, because Go promotes the shallower
	// field and html/template resolves the same way.
	//
	// Fragment responses do not go through base.html, so they render the
	// "degraded-notice" define directly.
	Degraded bool
}

// navGroups maps a sidebar item to the group heading above it. The header
// breadcrumb shows that group over the page title, so deriving it here keeps the
// two in step instead of asking every call site to remember which group its page
// belongs to.
var navGroups = map[string]string{
	"home":       "Overview",
	"forms":      "Collect",
	"waitlists":  "Collect",
	"quarantine": "Moderate",
	"rules":      "Moderate",
	"users":      "System",
	"account":    "System",
	"backups":    "System",
}

// Shell builds the shell state for one request.
//
// Named Shell rather than Page because Base is embedded into every admin
// handler and BackupHandler already has a Page method — an embedded method
// with that name would be silently shadowed by it.
//
// Reading the flash consumes it: flash.Get clears the cookie on a successfully
// validated one. Calling Shell twice does not double-consume — the second call
// re-reads the unmodified request — but calling it on a path that then redirects
// instead of rendering silently eats the operator's message. Call it on paths
// that render.
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
			// A zeroed badge is indistinguishable from an empty queue, so the
			// failure is recorded rather than only logged.
			log.Printf("page: nav counts: %v", err)
			data.Degraded = true
		} else {
			data.Nav = counts
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
