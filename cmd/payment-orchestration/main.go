package main

import (
	"log"
	"net/http"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/api"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/config"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/fraud"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/logging"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/mpi"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

func main() {
	log.Fatal(run())
}

// run builds the service from environment configuration and starts the HTTP
// server, blocking until it exits.
func run() error {
	cfg := config.FromEnv()
	srv := newService(cfg)
	mux := api.NewMux(srv)
	handler := srv.RequestLogMiddleware(mux)
	addr := ":" + cfg.Port
	return http.ListenAndServe(addr, handler)
}

// newService wires the service dependencies from environment configuration.
func newService(cfg config.Config) *api.Service {
	st := store.New()
	dummy := rail.NewDummy()
	registry := rail.NewRegistry(dummy)
	recorder := audit.NewRecorder()
	svc := api.NewService(st, registry, recorder, cfg.WebhookSecret("card"))
	svc.ApplyConfig(cfg)
	svc.MPI = mpi.NewDummy()
	svc.Fraud = fraud.NewDummy()
	svc.Logger = logging.NewDefault()
	return svc
}