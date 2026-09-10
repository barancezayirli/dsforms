package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/barancezayirli/dsforms/internal/auth"
	"github.com/barancezayirli/dsforms/internal/backup"
	"github.com/barancezayirli/dsforms/internal/broadcaster"
	"github.com/barancezayirli/dsforms/internal/config"
	"github.com/barancezayirli/dsforms/internal/handler"
	"github.com/barancezayirli/dsforms/internal/mail"
	"github.com/barancezayirli/dsforms/internal/ratelimit"
	"github.com/barancezayirli/dsforms/internal/safe"
	"github.com/barancezayirli/dsforms/internal/screen"
	"github.com/barancezayirli/dsforms/internal/store"
	"github.com/barancezayirli/dsforms/internal/webhook"
	"github.com/go-chi/chi/v5"
)

// Compile-time checks that the concrete types satisfy the interfaces their
// consumers declare. Wiring below would catch a mismatch anyway, but only at the
// point of use and with a worse message; pinning it here also documents which
// interfaces each type is expected to serve.
//
// A new consumer interface belongs here, unless its consumer pins it itself —
// broadcaster.Store is asserted in broadcaster.go, next to the interface.
var (
	_ handler.Notifier           = (*mail.Mailer)(nil)
	_ handler.ConfirmationMailer = (*mail.Mailer)(nil)
	_ handler.DigestMailer       = (*mail.Mailer)(nil)
	_ broadcaster.Mailer         = (*mail.Mailer)(nil)

	_ handler.WebhookSender     = (*webhook.Sender)(nil)
	_ handler.BroadcastNotifier = (*broadcaster.Worker)(nil)
	_ auth.SessionStore         = (*store.Store)(nil)

	// One store, one narrow view per handler. Each declares only the methods it
	// calls, so what a handler *can* reach is what it does reach; before this,
	// every handler held *store.Store and could reach all of it.
	//
	// Asserted here rather than left to the wiring because a store method
	// renamed out from under an interface should fail with "does not implement",
	// naming the method, not with a struct-literal error forty lines further on.
	_ handler.NavCounter          = (*store.Store)(nil)
	_ handler.AdminStore          = (*store.Store)(nil)
	_ handler.QuarantineStore     = (*store.Store)(nil)
	_ handler.WaitlistStore       = (*store.Store)(nil)
	_ handler.UsersStore          = (*store.Store)(nil)
	_ handler.SubmitStore         = (*store.Store)(nil)
	_ handler.OverviewStore       = (*store.Store)(nil)
	_ handler.AuthStore           = (*store.Store)(nil)
	_ handler.BackupStore         = (*store.Store)(nil)
	_ handler.WaitlistSubmitStore = (*store.Store)(nil)
	_ handler.SearchStore         = (*store.Store)(nil)
	_ handler.DigestStore         = (*store.Store)(nil)

	// backup.Import swaps the database file underneath the process; it names the
	// two methods that takes rather than importing store at all.
	//
	// handler.BackupStore is an alias for this type, so the assertion above is
	// redundant to the compiler. It is kept deliberately: this block is the list
	// TestEveryStorageFieldIsWired reads to decide which fields a handler literal
	// must set, and dropping the alias from it silently stopped BackupHandler's
	// own Store field being required — verified by removing the wiring and
	// watching the test pass.
	_ backup.Store = (*store.Store)(nil)
)

// journalMode reports the database's actual journal mode for the sidebar card.
//
// The card used to render the literal "WAL". SQLite silently falls back to
// `delete` journaling on filesystems without shared-memory support, so the claim
// could be false on exactly the deployments where it matters — and it is shown
// on the page an operator reads while deciding whether a restore is safe.
//
// Read once: it is a property of the file, and the sidebar renders on every
// page. An unreadable answer returns empty, which renders as nothing rather than
// as a guess.
func journalMode(db *sql.DB) string {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		log.Printf("journal mode: %v", err)
		return ""
	}
	return strings.ToUpper(mode)
}

//go:embed templates/*
var templateFS embed.FS

// staticFS carries the vendored stylesheet, the interaction script, and the
// Inter variable font. They are embedded rather than fetched from a CDN for two
// reasons: dsforms ships as a single binary with no runtime network dependency,
// and the Content-Security-Policy set in newRouter is `default-src 'self'` with
// no font-src, so a remote font would be blocked by our own header.
//
//go:embed static/*
var staticFS embed.FS

