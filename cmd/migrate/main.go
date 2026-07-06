// Package main is the migrate CLI. It applies or rolls back embedded SQL
// migrations against the DB_URL database. Usage:
//
//	migrate up      # apply all pending migrations
//	migrate down    # roll back the latest applied migration
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		fmt.Fprintln(os.Stderr, "usage: migrate [up|down]")
		os.Exit(2)
	}
	direction := os.Args[1]

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := store.LoadConfig()
	if cfg.DBURL == "" {
		log.Fatal("DB_URL is required")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DBURL)
	if err != nil {
		log.Fatalf("parse DB_URL: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	applyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch direction {
	case "up":
		if err := store.Migrate(applyCtx, pool); err != nil {
			log.Fatalf("migrate up: %v", err)
		}
		log.Println("migrations applied")
	case "down":
		if err := store.MigrateDown(applyCtx, pool); err != nil {
			log.Fatalf("migrate down: %v", err)
		}
		log.Println("latest migration rolled back")
	}
}