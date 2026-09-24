package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/startup"
)

// runEtcdMigrateOnly preserves the shared startup contract. Etcd's v3 records
// have no schema migrator; this mode therefore proves a linearizable read and
// exits without serving or writing control records.
func runEtcdMigrateOnly(backend startup.ControlBackendConfig) error {
	client, err := newEtcdClient(backend)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := checkEtcdSourceHealth(context.Background(), client, backend.EtcdPrefix); err != nil {
		return fmt.Errorf("etcd schema check: %w", err)
	}
	log.Printf("norn %s etcd migrate-only schema check complete", Version)
	return nil
}

// runEtcdPassiveRuntime exposes the startup contract's loopback-only status
// surface. It does not construct auth stores, operation stores, or workers.
func runEtcdPassiveRuntime(cfg *config.Config, backend startup.ControlBackendConfig) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	client, err := newEtcdClient(backend)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := checkEtcdSourceHealth(context.Background(), client, backend.EtcdPrefix); err != nil {
		return fmt.Errorf("etcd schema check: %w", err)
	}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		writeEtcdSourceJSON(w, http.StatusOK, value)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"status": "passive", "backend": "etcd", "schemaMode": "check"})
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) { write(w, map[string]string{"version": Version}) })
	mux.HandleFunc("GET /api/schema", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"backend": "etcd", "startupMode": "passive", "schemaMode": "check", "migration": "not-applicable"})
	})
	srv := &http.Server{Addr: cfg.BindAddr + ":" + cfg.Port, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 16}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("norn %s etcd passive runtime listening on %s", Version, srv.Addr)
		if serveErr := srv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-quit:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
