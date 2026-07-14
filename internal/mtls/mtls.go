package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"time"
)

// Config holds the material for an outbound mTLS HTTP transport.
type Config struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// Enabled reports whether cert and key material is configured.
func (c Config) Enabled() bool {
	return c.CertFile != "" && c.KeyFile != ""
}

// HTTPClient returns an *http.Client configured for mutual TLS using the
// configured material. If no material is configured it returns a default
// client.
func (c Config) HTTPClient() (*http.Client, error) {
	if !c.Enabled() {
		return &http.Client{Timeout: 10 * time.Second}, nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if c.CAFile != "" {
		caPEM, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("mtls: failed to parse CA bundle")
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}, nil
}