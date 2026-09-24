package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/startup"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

type etcdHealthClient interface {
	Get(context.Context, string, ...clientv3.OpOption) (*clientv3.GetResponse, error)
}

// runEtcdSourceValidation starts the only PG-free API surface currently
// supported by norn-api. It owns no recovery, effects, or general handler
// aggregates: it accepts and executes source-only preflights on etcd.
func runEtcdSourceValidation(cfg *config.Config, backend startup.ControlBackendConfig) error {
	if cfg == nil || !backend.SourceValidation || backend.Backend != startup.BackendEtcd {
		return fmt.Errorf("etcd source validation mode is not configured")
	}
	if !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 {
		return fmt.Errorf("etcd source validation requires NORN_REQUIRE_EXPLICIT_AUTH=true and a 32-byte NORN_API_TOKEN")
	}
	if len(cfg.AuditSigningKey) < 32 {
		return fmt.Errorf("etcd source validation requires a 32-byte NORN_AUDIT_SIGNING_KEY")
	}
	if strings.TrimSpace(cfg.ControlAuthority) == "" {
		return fmt.Errorf("etcd source validation requires NORN_CONTROL_AUTHORITY")
	}
	signer, err := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey, cfg.AuditPreviousSigningKeys...)
	if err != nil {
		return fmt.Errorf("acceptance signer: %w", err)
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: backend.EtcdEndpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("etcd client: %w", err)
	}
	defer client.Close()
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = client.Get(probeCtx, backend.EtcdPrefix, clientv3.WithLimit(1))
	cancelProbe()
	if err != nil {
		return fmt.Errorf("etcd availability: %w", err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, backend.EtcdPrefix, cfg.ControlAuthority, signer)
	if err != nil {
		return err
	}
	identities := etcdstore.NewAuthStore(client, backend.EtcdPrefix)
	pipe := &pipeline.Pipeline{
		OperationStore: operations, CheckpointStore: operations,
		AppsDir: cfg.AppsDir, NetworkMode: cfg.NetworkMode, StrictSecrets: cfg.StrictSecrets,
	}

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	go worker.NewOperationWorkerForKinds(operations, pipe, []string{"app.preflight"}).Run(workerCtx)

	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)
	router.Get("/api/health", sourceValidationHealthHandler(client, backend.EtcdPrefix))
	router.Get("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, map[string]string{"version": Version})
	})
	router.Get("/api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, map[string]interface{}{
			"protocolVersion": 1, "serverVersion": Version,
			"backend": "etcd", "mode": "source-validation",
			"features":  []string{"etcd-source-validation-v1", "signed-operation-acceptance", "managed-token-revocation", "source-checkpoints"},
			"endpoints": map[string]string{"sourcePreflight": "/api/v1/source-validation/apps/{id}/preflights"},
		})
	})
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)).Post("/api/v1/source-validation/apps/{id}/preflights", sourceValidationPreflight(pipe, operations, cfg))
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead, handler.ScopeAPIWrite)).Get("/api/v1/source-validation/operations/{id}", sourceValidationOperation(operations))
	// Every unlisted API route fails before a handler can reach a legacy store
	// or initiate an external effect.
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "source_validation_route_unsupported", "route is unavailable in etcd source-validation mode")
			return
		}
		http.NotFound(w, r)
	})

	srv := &http.Server{Addr: cfg.BindAddr + ":" + cfg.Port, Handler: router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("norn %s etcd source-validation listening on %s", Version, srv.Addr)
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

func sourceValidationHealthHandler(client etcdHealthClient, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := checkEtcdSourceHealth(r.Context(), client, prefix); err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "source_validation_unavailable", "etcd is unavailable")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, map[string]string{"status": "ok", "backend": "etcd", "mode": "source-validation"})
	}
}

// checkEtcdSourceHealth performs a bounded linearizable read in the selected
// control prefix. A member's Status response proves reachability only; this
// read requires the quorum-backed KV path that accepts source-validation work.
func checkEtcdSourceHealth(parent context.Context, client etcdHealthClient, prefix string) error {
	if client == nil || strings.TrimSpace(prefix) == "" {
		return fmt.Errorf("etcd source-validation health is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	_, err := client.Get(ctx, prefix, clientv3.WithLimit(1))
	return err
}

func etcdManagedTokenAuth(cfg *config.Config, identities store.IdentityStore, requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			principal, ok := handler.VerifyAccessTokenWithIdentityStore(cfg.APIToken, value, cfg.LegacyTokenSigningUntil, identities)
			if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken {
				handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a managed etcd access token is required")
				return
			}
			allowed := false
			for _, scope := range requiredScopes {
				allowed = allowed || principal.Allows(scope)
			}
			if !allowed {
				handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "token lacks required source-validation scope")
				return
			}
			next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
		})
	}
}

func sourceValidationPreflight(pipe *pipeline.Pipeline, operations store.OperationStore, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := chi.URLParam(r, "id")
		if app == "" {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_id", "app ID is required")
			return
		}
		var request struct {
			Ref string `json:"ref"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil && err != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); err != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
			return
		}
		if request.Ref == "" {
			request.Ref = "HEAD"
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken {
			handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a managed etcd access token is required")
			return
		}
		specs, err := model.DiscoverAllApps(cfg.AppsDir)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "app_discovery_failed", "source app discovery failed")
			return
		}
		var spec *model.InfraSpec
		for _, candidate := range specs {
			if candidate.App == app {
				spec = candidate
				break
			}
		}
		if spec == nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found")
			return
		}
		authority, err := operations.Authority(r.Context())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "authority_unavailable", "control authority is unavailable")
			return
		}
		accepted, err := pipe.Preflight(r.Context(), spec, request.Ref, pipeline.EnqueueRequest{
			Authority: authority, Key: key,
			Actor:     store.OperationActor{Issuer: "norn://managed-token", Subject: principal.TokenID},
			Audit:     store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-source-validation", Scopes: principal.Scopes},
			Semantics: map[string]interface{}{"mode": "source-validation"},
		})
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "operation_acceptance_failed", "source-validation operation was not accepted")
			return
		}
		status := http.StatusAccepted
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, map[string]interface{}{"operationId": accepted.Operation.ID, "status": accepted.Operation.Status, "replayed": accepted.Replayed, "signature": accepted.Intent.Signature})
	}
}

func sourceValidationOperation(operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operation, err := operations.GetOperation(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation was not found")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, operation)
	}
}

func writeEtcdSourceJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
