package main

// The normal etcd router is deliberately narrow. Fleet capacity plans are
// signed intent receipts and do not contact a provider, so they are the first
// normal control aggregate that can run wholly on the v3 etcd store.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/startup"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

const (
	etcdCanaryWorkerEnv      = "NORN_ETCD_CANARY_WORKER"
	etcdCanaryHTTPPreviewEnv = "NORN_ETCD_CANARY_HTTP_PREVIEW"
)

func runEtcdFleetRuntime(cfg *config.Config, backend startup.ControlBackendConfig) error {
	if cfg == nil || backend.Backend != startup.BackendEtcd || backend.SourceValidation {
		return fmt.Errorf("normal etcd Fleet runtime is not configured")
	}
	if !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 || len(cfg.AuditSigningKey) < 32 || strings.TrimSpace(cfg.ControlAuthority) == "" {
		return fmt.Errorf("etcd Fleet runtime requires explicit auth, a 32-byte API token and audit key, and control authority")
	}
	signer, err := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey, cfg.AuditPreviousSigningKeys...)
	if err != nil {
		return fmt.Errorf("acceptance signer: %w", err)
	}
	client, err := newEtcdClient(backend)
	if err != nil {
		return fmt.Errorf("etcd client: %w", err)
	}
	defer client.Close()
	if err := checkEtcdSourceHealth(context.Background(), client, backend.EtcdPrefix); err != nil {
		return fmt.Errorf("etcd availability: %w", err)
	}
	if err := requireInitialEtcdBootstrap(context.Background(), client, backend.EtcdPrefix, cfg.APIToken); err != nil {
		return fmt.Errorf("etcd bootstrap: %w", err)
	}
	operations, err := etcdstore.NewV3OperationStoreWithPolicy(client, backend.EtcdPrefix, cfg.ControlAuthority, signer, store.AcceptancePolicy{ReplayTTL: cfg.OperationReplayTTL})
	if err != nil {
		return err
	}
	catalogWorker := worker.NewOperationWorkerForKinds(operations, &pipeline.EtcdCatalogExecutor{Catalog: operations}, []string{pipeline.CatalogActivationKind})
	if err := preflightConfiguredPrivateInvocationKeys(context.Background(), cfg, operations); err != nil {
		return fmt.Errorf("private invocation startup preflight: %w", err)
	}
	identities := etcdstore.NewAuthStore(client, backend.EtcdPrefix)
	githubConfig := githubapp.Config{AppID: cfg.FleetGitHubAppID, InstallationID: cfg.FleetGitHubInstallationID, PrivateKeyFile: cfg.FleetGitHubPrivateKeyFile, Repository: cfg.FleetGitHubRepository, Environment: cfg.FleetGitHubEnvironment, PilotRunID: cfg.FleetGitHubPilotRunID, DefaultBranch: cfg.FleetGitHubDefaultBranch, ConfigPath: cfg.FleetGitHubConfigPath, PlanWorkflow: cfg.FleetGitHubPlanWorkflow, ApplyWorkflow: cfg.FleetGitHubApplyWorkflow, RecoverWorkflow: cfg.FleetGitHubRecoverWorkflow, APIBaseURL: cfg.FleetGitHubAPIBaseURL, Production: cfg.Production()}
	var fleetGitHub *githubapp.Client
	if githubapp.Configured(githubConfig) {
		fleetGitHub, err = githubapp.New(githubConfig, nil)
		if err != nil {
			return fmt.Errorf("configure Fleet GitHub App: %w", err)
		}
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	var workerWG sync.WaitGroup
	var deploy *etcdFleetDeployRuntime
	defer func() {
		stopWorker()
		workerWG.Wait()
		if deploy != nil {
			_ = deploy.secrets.Close()
		}
	}()
	workerEnabled, canaryHTTPEnabled, err := etcdCanaryPreviewFlags(os.Getenv)
	if err != nil {
		return err
	}
	var canary *etcdCanaryRuntime
	if workerEnabled {
		canary, err = newEtcdCanaryRuntime(cfg, operations)
		if err != nil {
			return fmt.Errorf("configure etcd canary worker: %w", err)
		}
	}
	deployConfig := strings.TrimSpace(os.Getenv(etcdFleetDeployWorkerConfigEnv))
	if deployConfig != "" {
		deploy, err = newEtcdFleetDeployRuntime(workerCtx, cfg, operations, deployConfig)
		if err != nil {
			return fmt.Errorf("configure etcd Fleet deploy worker: %w", err)
		}
	}
	releaseHTTPEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv(etcdFleetReleaseHTTPEnv)), "true")
	var releaseVerifier *pipeline.Pipeline
	if releaseHTTPEnabled {
		if deployConfig == "" {
			return fmt.Errorf("%s requires %s", etcdFleetReleaseHTTPEnv, etcdFleetDeployWorkerConfigEnv)
		}
		releaseVerifier, err = newEtcdFleetReleaseVerifier(cfg)
		if err != nil {
			return fmt.Errorf("configure etcd Fleet release admission: %w", err)
		}
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)
	var reconciler *controller.Reconciler
	if fleetReconcilerEnabled(os.Getenv) {
		reconciler = newFleetReconciler(operations, func(err error) bool { return errors.Is(err, etcdstore.ErrFleetResourceNotFound) })
		router.Use(fleetReconcilerNotify(reconciler))
	}
	router.Get("/api/health", sourceValidationHealthHandler(client, backend.EtcdPrefix))
	router.Get("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, map[string]string{"version": Version})
	})
	router.Get("/api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, etcdFleetCapabilities(cfg, canaryHTTPEnabled, fleetGitHub != nil, releaseHTTPEnabled))
	})
	// Fleet runners carry fleet:operate only for their own attempts and
	// reconciliation checkpoints. Inventory and general operation reads still
	// require api:read.
	read := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)
	plan := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)
	catalogOperate := etcdManagedTokenAuth(cfg, identities, handler.ScopePlatformOperate)
	router.With(read).Get("/api/v1/fleet/node-pools", etcdFleetInventory(cfg))
	router.With(read).Get("/api/v1/apps/{id}/fleet-target", etcdFleetAppTargetRead(cfg, operations))
	if releaseHTTPEnabled {
		releaseStage := etcdManagedTokenAuth(cfg, identities, handler.ScopeReleaseStage)
		router.With(releaseStage).Post("/api/v1/apps/{id}/releases/deployments", etcdFleetReleaseDeployment(cfg, operations, releaseVerifier))
	}
	router.With(catalogOperate).Put("/api/v1/apps/{id}/fleet-target", etcdFleetAppTargetConfigure(cfg, operations))
	router.With(read).Get("/api/v1/database/catalog", etcdFleetDatabaseCatalog(operations))
	router.With(catalogOperate).Post("/api/v1/database/catalog/activations", etcdFleetCatalogActivation(operations))
	router.With(read).Get("/api/v1/fleet/plans", etcdFleetPlans(operations))
	router.With(plan).Post("/api/v1/fleet/node-pools/{pool}/plan", etcdFleetPlan(cfg, operations))
	if canaryHTTPEnabled {
		router.With(plan).Post("/api/v1/apps/{id}/promote", etcdCanaryPromote(cfg, operations, identities, canary))
	}
	operationRead := read
	if releaseHTTPEnabled {
		operationRead = etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead, handler.ScopeReleaseStage)
	}
	registerEtcdFleetTargetRoutes(router, cfg, identities, operations, fleetGitHub)
	var liveness handler.FleetControllerLiveness
	if reconciler != nil {
		liveness = reconciler
	}
	registerEtcdFleetResourceRoutes(router, cfg, identities, operations, fleetGitHub, liveness)
	router.With(operationRead).Get("/api/v1/operations/{id}", etcdFleetOperation(operations, canaryHTTPEnabled, cfgIfFleetRelease(releaseHTTPEnabled, cfg)))
	if fleetGitHub != nil {
		fleetRunner := handler.NewEtcdFleetRunnerHandler(cfg, operations, fleetGitHub)
		runnerAuth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetOperate)
		router.With(plan).Post("/api/v1/fleet/plans/{planID}/github/dispatch", etcdFleetGitHubDispatch(cfg, operations, fleetGitHub))
		router.With(plan).Post("/api/v1/fleet/plans/{planID}/github/pull-request", etcdFleetGitHubPullRequest(cfg, operations, fleetGitHub))
		router.With(runnerAuth).Get("/api/v1/fleet/plans/{planID}/attempts", fleetRunner.List)
		router.With(runnerAuth).Post("/api/v1/fleet/plans/{planID}/attempts", fleetRunner.Create)
		router.With(runnerAuth).Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}", fleetRunner.Get)
		router.With(runnerAuth).Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", fleetRunner.Heartbeat)
		router.With(runnerAuth).Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance", fleetRunner.Advance)
		router.With(runnerAuth).Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel", fleetRunner.Cancel)
		router.With(runnerAuth).Get("/api/v1/fleet/plans/{planID}/reconciliations", fleetRunner.ListReconciliations)
		router.With(runnerAuth).Post("/api/v1/fleet/plans/{planID}/reconciliations", fleetRunner.Reconcile)
	}
	// A credential may retire itself regardless of its application scopes.
	managed := etcdManagedTokenAuth(cfg, identities)
	// GitHub's short-lived assertion is the credential for this route; a Norn
	// bearer token does not exist until the durable exchange succeeds.
	router.Post("/api/v1/auth/github-actions/exchange", func(w http.ResponseWriter, r *http.Request) {
		handler.ExchangeFleetGitHubActionsOIDC(cfg, identities, w, r)
	})
	router.With(managed).Post("/api/v1/auth/rotate", func(w http.ResponseWriter, r *http.Request) {
		handler.RotateManagedToken(cfg, identities, nil, w, r)
	})
	router.With(managed).Post("/api/v1/auth/revoke", func(w http.ResponseWriter, r *http.Request) {
		handler.RevokeManagedToken(identities, nil, w, r)
	})
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			handler.WriteControlProblem(w, r, http.StatusNotImplemented, "backend_route_unsupported", "route is not implemented by the etcd normal Fleet capability set")
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Addr: cfg.BindAddr + ":" + cfg.Port, Handler: router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("etcd Fleet runtime listen: %w", err)
	}
	defer listener.Close()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	// Complete selected runtime and listener preflights before any worker can
	// claim queued work. Invalid release policy or a failed bind must not leave
	// a short window for a deployment effect.
	runWorker := func(value *worker.OperationWorker) {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			value.Run(workerCtx)
		}()
	}
	runWorker(catalogWorker)
	if canary != nil {
		runWorker(canary.worker)
		log.Printf("etcd canary operation worker enabled; HTTP preview=%t", canaryHTTPEnabled)
	}
	if deploy != nil {
		runWorker(deploy.worker)
		log.Print("etcd Fleet app.deploy worker enabled for signed claimed operations")
	}
	if reconciler != nil {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			reconciler.Run(workerCtx)
		}()
		log.Print("etcd Fleet resource reconciler enabled (observe-only; writes Fleet status only)")
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("norn %s etcd Fleet runtime listening on %s", Version, srv.Addr)
		if e := srv.Serve(listener); e != nil && e != http.ErrServerClosed {
			errCh <- e
		}
	}()
	select {
	case e := <-errCh:
		return e
	case <-quit:
		stopWorker()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

func etcdCanaryPreviewFlags(getenv func(string) string) (workerEnabled, httpEnabled bool, err error) {
	workerEnabled = strings.EqualFold(strings.TrimSpace(getenv(etcdCanaryWorkerEnv)), "true")
	httpEnabled = strings.EqualFold(strings.TrimSpace(getenv(etcdCanaryHTTPPreviewEnv)), "true")
	if httpEnabled && !workerEnabled {
		return false, false, fmt.Errorf("%s requires %s=true", etcdCanaryHTTPPreviewEnv, etcdCanaryWorkerEnv)
	}
	return workerEnabled, httpEnabled, nil
}

func etcdFleetCapabilities(cfg *config.Config, canaryHTTPEnabled bool, githubEnabled ...bool) map[string]interface{} {
	features := []string{"etcd-normal-router-v1", "fleet-authority-only-v1", "fleet-v1", "managed-token-revocation", "managed-token-lifecycle", "fleet-github-oidc-exchange", "signed-operation-acceptance", "fleet-inventory", "fleet-app-target-configuration", "durable-fleet-capacity-plans", "database-catalog-inspection", "postgresql-catalog-activation", "fleet-target-fence-v1", "fleet-resource-v1"}
	endpoints := map[string]string{"fleetNodePools": "/api/v1/fleet/node-pools", "fleetAppTarget": "/api/v1/apps/{id}/fleet-target", "fleetPlans": "/api/v1/fleet/plans", "fleetPlan": "/api/v1/fleet/node-pools/{pool}/plan", "operation": "/api/v1/operations/{id}", "databaseCatalog": "/api/v1/database/catalog", "databaseCatalogActivations": "/api/v1/database/catalog/activations", "tokenRotate": "/api/v1/auth/rotate", "tokenRevoke": "/api/v1/auth/revoke", "fleetOIDCExchange": "/api/v1/auth/github-actions/exchange", "fleetTargets": "/api/v1/fleet/targets", "fleetResources": "/api/v1/fleet/resources"}
	unsupported := []string{"app-mutations", "fleet-runner-attempts", "fleet-github-bridge", "operation-cancellation"}
	if len(githubEnabled) > 0 && githubEnabled[0] {
		features = append(features, "fleet-github-pull-request", "fleet-github-protected-dispatch", "fleet-runner-attempts-v1", "fleet-reconciliation-v1")
		endpoints["fleetGitHubPullRequest"] = "/api/v1/fleet/plans/{planID}/github/pull-request"
		endpoints["fleetGitHubDispatch"] = "/api/v1/fleet/plans/{planID}/github/dispatch"
		endpoints["fleetRunnerAttempts"] = "/api/v1/fleet/plans/{planID}/attempts"
		endpoints["fleetReconciliations"] = "/api/v1/fleet/plans/{planID}/reconciliations"
		unsupported = []string{"app-mutations", "operation-cancellation"}
	}
	if canaryHTTPEnabled {
		features = append(features, "durable-canary-promotion-preview")
		endpoints["appCanaryPromote"] = "/api/v1/apps/{id}/promote"
		unsupported[0] = "other-app-mutations"
	}
	if len(githubEnabled) > 1 && githubEnabled[1] {
		features = append(features, "staging-fleet-release-admission-v1")
		endpoints["appReleaseDeployment"] = "/api/v1/apps/{id}/releases/deployments"
		unsupported[0] = "other-app-mutations"
	}
	return map[string]interface{}{
		"protocolVersion": 1, "serverVersion": Version, "backend": "etcd", "mode": "normal-fleet",
		"environment": map[string]string{"id": cfg.EnvironmentID(), "profile": cfg.ProfileID()},
		// Native clients use this authority marker to avoid requesting app and
		// host inventories that the narrow Fleet router does not expose.
		"authority": "fleet-only",
		"features":  features, "endpoints": endpoints, "unsupported": unsupported,
		"auth": map[string]interface{}{
			"scopes":                handler.AccessTokenScopeNames(),
			"principal":             map[string]interface{}{"authenticated": false, "scopes": []string{}},
			"websocketBearerHeader": false, "websocketQueryToken": false,
		},
	}
}

type etcdCanaryRuntime struct {
	worker   *worker.OperationWorker
	pipeline *pipeline.Pipeline
	nomad    *nomad.Client
}

func newEtcdCanaryRuntime(cfg *config.Config, operations *etcdstore.V3OperationStore) (*etcdCanaryRuntime, error) {
	if cfg == nil || operations == nil || strings.TrimSpace(cfg.NomadAddr) == "" {
		return nil, fmt.Errorf("etcd canary worker requires configured Nomad and operation stores")
	}
	nomadClient, err := nomad.NewClient(cfg.NomadAddr)
	if err != nil {
		return nil, err
	}
	effectStore, err := etcdstore.NewV3CanaryEffectReservations(operations)
	if err != nil {
		return nil, err
	}
	effects, err := pipeline.NewNomadCanaryPromotionEffectsWithStore(effectStore, nomadClient)
	if err != nil {
		return nil, err
	}
	p := &pipeline.Pipeline{OperationStore: operations, Nomad: nomadClient, CanaryPromotionEffects: effects}
	return &etcdCanaryRuntime{worker: worker.NewOperationWorkerForKinds(operations, p, []string{"app.canary-promote"}), pipeline: p, nomad: nomadClient}, nil
}

func etcdFleetInventory(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inventory, err := loadEtcdFleetInventory(cfg)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_config_read_failed", "failed to read configured fleet document")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, inventory)
	}
}
func loadEtcdFleetInventory(cfg *config.Config) (*fleet.Inventory, error) {
	path := ""
	if cfg != nil {
		path = strings.TrimSpace(cfg.FleetConfig)
	}
	source := ""
	if path != "" {
		source = filepath.Base(filepath.Clean(path))
	}
	inventory := &fleet.Inventory{SchemaVersion: "norn.fleet-inventory/v1", Configured: path != "", Source: source, NodePools: map[string]fleet.NodePool{}}
	if path == "" {
		return inventory, nil
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, report := fleet.ParseAndValidate(document)
	inventory.Digest = fleet.Digest(document)
	inventory.Document = parsed
	inventory.Validation = report
	if parsed != nil {
		inventory.NodePools = parsed.NodePools
	}
	return inventory, nil
}
func etcdFleetPlans(operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ops, err := operations.ListOperationsByKind(r.Context(), "fleet.capacity-plan", 50)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_read_failed", "failed to read capacity plans")
			return
		}
		plans := make([]model.Operation, 0)
		for _, op := range ops {
			op.AttachReceipt()
			plans = append(plans, op)
		}
		writeEtcdSourceJSON(w, http.StatusOK, map[string]interface{}{"plans": plans, "count": len(plans)})
	}
}
func cfgIfFleetRelease(enabled bool, cfg *config.Config) *config.Config {
	if enabled {
		return cfg
	}
	return nil
}

