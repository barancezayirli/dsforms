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
		w.Write([]byte("ok"))
	})

	return r
}

func main() {
	cfg := config.Load()

	r := newRouter()

	log.Printf("starting server on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
