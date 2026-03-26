package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/auth"
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

func main() {
	cfg := config.Load()

	s, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer s.Close()

	// Parse base template once, then clone it for each page that extends it.
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}
	baseTmpl, err := template.New("base").Funcs(funcMap).ParseFS(templateFS, "templates/base.html")
	if err != nil {
		log.Fatalf("failed to parse base template: %v", err)
	}

	templates := make(map[string]*template.Template)
	for _, name := range []string{"dashboard.html", "form_new.html", "form_edit.html", "form_detail.html", "submission_detail.html"} {
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

	r := newRouter()
	r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", submitHandler.Handle)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/forms", http.StatusFound)
	})
	r.Get("/admin/login", authHandler.LoginPage)
	r.Post("/admin/login", authHandler.LoginSubmit)
	r.Get("/success", adminHandler.Success)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(s, cfg.SecretKey))
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
	})

	log.Printf("starting server on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
