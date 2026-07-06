// Package main is the payment-orchestration service entrypoint. It opens a
// pooled Postgres connection, runs embedded SQL migrations on startup, and
// serves the HTTP API.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := store.LoadConfig()

	var health *store.HealthChecker
	if cfg.DBURL != "" {
		pool, err := store.Open(ctx, cfg)
		if err != nil {
			log.Fatalf("open store: %v", err)
		}
		defer pool.Close()
		health = store.NewHealthChecker(pool)
	} else {
		log.Println("DB_URL not set; starting without a database (migrations skipped)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz(health))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("payment-orchestration listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func healthz(h *store.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := h.Check(ctx); err != nil {
				http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}