package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf(`expected status "ok", got %q`, body["status"])
	}
}

func TestNewMuxRouting(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{name: "healthz ok", method: http.MethodGet, path: "/healthz", wantCode: http.StatusOK},
		{name: "unknown path", method: http.MethodGet, path: "/nope", wantCode: http.StatusNotFound},
		{name: "root not registered", method: http.MethodGet, path: "/", wantCode: http.StatusNotFound},
	}

	mux := newMux()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("%s %s: expected %d, got %d", tt.method, tt.path, tt.wantCode, rec.Code)
			}
		})
	}
}

func TestRunReturnsErrorWhenAddrInUse(t *testing.T) {
	// Occupy a port so run fails fast instead of blocking.
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
