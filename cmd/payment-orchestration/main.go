package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/api"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/config"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/fraud"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/logging"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/mpi"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store/postgres"
)

func main() {
	log.Fatal(run())
}

func run() error {
	cfg := config.FromEnv()
	srv := newService(cfg)
	mux := api.NewMux(srv)
	handler := srv.RequestLogMiddleware(mux)
	addr := ":" + cfg.Port
	return http.ListenAndServe(addr, handler)
}

func newService(cfg config.Config) *api.Service {
	st := newStore()
	logger := logging.NewDefault()
	devMode := config.DevMode()
	if devMode {
		logger.Warn("DEV_MODE=1: stub clients in use — NOT FOR PRODUCTION", nil)
	}
	sink := newAuditSink(devMode)
	if devMode {
		dummy := rail.NewDummy()
		registry := rail.NewRegistry(dummy)
		svc := api.NewService(st, registry, sink, cfg.WebhookSecret("card"))
		svc.ApplyConfig(cfg)
		svc.MPI = mpi.NewDummy()
		svc.Fraud = fraud.NewDummy()
		svc.Logger = logger
		return svc
	}
	_ = config.MustEnvOrFatal("RAIL_CONNECTORS_URL", "RAIL_CONNECTORS_URL required in production mode; real rail client not yet implemented — set DEV_MODE=1 for local dev")
	_ = config.MustEnvOrFatal("MPI_URL", "MPI_URL required in production mode; real MPI client not yet implemented — set DEV_MODE=1 for local dev")
	fraudURL := config.MustEnv("FRAUD_DETECTION_URL")
	svc := api.NewService(st, rail.NewRegistry(rail.NewDummy()), sink, cfg.WebhookSecret("card"))
	svc.ApplyConfig(cfg)
	svc.MPI = mpi.NewDummy()
	if fraudURL != "" {
		svc.Fraud = fraud.NewHTTP(fraudURL)
	}
	svc.Logger = logger
	return svc
}

func newAuditSink(devMode bool) audit.Sink {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		if devMode {
			log.Printf("warn: KAFKA_BROKERS unset and DEV_MODE=1; audit events recorded in-memory only")
			return audit.NewRecorder()
		}
		log.Fatalf("KAFKA_BROKERS unset and DEV_MODE not set; cannot start audit producer")
	}
	sink, err := audit.NewKafkaSink(splitCSV(brokers))
	if err != nil {
		if devMode {
			log.Printf("warn: audit kafka init failed (DEV_MODE): %v; falling back to recorder", err)
			return audit.NewRecorder()
		}
		log.Fatalf("audit kafka init: %v", err)
	}
	return sink
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newStore() store.Store {
	dsn := os.Getenv("DB_URL")
	if dsn != "" {
		db, err := postgres.Open(context.Background(), dsn)
		if err != nil {
			log.Fatalf("postgres: open: %v", err)
		}
		return db
	}
	if config.DevMode() {
		log.Printf("WARNING: DEV_MODE=1 with no DB_URL — using in-memory store; all state is lost on restart")
		return store.New()
	}
	config.MustEnvOrFatal("DB_URL", "DB_URL required in production mode — set DEV_MODE=1 to allow in-memory store for development")
	return store.New()
}