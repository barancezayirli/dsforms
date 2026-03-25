package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/config"
	"github.com/youruser/dsforms/internal/handler"
	"github.com/youruser/dsforms/internal/mail"
	"github.com/youruser/dsforms/internal/ratelimit"
	"github.com/youruser/dsforms/internal/store"
)

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

	r := newRouter()
	r.With(rateLimitMiddleware(limiter)).Post("/f/{formID}", submitHandler.Handle)

	log.Printf("starting server on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