func etcdFleetOperation(operations *etcdstore.V3OperationStore, allowCanary bool, releaseConfig ...*config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || (!principal.Allows(handler.ScopeAPIRead) && !principal.Allows(handler.ScopeReleaseStage)) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet capacity-plan reads require a managed api:read principal")
			return
		}
		op, err := operations.GetOperation(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
			return
		}
		allowed := principal.Allows(handler.ScopeAPIRead) && (op.Kind == "fleet.capacity-plan" || op.Kind == pipeline.CatalogActivationKind || (allowCanary && op.Kind == "app.canary-promote"))
		if !allowed && len(releaseConfig) > 0 && releaseConfig[0] != nil && op.Kind == "app.deploy" {
			allowed = principal.Allows(handler.ScopeAPIRead) || fleetReleaseOperationReadable(*releaseConfig[0], principal, *op)
		}
		if !allowed {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
			return
		}
		op.AttachReceipt()
		writeEtcdSourceJSON(w, http.StatusOK, op)
	}
}

func etcdFleetDatabaseCatalog(operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || !principal.Allows(handler.ScopeAPIRead) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "database catalog inspection requires a managed api:read principal")
			return
		}
		active, err := operations.ActiveDatabaseCatalog(r.Context())
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "database_catalog_not_active", "no database catalog revision is active")
			return
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "database_catalog_unreadable", "the active database catalog could not be verified")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, database.InspectCatalog(active.Revision, active.Digest, active.Catalog))
	}
}

