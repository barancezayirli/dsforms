package handler

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/flash"
	"github.com/barancezayirli/dsforms/internal/store"
)

// NavCounter supplies the sidebar badge counts, and is the only thing the shell
// needs from storage.
//
// One method rather than a store handle: Base is embedded into every admin
// handler, so a *store.Store here was a second, wider route to the database from
// handlers that had already declared the narrow one they use.
type NavCounter interface {
	NavCounts() (store.NavCounts, error)
}

// Base is the state every admin handler needs, and every admin handler embeds
// it.
//
// These fields were previously repeated field-for-field in each handler. The
// sidebar shell needs several more values on every page (nav badge counts, the
// database card, the version chip), and copying those into every handler — and
// into every per-page data struct — is how they drift apart.
type Base struct {
	Nav       NavCounter
	SecretKey string
	BaseURL   string
	DBPath    string
	Version   string
	AssetVer  string // content hash appended to /static URLs for cache busting

	// Journal is the database's actual journal mode, read once at startup.
	//
	// It used to be the string literal "WAL", rendered on every admin page as
	// fact. SQLite silently falls back to `delete` journaling on filesystems
	// without shared-memory support — a network mount, some container volumes —
	// and the sidebar would go on claiming WAL. That is the same defect as the
	// hardcoded "ok" that /healthz used to return, on the page an operator reads
	// while deciding whether a restore is safe.
	//
	// Read at startup rather than per request: it is a property of the file, it
	// costs a query, and the sidebar renders on every page. Empty renders as
	// unknown rather than as a guess.
	Journal   string
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
// A failure to read the nav counts is logged rather than returned — a sidebar
// badge is not worth turning a working page into a 500 — and sets Degraded, so
// the page says the counts are unreliable instead of showing zeroes as though
// they were the answer.
//
// The database card is different: dbStatus swallows a failed stat silently and
// does not set Degraded, on the reasoning recorded there.
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

	if isNil(b.Nav) {
		// Unwired rather than failed, but the operator sees the same thing: a
		// sidebar reading zero everywhere. Nav is an interface, so it is nil
		// whenever a Base literal simply omits the field — which compiles — and
		// silence here would render that as an empty queue.
		log.Printf("page: no nav counter wired")
		data.Degraded = true
		return data
	}
	counts, err := b.Nav.NavCounts()
	if err != nil {
		// A zeroed badge is indistinguishable from an empty queue, so the
		// failure is recorded rather than only logged.
		log.Printf("page: nav counts: %v", err)
		data.Degraded = true
		return data
	}
	data.Nav = counts
	return data
}

// isNil reports whether an interface holds nothing usable.
//
// `b.Nav == nil` is false for a non-nil interface holding a nil pointer — the
// classic Go trap — so a Base wired with a (*store.Store)(nil) sailed past the
// guard and panicked inside NavCounts on the first request. AGENT.md §4: never
// panic during a request. Unreachable from main, which exits if the store cannot
// open, but the guard exists precisely for the wiring nobody checked.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// dbStatus describes the database file for the sidebar card.
func (b *Base) dbStatus() DBStatus {
	status := DBStatus{Name: "dsforms.db", Journal: b.Journal}
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

	// The write-ahead log counts. In WAL mode a commit lands in dsforms.db-wal
	// and stays there until a checkpoint folds it into the main file, so on a
	// young database the main file is not the database. Measured on a running
	// instance after 40 submissions: main file 4,096 bytes, WAL 2,084,752. This
	// card read "4.0 KB". It exists for an operator asking whether their data is
	// still there, and it was answering no.
	//
	// What this reports is disk footprint, and it is an upper bound rather than
	// the size of the data. Two reasons, both measured rather than assumed:
	//
	//   - SQLite's automatic checkpoint reuses the WAL instead of truncating it,
	//     so the file stays at its high-water mark. After a PASSIVE checkpoint
	//     moved every frame (busy=0 log=449 checkpointed=449), a 4,165,352-byte
	//     WAL was still 4,165,352 bytes beside a 606 KB database. Only
	//     wal_checkpoint(TRUNCATE) zeroes it, and this codebase issues that only
	//     during a restore.
	//   - A WAL holds superseded page images, so `Download snapshot` legitimately
	//     produces a much smaller file: VACUUM INTO writes a compacted copy.
	//
	// That asymmetry is deliberate. Over-reporting disk use is the mild error;
	// under-reporting is the one that tells an operator their submissions are
	// gone, which is what this replaces. Reporting the compacted size instead
	// would mean a VACUUM on every page render.
	//
	// A missing -wal contributes zero rather than suppressing the figure, so a
	// non-WAL database still reports its size.
	//
	// -shm is not counted because it is a reconstructable shared-memory index
	// rather than data — it is rebuilt on open and carries nothing a restore
	// would need. Its fixed ~32 KB would also read as 36 KB for an empty
	// database.
	total := info.Size()
	if wal, err := os.Stat(b.DBPath + "-wal"); err == nil {
		total += wal.Size()
	}

	status.Size = humanBytes(total)
	return status
}

// humanBytes formats a file size for display. Deliberately coarse — the sidebar
// card is a glance, not a metric.
func humanBytes(n int64) string {
	const unit = 1024
	// The suffixes this can name. Indexing it was previously unguarded, and
	// humanBytes(1<<50) panicked with "index out of range [4] with length 4" —
	// in a function called on every admin page render. A petabyte SQLite file is
	// not a real scenario, but an unguarded index in a request path is worth two
	// lines regardless: the failure mode is a 500 on every page, not a wrong
	// number on one.
	const suffixes = "KMGT"
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < len(suffixes)-1; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), suffixes[exp])
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
