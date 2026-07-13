package main

import (
	"log"
	"net/http"
	"os"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/api"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

func main() {
	log.Fatal(run(":8080"))
}

// run starts the HTTP server on addr and blocks until it exits.
func run(addr string) error {
	srv := newService()
	mux := api.NewMux(srv)
	return http.ListenAndServe(addr, mux)
}

// newService wires the service dependencies from environment configuration.
func newService() *api.Service {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	_ = port

	webhookKey := os.Getenv("WEBHOOK_SECRET")
	if webhookKey == "" {
		webhookKey = "dev-secret"
	}

	st := store.New()
	dummy := rail.NewDummy()
	registry := rail.NewRegistry(dummy)
	recorder := audit.NewRecorder()
	return api.NewService(st, registry, recorder, webhookKey)
}