func etcdFleetPlan(cfg *config.Config, operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pool := chi.URLParam(r, "pool")
		if !etcdFleetName(pool) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "node pool name is invalid")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.TokenID == "" {
			handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a managed etcd access token is required")
			return
		}
		if principal.CI != nil || !principal.Allows(handler.ScopeAPIWrite) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet capacity planning requires a non-CI api:write operator")
			return
		}
		var request fleet.PlanRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "invalid request body")
			return
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); err != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "request body must contain one JSON value")
			return
		}
		authority, err := operations.Authority(r.Context())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "authority_unavailable", "control authority is unavailable")
			return
		}
		identity := store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "norn://managed-token", Subject: principal.TokenID}, Kind: "fleet.capacity-plan", Resource: pool, Key: key}
		// Resolve before reading mutable Fleet configuration. A verified receipt
		// is immutable evidence for this identity, including when the document
		// that informed its original plan has changed or disappeared.
		if accepted, resolveErr := operations.ResolveIdentity(r.Context(), identity); resolveErr == nil {
			if !etcdFleetReplayRequestMatches(accepted, pool, request) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different operation request")
				return
			}
			accepted.Operation.AttachReceipt()
			w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
			writeEtcdSourceJSON(w, http.StatusOK, accepted.Operation)
			return
		} else if !errors.Is(resolveErr, store.ErrAcceptanceNotFound) {
			writeEtcdFleetAcceptanceError(w, r, resolveErr)
			return
		}
		inventory, err := loadEtcdFleetInventory(cfg)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_config_read_failed", "failed to read configured fleet document")
			return
		}
		if !inventory.Configured || inventory.Document == nil || inventory.Validation == nil || !inventory.Validation.Valid {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_not_configured", "a valid NORN_FLEET_CONFIG is required before planning")
			return
		}
		current, ok := inventory.NodePools[pool]
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "fleet_node_pool_not_found", "node pool not found")
			return
		}
		plan, err := buildEtcdCapacityPlan(inventory, pool, current, request, cfg.AuditSigningKey)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "fleet_plan_unsafe", err.Error())
			return
		}
		// A replay must reproduce the same signed plan payload before acceptance
		// compares its fingerprint. The durable request identity is the stable
		// source for this receipt ID; random IDs would turn an identical retry
		// into an idempotency conflict.
		plan.ID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(cfg.ControlAuthority+"\x00"+principal.TokenID+"\x00"+pool+"\x00"+key)).String()
		if err := refreshEtcdCapacityPlanSignature(plan, cfg.AuditSigningKey); err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_signing_failed", "failed to sign Fleet capacity plan")
			return
		}
		payloadBytes, _ := json.Marshal(plan)
		payload := map[string]interface{}{}
		_ = json.Unmarshal(payloadBytes, &payload)
		now := time.Now().UTC()
		finished := now
		op := model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: pool, Status: model.OperationSucceeded, Risk: "read-only infrastructure capacity plan; Git review and protected apply remain required", Source: "etcd-control-api", Message: "capacity plan recorded; no provider mutation performed", Payload: payload, Metadata: map[string]interface{}{"planId": plan.ID, "planDigest": plan.Digest, "signature": plan.Signature}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
		acceptance := store.OperationAcceptance{Identity: identity, Operation: op, Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-normal-fleet", Scopes: principal.Scopes}, Semantics: map[string]interface{}{"pool": pool, "request": request, "planDigest": plan.Digest}}
		acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "operation_acceptance_failed", "failed to fingerprint operation")
			return
		}
		accepted, err := operations.Accept(r.Context(), acceptance)
		if err != nil {
			writeEtcdFleetAcceptanceError(w, r, err)
			return
		}
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		status := http.StatusCreated
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, accepted.Operation)
	}
}

