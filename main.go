package main

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/youruser/dsforms/internal/config"
)

func newRouter() *chi.Mux {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Printf("healthz write error: %v", err)
		}
	})

	return r
}

func main() {
	cfg := config.Load()

	r := newRouter()

	log.Printf("starting server on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