// quarantineRetention is how long a held submission stays reviewable before it
// is deleted. It is passed to the handlers as RetentionDays, so the figure the
// UI states tracks this constant automatically.
const quarantineRetention = 30 * 24 * time.Hour

// version is stamped at build time with -ldflags "-X main.version=…" and shown
// in the sidebar. It stays "dev" for a plain `go build`.
var version = "dev"

// basePages extend templates/base.html and are rendered with
// ExecuteTemplate(w, "base", data). Each must define a "content" block.
//
// standalonePages are full documents that do not use the shell.
//
// Both lists are package-level so TestTemplatesParse can walk them: nothing
// else in the repo parses the real template files, and a broken one would
// otherwise surface only as a log.Fatalf at startup.
var basePages = []string{
	"dashboard.html", "form_new.html", "form_edit.html", "form_detail.html",
	"submission_detail.html", "users.html", "users_new.html", "account.html",
	"backups.html", "waitlists.html", "waitlist_new.html", "waitlist_edit.html",
	"waitlist_detail.html", "broadcast_new.html", "broadcast_detail.html",
	"quarantine.html", "rules.html", "home.html", "search.html",
}

var standalonePages = []string{"login.html", "success.html", "404.html", "500.html"}

// parseTemplates parses base.html once and clones it per page.
//
// The clone is needed because every page defines a block named "content" and
// html/template keeps one shared namespace per template set — parsing them all
// into one set would leave the last page's "content" defined for every page.
// The icon sprite is parsed into the base set so each clone inherits it.
func parseTemplates() (map[string]*template.Template, error) {
	// Chart geometry is exposed as template functions rather than hardcoded in
	// the markup, so a viewBox can never drift from the coordinate space the
	// paths in internal/handler were generated in.
	funcMap := template.FuncMap{
		"add":              func(a, b int) int { return a + b },
		"sub":              func(a, b int) int { return a - b },
		"pct":              handler.Percent,
		"SparkViewBox":     handler.SparkViewBox,
		"FormSparkViewBox": handler.FormSparkViewBox,
		"ChartViewBox":     handler.ChartViewBox,
		"BarWidth":         handler.BarWidth,
		"ruleIcon":         handler.RuleIcon,
		"ruleLabel":        handler.RuleLabel,
		"initial":          handler.Initial,
	}

	baseTmpl, err := template.New("base").Funcs(funcMap).ParseFS(templateFS,
		"templates/base.html", "templates/icons.html")
	if err != nil {
		return nil, fmt.Errorf("parse base template: %w", err)
	}

	templates := make(map[string]*template.Template, len(basePages)+len(standalonePages))
	for _, name := range basePages {
		t, err := baseTmpl.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone base template for %s: %w", name, err)
		}
		if _, err := t.ParseFS(templateFS, "templates/"+name); err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		templates[name] = t
	}

	for _, name := range standalonePages {
		// The icon sprite comes along: these pages do not extend base.html but
		// they still render icons, and a missing "icons" template is an
		// execution-time failure that template parsing alone will not catch.
		t, err := template.New(name).Funcs(funcMap).ParseFS(templateFS,
			"templates/"+name, "templates/icons.html")
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		templates[name] = t
	}

	return templates, nil
}

