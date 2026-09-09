package main

import (
	"crypto/sha256"
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

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/backup"
	"github.com/youruser/dsforms/internal/broadcaster"
	"github.com/youruser/dsforms/internal/config"
	"github.com/youruser/dsforms/internal/handler"
	"github.com/youruser/dsforms/internal/mail"
	"github.com/youruser/dsforms/internal/ratelimit"
	"github.com/youruser/dsforms/internal/spam"
	"github.com/youruser/dsforms/internal/store"
	"github.com/youruser/dsforms/internal/webhook"
)

// Compile-time checks that *mail.Mailer satisfies the consumer interfaces it is wired into.
var (
	_ handler.Notifier           = (*mail.Mailer)(nil)
	_ handler.ConfirmationMailer = (*mail.Mailer)(nil)
	_ broadcaster.Mailer         = (*mail.Mailer)(nil)
)

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
// is deleted. The quarantine UI states this figure; changing one means changing
// both.
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
	"quarantine.html", "rules.html", "home.html",
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
		t, err := template.New(name).Funcs(funcMap).ParseFS(templateFS, "templates/"+name)
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

func newRouter() *chi.Mux {
	r := chi.NewRouter()

	// Recovery middleware — must be first so it wraps all other middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Printf("panic recovered: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					if _, werr := w.Write([]byte("Internal Server Error")); werr != nil {
						log.Printf("recovery write error: %v", werr)
					}
				}
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

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("healthz write error: %v", err)
		}
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte("Page not found")); err != nil {
			log.Printf("404 write error: %v", err)
		}
	})

	return r
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
			if err := s.CleanExpiredSessions(); err != nil {
				log.Printf("session cleanup error: %v", err)
			}
		}
	}()

	// Quarantine retention. Held submissions are reviewable for 30 days and then
	// deleted — the promise the UI makes, and the reason holding spam does not
	// grow the database without bound. Sweeps once at startup so an instance
	// that is restarted more often than daily still expires things.
	go func() {
		purge := func() {
			n, err := s.PurgeHeldOlderThan(time.Now().UTC().Add(-quarantineRetention))
			if err != nil {
				log.Printf("quarantine purge error: %v", err)
				return
			}
			if n > 0 {
				log.Printf("quarantine: purged %d submission(s) held longer than %s", n, quarantineRetention)
			}
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

	submitHandler := &handler.SubmitHandler{
		Store:            s,
		Notifier:         mailer,
		Webhook:          webhookSender,
		BaseURL:          cfg.BaseURL,
		Tracker:          spam.NewTracker(10000),
		DefaultThreshold: cfg.SpamThreshold,
	}

	limiter := ratelimit.NewLimiter(cfg.RateBurst, cfg.RatePerMinute, time.Now)
	limiter.StartCleanup(10*time.Minute, 30*time.Minute)

	loginGuard := ratelimit.NewLoginGuard(5, 15*time.Minute, time.Now)
	loginGuard.StartCleanup(30*time.Minute, 30*time.Minute)

	// Every admin handler shares the same store, secret, templates and shell
	// state, so they share one Base rather than repeating five identical field
	// lists that could drift apart.
	base := handler.Base{
		Store:     s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		DBPath:    cfg.DBPath,
		Version:   version,
		AssetVer:  assetVersion(),
		Templates: templates,
	}

	authHandler := &handler.AuthHandler{Base: base, LoginGuard: loginGuard}
	overviewHandler := &handler.OverviewHandler{
		Base:          base,
		Limiter:       limiter,
		RateBurst:     cfg.RateBurst,
		RatePerMinute: cfg.RatePerMinute,
		RetentionDays: int(quarantineRetention / (24 * time.Hour)),
	}
	quarantineHandler := &handler.QuarantineHandler{
		Base:             base,
		Notifier:         mailer,
		RetentionDays:    int(quarantineRetention / (24 * time.Hour)),
		DefaultThreshold: cfg.SpamThreshold,
	}
	adminHandler := &handler.AdminHandler{Base: base, Webhook: webhookSender}
	usersHandler := &handler.UsersHandler{Base: base}
	backupHandler := &handler.BackupHandler{Base: base}

	waitlistSubmitHandler := &handler.WaitlistSubmitHandler{
		Store:   s,
		BaseURL: cfg.BaseURL,
	}
	if sendMailer != nil {
		waitlistSubmitHandler.Mailer = sendMailer
	}

	waitlistHandler := &handler.WaitlistHandler{Base: base, Broadcaster: worker}

	r := newRouter()
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
