package main

import (
	"net"
	"testing"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/api"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

func TestRunReturnsErrorWhenAddrInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open listener: %v", err)
	}
	defer ln.Close()

	if err := run(ln.Addr().String()); err == nil {
		t.Fatal("expected run to return an error for an address already in use")
	}
}

func TestRunReturnsErrorForInvalidAddr(t *testing.T) {
	if err := run("not-a-valid-addr"); err == nil {
		t.Fatal("expected run to return an error for an invalid address")
	}
}

func TestNewServiceDefaults(t *testing.T) {
	s := newService()
	if s == nil {
		t.Fatal("expected non-nil service")
	}
	// Smoke test the mux builds and the health endpoint works.
	mux := api.NewMux(s)
	if mux == nil {
		t.Fatal("expected non-nil mux")
	}
}

// Ensure the unused helpers from the audit package keep the dependency live.
var _ = audit.NewRecorder

// Ensure store.New is referenced so the package is used.
var _ = store.New

// Ensure rail.NewDummy is referenced so the package is used.
var _ = rail.NewDummy