// assetVersion is a short content hash over every embedded static file. Pages
// append it to their asset URLs as ?v=…, which is what makes the long
// Cache-Control below safe: the URL changes whenever the bytes do, so an
// upgraded binary is picked up immediately instead of serving a cached
// stylesheet for the rest of the hour.
func assetVersion() string {
	h := sha256.New()
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	if err != nil {
		// Not fatal: fall back to a per-process value, which still busts the
		// cache on restart, just not deterministically across replicas.
		log.Printf("asset version: %v", err)
		return strconv.FormatInt(time.Now().Unix(), 36)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// staticHandler serves the embedded assets. Cached hard, because every URL
// carries the content hash from assetVersion.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static assets: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	}))
}

// healthCheck reports whether the process can actually serve. Returning an error
// makes /healthz fail.
type healthCheck func(context.Context) error

func newRouter(healthy healthCheck) *chi.Mux {
	r := chi.NewRouter()

	// Recovery middleware — must be first so it wraps all other middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				log.Printf("panic recovered: %v", rec)

				// Render the styled page, but never let the recovery path panic
				// a second time — a panic in here is unrecoverable and takes the
				// process down, and the original panic may well have come from a
				// template. The nested recover buys the plain-text fallback.
				defer func() {
					if again := recover(); again != nil {
						log.Printf("panic while rendering the error page: %v", again)
						http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					}
				}()
				serverErrorPage(w)
			}()
			next.ServeHTTP(w, r)
		})
	})

	// Security headers on all responses
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
			next.ServeHTTP(w, r)
		})
	})

	// Request body size limit (64KB); backup import has its own 100MB limit.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Backup import sets its own limit inside the handler
			if r.URL.Path == "/admin/backups/import" {
				next.ServeHTTP(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			next.ServeHTTP(w, r)
		})
	})

	// /healthz asks the database, rather than reporting that the HTTP server is
	// listening — which it always is, right up until it is useless.
	//
	// A failed backup restore could leave the process holding a closed database
	// handle: every route 500s, only a restart fixes it, and this endpoint went on
	// answering "ok" so nothing detected it. That is now the exact condition it
	// reports, since a closed handle fails to ping.
	//
	// The tradeoff is accepted deliberately: a transient database problem now
	// fails the healthcheck and an orchestrator may restart the container. The
	// state this exists to catch is only recoverable by restarting.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		body, status := "ok", http.StatusOK
		if err := healthy(ctx); err != nil {
			log.Printf("healthz: %v", err)
			body, status = "database unavailable", http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			log.Printf("healthz write error: %v", err)
		}
	})

	// Plain-text fallback so a router built without templates still answers
	// correctly; errorPages upgrades this to the styled page in main().
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte("Page not found")); err != nil {
			log.Printf("404 write error: %v", err)
		}
	})

	return r
}

// errorPages wires the styled 404 template into the router and installs the
// styled 500 renderer. Both templates existed in templates/ since before this
// redesign but were never parsed or routed.
func errorPages(r *chi.Mux, templates map[string]*template.Template) {
	render := func(w http.ResponseWriter, name string, status int, fallback string) {
		tmpl, ok := templates[name]
		if !ok {
			http.Error(w, fallback, status)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if err := tmpl.ExecuteTemplate(w, name, nil); err != nil {
			log.Printf("%s template error: %v", name, err)
		}
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		render(w, "404.html", http.StatusNotFound, "Page not found")
	})

	// Swap the plain-text fallback for the styled page now that templates are
	// parsed. The recovery middleware in newRouter is the only caller, and it
	// guards this call with its own recover(): rendering a 500 can itself panic
	// if the template is the thing that broke.
	serverErrorPage = func(w http.ResponseWriter) {
		render(w, "500.html", http.StatusInternalServerError, "Internal Server Error")
	}
}

