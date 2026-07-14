package mtls

import (
	"net/http"
	"testing"
)

func TestConfigEnabled(t *testing.T) {
	c := Config{}
	if c.Enabled() {
		t.Fatal("empty config should not be enabled")
	}
	c.CertFile = "cert.pem"
	c.KeyFile = "key.pem"
	if !c.Enabled() {
		t.Fatal("cert+key should enable")
	}
}

func TestHTTPClientDisabled(t *testing.T) {
	c := Config{}
	client, err := c.HTTPClient()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if client.Timeout == 0 {
		t.Fatal("expected default timeout")
	}
	if _, ok := client.Transport.(*http.Transport); ok {
		t.Fatal("disabled config should not set custom transport")
	}
}

func TestHTTPClientInvalidMaterial(t *testing.T) {
	c := Config{CertFile: "/nonexistent/cert.pem", KeyFile: "/nonexistent/key.pem"}
	if _, err := c.HTTPClient(); err == nil {
		t.Fatal("expected error for missing cert material")
	}
}