func etcdFleetReplayRequestMatches(accepted store.AcceptedOperation, pool string, request fleet.PlanRequest) bool {
	var receipt struct {
		Kind      string `json:"kind"`
		Resource  string `json:"resource"`
		Semantics struct {
			Pool    string            `json:"pool"`
			Request fleet.PlanRequest `json:"request"`
		} `json:"semantics"`
	}
	if err := json.Unmarshal(accepted.Intent.RequestCanonicalBytes, &receipt); err != nil {
		return false
	}
	return receipt.Kind == "fleet.capacity-plan" && receipt.Resource == pool && receipt.Semantics.Pool == pool && reflect.DeepEqual(receipt.Semantics.Request, request)
}
func etcdFleetName(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') || (i == 0 && r == '-') {
			return false
		}
	}
	return true
}

func writeEtcdFleetAcceptanceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrAcceptanceExpired) {
		handler.WriteControlProblem(w, r, http.StatusGone, "operation_replay_expired", "the accepted request's replay window expired; use a new Idempotency-Key after reviewing its operation receipt")
		return
	}
	if errors.Is(err, store.ErrAcceptanceConflict) {
		handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different operation request")
		return
	}
	if errors.Is(err, store.ErrAcceptanceIndeterminate) {
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_indeterminate", "operation acceptance is indeterminate; retry with the same Idempotency-Key")
		return
	}
	handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_failed", "failed to accept Fleet capacity plan")
}
func buildEtcdCapacityPlan(inventory *fleet.Inventory, pool string, current fleet.NodePool, request fleet.PlanRequest, key string) (*fleet.CapacityPlan, error) {
	proposed := current
	if request.Desired != nil {
		proposed.Desired = *request.Desired
	}
	if request.Size != "" {
		proposed.Size = strings.TrimSpace(request.Size)
		if proposed.Size == "" {
			return nil, fmt.Errorf("size must not be blank")
		}
	}
	strategy := strings.TrimSpace(request.Strategy)
	if strategy == "" {
		strategy = current.Replacement.Strategy
	}
	if strategy == "" {
		strategy = "blueGreen"
	}
	if strategy != "blueGreen" && strategy != "rolling" {
		return nil, fmt.Errorf("strategy must be blueGreen or rolling")
	}
	if proposed.Desired < current.Min || proposed.Desired > current.Max {
		return nil, fmt.Errorf("desired capacity must remain between min %d and max %d", current.Min, current.Max)
	}
	if proposed.Size != current.Size && strategy != "blueGreen" {
		return nil, fmt.Errorf("VM size changes require blueGreen replacement")
	}
	action := "reconcile"
	if proposed.Size != current.Size {
		action = "replace"
	} else if proposed.Desired != current.Desired {
		action = "scale"
	}
	findings := []fleet.Finding{}
	if action == "reconcile" {
		findings = append(findings, fleet.Finding{Severity: "info", Code: "fleet.plan.no-desired-change", Field: "proposed", Message: "desired state is unchanged; the runner should reconcile drift only"})
	}
	if proposed.Desired < current.Desired {
		findings = append(findings, fleet.Finding{Severity: "warning", Code: "fleet.plan.downsize.requires-live-evidence", Field: "proposed.desired", Message: "downsize approval requires live reservation, rollout, failure-headroom, pending-allocation, volume, and singleton checks", Remediation: "Attach Norn assurance evidence to the protected apply review."})
	}
	if action == "replace" {
		findings = append(findings, fleet.Finding{Severity: "info", Code: "fleet.plan.blue-green.required", Field: "strategy", Message: "new nodes must enroll and pass readiness before old nodes drain"})
	}
	plan := &fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: uuid.NewString(), Cluster: inventory.Document.Cluster.Name, Pool: pool, Current: current, Proposed: proposed, Action: action, Strategy: strategy, Reason: strings.TrimSpace(request.Reason), SourceDigest: inventory.Digest, WorkflowURL: inventory.Document.Metadata.WorkflowURL, Findings: findings}
	sort.SliceStable(plan.Findings, func(i, j int) bool { return plan.Findings[i].Code < plan.Findings[j].Code })
	canonical := *plan
	canonical.Findings = append([]fleet.Finding(nil), plan.Findings...)
	bytes, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bytes)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(plan.Digest))
	plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	return plan, nil
}

func refreshEtcdCapacityPlanSignature(plan *fleet.CapacityPlan, key string) error {
	canonical := *plan
	canonical.Digest, canonical.Signature = "", ""
	canonical.Findings = append([]fleet.Finding(nil), plan.Findings...)
	sort.SliceStable(canonical.Findings, func(i, j int) bool { return canonical.Findings[i].Code < canonical.Findings[j].Code })
	bytes, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bytes)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(plan.Digest))
	plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	return nil
}
