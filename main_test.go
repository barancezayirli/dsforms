package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/ratelimit"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	r := newRouter()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", w.Body.String(), "ok")
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	r := newRouter()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.header, func(t *testing.T) {
			t.Parallel()
			got := w.Header().Get(tt.header)
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestMaxBytesReader(t *testing.T) {
	t.Parallel()
	r := newRouter()
	// Add a test route that reads the body
	r.Post("/test-body", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	bigBody := make([]byte, 65*1024)
	req := httptest.NewRequest("POST", "/test-body", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := ratelimit.NewLimiter(2, 6, func() time.Time { return now })

	r := chi.NewRouter()
	r.With(rateLimitMiddleware(l)).Post("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First 2 requests succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

func TestNotFoundHandler(t *testing.T) {
	t.Parallel()
	r := newRouter()
	req := httptest.NewRequest("GET", "/nonexistent-route", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Page not found") {
		t.Error("404 page content not rendered")
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()
	r := newRouter()
	r.Get("/panic-test", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	req := httptest.NewRequest("GET", "/panic-test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestRateLimitMiddlewareJSON(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := ratelimit.NewLimiter(1, 6, func() time.Time { return now })

	r := chi.NewRouter()
	r.With(rateLimitMiddleware(l)).Post("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Exhaust
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Rate limited with JSON Accept
	req2 := httptest.NewRequest("POST", "/test", nil)
	req2.Header.Set("Accept", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w2.Code)
	}
	var resp map[string]string
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["error"] != "too many requests" {
		t.Errorf("error = %q, want 'too many requests'", resp["error"])
	}
}