// serverErrorPage renders the styled 500. It is a package-level hook because
// the templates are not parsed until main() runs; before then, and in tests
// that build a router directly, it falls back to plain text.
var serverErrorPage = func(w http.ResponseWriter) {
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func rateLimitMiddleware(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := handler.ExtractIP(r)
			if !l.Allow(ip) {
				if strings.Contains(r.Header.Get("Accept"), "application/json") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					json.NewEncoder(w).Encode(map[string]string{"error": "too many requests"})
					return
				}
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func runUserCLI(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dsforms user <list|add|set-password|delete> [args...]")
		os.Exit(1)
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/dsforms.db"
	}

	s, err := store.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	switch args[0] {
	case "list":
		users, err := s.ListUsers()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-20s %s\n", "USERNAME", "CREATED")
		for _, u := range users {
			fmt.Printf("%-20s %s\n", u.Username, u.CreatedAt.Format("2006-01-02 15:04:05"))
		}

	case "add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: dsforms user add <username> <password>")
			os.Exit(1)
		}
		if err := s.CreateUser(args[1], args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("User %q created.\n", args[1])

	case "set-password":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: dsforms user set-password <username> <password>")
			os.Exit(1)
		}
		u, err := s.GetUserByUsername(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := s.UpdatePassword(u.ID, args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Password updated for user %q.\n", args[1])

	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dsforms user delete <username>")
			os.Exit(1)
		}
		u, err := s.GetUserByUsername(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := s.DeleteUser(u.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("User %q deleted.\n", args[1])

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: user %s\n", args[0])
		os.Exit(1)
	}
}

func runBackupCLI(args []string) {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "Usage: dsforms backup create")
		os.Exit(1)
	}

	backupDir := os.Getenv("BACKUP_LOCAL_DIR")
	if backupDir == "" {
		fmt.Fprintln(os.Stderr, "Error: BACKUP_LOCAL_DIR is not set")
		os.Exit(1)
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/dsforms.db"
	}

	s, err := store.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	exportPath, err := backup.Export(s.DB())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating backup: %v\n", err)
		os.Exit(1)
	}

	// Move to backup dir with timestamp filename
	timestamp := time.Now().Format("2006-01-02-150405")
	destPath := filepath.Join(backupDir, fmt.Sprintf("dsforms-backup-%s.db", timestamp))

	// Ensure backup dir exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		os.Remove(exportPath)
		fmt.Fprintf(os.Stderr, "Error creating backup directory: %v\n", err)
		os.Exit(1)
	}

	if err := os.Rename(exportPath, destPath); err != nil {
		// If rename fails (cross-device), try copy
		data, readErr := os.ReadFile(exportPath)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error reading backup: %v\n", readErr)
			os.Exit(1)
		}
		if writeErr := os.WriteFile(destPath, data, 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "Error writing backup to %s: %v\nTemp file preserved at: %s\n", destPath, writeErr, exportPath)
			os.Exit(1)
		}
		os.Remove(exportPath) // Only delete after successful write
	}

	fmt.Printf("Backup created: %s\n", destPath)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "user":
			runUserCLI(os.Args[2:])
			return
		case "backup":
			runBackupCLI(os.Args[2:])
			return
		}
	}

	cfg := config.Load()

	s, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer s.Close()

	// Clean expired sessions periodically
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			// Guarded per tick, not per goroutine: one bad sweep should cost one
			// hour, not the HTTP server. A panic here would otherwise take the
			// process down and read as a crash loop under Docker's restart policy.
			safe.Do("session cleanup", func() {
				if err := s.CleanExpiredSessions(); err != nil {
					log.Printf("session cleanup error: %v", err)
				}
			})
		}
	}()

	// Quarantine retention. Held submissions are reviewable for 30 days and then
	// deleted — the promise the UI makes, and the reason holding spam does not
	// grow the database without bound. Sweeps once at startup so an instance
	// that is restarted more often than daily still expires things.
	go func() {
		purge := func() {
			safe.Do("quarantine purge", func() {
				n, err := s.PurgeHeldOlderThan(time.Now().UTC().Add(-quarantineRetention))
				if err != nil {
					log.Printf("quarantine purge error: %v", err)
					return
				}
				if n > 0 {
					log.Printf("quarantine: purged %d submission(s) held longer than %s", n, quarantineRetention)
				}
			})
		}
		purge()
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			purge()
		}
	}()

	templates, err := parseTemplates()
	if err != nil {
		log.Fatalf("failed to parse templates: %v", err)
	}

	var mailer handler.Notifier
	var sendMailer *mail.Mailer
	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" {
		sendMailer = &mail.Mailer{
			Host:    cfg.SMTPHost,
			Port:    cfg.SMTPPort,
			User:    cfg.SMTPUser,
			Pass:    cfg.SMTPPass,
			From:    cfg.SMTPFrom,
			BaseURL: cfg.BaseURL,
		}
		mailer = sendMailer
		log.Printf("email notifications enabled (SMTP: %s)", cfg.SMTPHost)
	} else {
		log.Println("email notifications disabled (SMTP_HOST or SMTP_FROM not set)")
	}

	webhookSender := webhook.NewSender()

	worker := &broadcaster.Worker{
		Store:       s,
		BatchSize:   50,
		MaxAttempts: cfg.BroadcastMaxAttempts,
		Throttle:    time.Duration(cfg.BroadcastThrottleMs) * time.Millisecond,
	}
	if sendMailer != nil {
		worker.Mailer = sendMailer
	}
	worker.Start()
	if sendMailer == nil {
		log.Println("broadcast worker started without SMTP — broadcasts will be marked failed until SMTP_HOST/SMTP_FROM are configured")
	}

	// One screener for the process: it carries the repeat-IP tally, which is the
	// only state the hold/accept decision keeps.
	screener := screen.New(10000)

	// A rule stored under an older normalisation can be permanently unmatchable,
	// and a block rule in that state fails open while still appearing on the
	// rules screen. Said once at boot so it is visible without anyone visiting
	// that page; the page itself names the individual rules.
	if rules, err := s.ListFilterRules(); err != nil {
		log.Printf("startup: could not check filter rules: %v", err)
	} else if problems := screen.CheckRules(rules); len(problems) > 0 {
		log.Printf("⚠  %d filter rule(s) can never match and are protecting nothing — see /admin/rules", len(problems))
		for _, p := range problems {
			log.Printf("   rule %s (%q): %s", p.RuleID, p.Value, p.Reason)
		}
	}

	submitHandler := &handler.SubmitHandler{
		Store:            s,
		Notifier:         mailer,
		Webhook:          webhookSender,
		BaseURL:          cfg.BaseURL,
		Screener:         screener,
		DefaultThreshold: cfg.SpamThreshold,
	}

	limiter := ratelimit.NewLimiter(cfg.RateBurst, cfg.RatePerMinute, time.Now)
	limiter.StartCleanup(10*time.Minute, 30*time.Minute)

	loginGuard := ratelimit.NewLoginGuard(5, 15*time.Minute, time.Now)
	loginGuard.StartCleanup(30*time.Minute, 30*time.Minute)

	// Every admin handler shares the same secret, templates and shell state, so
	// they share one Base rather than repeating the same field list in every
	// handler, where the copies could drift apart.
	//
	// Base carries no store handle. It needs the nav badge counts and nothing
	// else, and holding the whole store here would have handed every embedding
	// handler a second, unrestricted route to the database beside the narrow one
	// it declares.
	base := handler.Base{
		Nav:       s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		DBPath:    cfg.DBPath,
		Journal:   journalMode(s.DB()),
		Version:   version,
		AssetVer:  assetVersion(),
		Templates: templates,
	}

	authHandler := &handler.AuthHandler{Base: base, Store: s, LoginGuard: loginGuard}
	overviewHandler := &handler.OverviewHandler{
		Base:          base,
		Store:         s,
		Limiter:       limiter,
		RateBurst:     cfg.RateBurst,
		RatePerMinute: cfg.RatePerMinute,
		RetentionDays: int(quarantineRetention / (24 * time.Hour)),
	}
	searchHandler := &handler.SearchHandler{Base: base, Store: s}
	quarantineHandler := &handler.QuarantineHandler{
		Base:             base,
		Store:            s,
		Notifier:         mailer,
		Webhook:          webhookSender,
		RetentionDays:    int(quarantineRetention / (24 * time.Hour)),
		DefaultThreshold: cfg.SpamThreshold,
	}
	adminHandler := &handler.AdminHandler{Base: base, Store: s, Webhook: webhookSender}
	usersHandler := &handler.UsersHandler{Base: base, Store: s}
	backupHandler := &handler.BackupHandler{Base: base, Store: s}

	waitlistSubmitHandler := &handler.WaitlistSubmitHandler{
		Store:   s,
		BaseURL: cfg.BaseURL,
	}
	if sendMailer != nil {
		waitlistSubmitHandler.Mailer = sendMailer
	}

	waitlistHandler := &handler.WaitlistHandler{Base: base, Store: s, Broadcaster: worker}

	// Daily quarantine digest. Opt-in via DIGEST_TO, and silently inert without
	// SMTP — the only background timer this redesign adds.
	if sendMailer != nil && cfg.DigestTo != "" {
		digest := &handler.Digest{
			Store:     s,
			Mailer:    sendMailer,
			To:        cfg.DigestTo,
			BaseURL:   cfg.BaseURL,
			Retention: int(quarantineRetention / (24 * time.Hour)),
		}
		digest.Start(24 * time.Hour)
		log.Printf("quarantine digest enabled (daily to %s)", cfg.DigestTo)
	} else if cfg.DigestTo != "" {
		// Otherwise an operator who sets DIGEST_TO waits days for a mail that
		// was never going to arrive, with nothing in the log to explain it.
		log.Printf("quarantine digest disabled: DIGEST_TO is set but SMTP is not configured")
	}

	r := newRouter(func(ctx context.Context) error {
		return s.DB().PingContext(ctx)
	})
	errorPages(r, templates)
	r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", submitHandler.Handle)
	r.With(rateLimitMiddleware(limiter)).Post("/w/{waitlistID}", waitlistSubmitHandler.Handle)

	// Embedded CSS, JS and the Inter woff2. Public and unauthenticated: the
	// login page needs the stylesheet before anyone has a session.
	r.Handle("/static/*", staticHandler())

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	r.Get("/admin/login", authHandler.LoginPage)
	r.Post("/admin/login", authHandler.LoginSubmit)
	r.Get("/success", adminHandler.Success)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Post("/admin/logout", authHandler.Logout)
		r.Get("/admin", overviewHandler.Page)
		r.Get("/admin/forms", adminHandler.Dashboard)
		r.Get("/admin/forms/new", adminHandler.NewFormPage)
		r.Post("/admin/forms/new", adminHandler.CreateForm)
		r.Get("/admin/forms/{id}/edit", adminHandler.EditFormPage)
		r.Post("/admin/forms/{id}/edit", adminHandler.EditForm)
		r.Post("/admin/forms/{id}/delete", adminHandler.DeleteForm)
		r.Get("/admin/forms/{id}", adminHandler.FormDetail)
		r.Post("/admin/forms/{id}/read-all", adminHandler.MarkAllRead)
		r.Post("/admin/forms/{id}/submissions/bulk-delete", adminHandler.BulkDeleteSubmissions)
		r.Post("/admin/forms/{id}/test-webhook", adminHandler.TestWebhook)
		r.Get("/admin/forms/{id}/export", adminHandler.ExportCSV)
		r.Get("/admin/forms/{formID}/submissions/{subID}", adminHandler.SubmissionDetail)
		r.Post("/admin/submissions/{id}/read", adminHandler.MarkRead)
		r.Post("/admin/submissions/{id}/delete", adminHandler.DeleteSubmission)
		r.Get("/admin/search", searchHandler.Page)
		r.Get("/admin/quarantine", quarantineHandler.Page)
		r.Post("/admin/quarantine/{id}/restore", quarantineHandler.Restore)
		r.Post("/admin/quarantine/{id}/report", quarantineHandler.Report)
		r.Post("/admin/quarantine/delete", quarantineHandler.Delete)
		r.Post("/admin/quarantine/empty", quarantineHandler.Empty)
		r.Get("/admin/rules", quarantineHandler.RulesPage)
		r.Post("/admin/rules", quarantineHandler.AddRule)
		r.Post("/admin/rules/{id}/delete", quarantineHandler.DeleteRule)
		r.Get("/admin/waitlists", waitlistHandler.List)
		r.Get("/admin/waitlists/new", waitlistHandler.NewPage)
		r.Post("/admin/waitlists/new", waitlistHandler.Create)
		r.Get("/admin/waitlists/{id}/edit", waitlistHandler.EditPage)
		r.Post("/admin/waitlists/{id}/edit", waitlistHandler.Edit)
		r.Post("/admin/waitlists/{id}/delete", waitlistHandler.Delete)
		r.Get("/admin/waitlists/{id}", waitlistHandler.Detail)
		r.Get("/admin/waitlists/{id}/export", waitlistHandler.ExportCSV)
		r.Post("/admin/waitlists/{id}/entries/{entryID}/delete", waitlistHandler.DeleteEntry)
		r.Get("/admin/waitlists/{id}/broadcast", waitlistHandler.BroadcastPage)
		r.Post("/admin/waitlists/{id}/broadcast", waitlistHandler.CreateBroadcast)
		r.Get("/admin/waitlists/{id}/broadcasts/{bid}", waitlistHandler.BroadcastDetail)
		r.Get("/admin/users", usersHandler.ListUsers)
		r.Get("/admin/users/new", usersHandler.NewUserPage)
		r.Post("/admin/users/new", usersHandler.CreateUser)
		r.Post("/admin/users/{id}/delete", usersHandler.DeleteUser)
		r.Get("/admin/account", usersHandler.AccountPage)
		r.Post("/admin/account/password", usersHandler.UpdatePassword)
		r.Get("/admin/backups", backupHandler.Page)
		r.Get("/admin/backups/export", backupHandler.Export)
		r.Post("/admin/backups/import", backupHandler.Import)
	})

	log.Printf("starting server on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
