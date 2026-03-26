package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
	"github.com/youruser/dsforms/internal/backup"
	"github.com/youruser/dsforms/internal/config"
	"github.com/youruser/dsforms/internal/handler"
	"github.com/youruser/dsforms/internal/mail"
	"github.com/youruser/dsforms/internal/ratelimit"
	"github.com/youruser/dsforms/internal/store"
)

//go:embed templates/*
var templateFS embed.FS

func newRouter() *chi.Mux {
	r := chi.NewRouter()

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

	// Request body size limit (64KB)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			fmt.Fprintf(os.Stderr, "Error: user not found\n")
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
			fmt.Fprintf(os.Stderr, "Error: user not found\n")
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
		os.Remove(exportPath)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", readErr)
			os.Exit(1)
		}
		if writeErr := os.WriteFile(destPath, data, 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", writeErr)
			os.Exit(1)
		}
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

	// Parse base template once, then clone it for each page that extends it.
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}
	baseTmpl, err := template.New("base").Funcs(funcMap).ParseFS(templateFS, "templates/base.html")
	if err != nil {
		log.Fatalf("failed to parse base template: %v", err)
	}

	templates := make(map[string]*template.Template)
	for _, name := range []string{"dashboard.html", "form_new.html", "form_edit.html", "form_detail.html", "submission_detail.html", "users.html", "users_new.html", "account.html", "backups.html"} {
		t, err := baseTmpl.Clone()
		if err != nil {
			log.Fatalf("failed to clone base template: %v", err)
		}
		_, err = t.ParseFS(templateFS, "templates/"+name)
		if err != nil {
			log.Fatalf("failed to parse template %s: %v", name, err)
		}
		templates[name] = t
	}

	for _, name := range []string{"login.html", "success.html"} {
		t, err := template.ParseFS(templateFS, "templates/"+name)
		if err != nil {
			log.Fatalf("failed to parse template %s: %v", name, err)
		}
		templates[name] = t
	}

	mailer := &mail.Mailer{
		Host:    cfg.SMTPHost,
		Port:    cfg.SMTPPort,
		User:    cfg.SMTPUser,
		Pass:    cfg.SMTPPass,
		From:    cfg.SMTPFrom,
		BaseURL: cfg.BaseURL,
	}

	submitHandler := &handler.SubmitHandler{
		Store:    s,
		Notifier: mailer,
		BaseURL:  cfg.BaseURL,
	}

	limiter := ratelimit.NewLimiter(cfg.RateBurst, cfg.RatePerMinute, time.Now)
	limiter.StartCleanup(10*time.Minute, 30*time.Minute)

	loginGuard := ratelimit.NewLoginGuard(5, 15*time.Minute, time.Now)
	loginGuard.StartCleanup(30*time.Minute, 30*time.Minute)

	authHandler := &handler.AuthHandler{
		Store:      s,
		SecretKey:  cfg.SecretKey,
		BaseURL:    cfg.BaseURL,
		LoginGuard: loginGuard,
		Templates:  templates,
	}

	adminHandler := &handler.AdminHandler{
		Store:     s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		Templates: templates,
	}

	usersHandler := &handler.UsersHandler{
		Store:     s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		Templates: templates,
	}

	backupHandler := &handler.BackupHandler{
		Store:     s,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		DBPath:    cfg.DBPath,
		Templates: templates,
	}

	r := newRouter()
	r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", submitHandler.Handle)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/forms", http.StatusFound)
	})
	r.Get("/admin/login", authHandler.LoginPage)
	r.Post("/admin/login", authHandler.LoginSubmit)
	r.Get("/success", adminHandler.Success)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s))
		r.Post("/admin/logout", authHandler.Logout)
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/forms", http.StatusFound)
		})
		r.Get("/admin/forms", adminHandler.Dashboard)
		r.Get("/admin/forms/new", adminHandler.NewFormPage)
		r.Post("/admin/forms/new", adminHandler.CreateForm)
		r.Get("/admin/forms/{id}/edit", adminHandler.EditFormPage)
		r.Post("/admin/forms/{id}/edit", adminHandler.EditForm)
		r.Post("/admin/forms/{id}/delete", adminHandler.DeleteForm)
		r.Get("/admin/forms/{id}", adminHandler.FormDetail)
		r.Post("/admin/forms/{id}/read-all", adminHandler.MarkAllRead)
		r.Get("/admin/forms/{id}/export", adminHandler.ExportCSV)
		r.Get("/admin/forms/{formID}/submissions/{subID}", adminHandler.SubmissionDetail)
		r.Post("/admin/submissions/{id}/read", adminHandler.MarkRead)
		r.Post("/admin/submissions/{id}/delete", adminHandler.DeleteSubmission)
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
