//go:build fleetlocal

package main

// Harness for v2/scripts/test-fleet-executor-local (Q1). It is compiled only
// with -tags fleetlocal and runs only when Q1_AUTHORITY=1.
//
// It starts the real PG authority-only server (serveFleetAuthorityOnly: the
// same router, reconciler, converge watcher and executor lane as production)
// from a test binary because the production entrypoint refuses a local
// topology: validateFleetAuthorityOnly pins NORN_FLEET_GITHUB_API_BASE_URL to
// https://api.github.com and requires a PostgreSQL DSN with sslmode=verify-full.
// The harness logs what the production validation says about this
// configuration and then continues past it. It also redirects the name
// api.github.com (which the authority-only handler pins, in production mode, as
// the only GitHub API host) to the harness's fake GitHub and trusts the
// harness CA for it, by patching http.DefaultTransport in this test process.
// Nothing else is bypassed.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"net/http"
	"os"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

func TestFleetExecutorLocalAuthority(t *testing.T) {
	if os.Getenv("Q1_AUTHORITY") != "1" {
		t.Skip("Q1_AUTHORITY is not set")
	}
	startupCfg, err := startup.Parse(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	backendCfg, err := startup.ParseControlBackend(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if addr := os.Getenv("Q1_GITHUB_API_ADDR"); addr != "" {
		pem, err := os.ReadFile(os.Getenv("Q1_CA_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			t.Fatal("Q1_CA_FILE holds no certificate")
		}
		transport := http.DefaultTransport.(*http.Transport)
		dialer := &net.Dialer{}
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{RootCAs: pool}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "api.github.com:443" {
				address = addr
			}
			return dialer.DialContext(ctx, network, address)
		}
	}
	cfg := config.Load()
	if err := validateControlSecurityForBackend(cfg, backendCfg); err != nil {
		log.Printf("Q1 HARNESS NOTE: production validation refuses this local topology (continuing past it): %v", err)
	} else {
		log.Printf("Q1 HARNESS NOTE: production validation accepted this configuration")
	}
	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrator, err := store.NewControlSchemaMigrator(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startup.ApplySchemaMode(context.Background(), migrator, startupCfg); err != nil {
		t.Fatal(err)
	}
	serveFleetAuthorityOnly(cfg, db)
}
