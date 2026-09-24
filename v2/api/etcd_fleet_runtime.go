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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/startup"
	"norn/v2/api/store"
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
	client, err := clientv3.New(clientv3.Config{Endpoints: backend.EtcdEndpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("etcd client: %w", err)
	}
	defer client.Close()
	if err := checkEtcdSourceHealth(context.Background(), client, backend.EtcdPrefix); err != nil {
		return fmt.Errorf("etcd availability: %w", err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, backend.EtcdPrefix, cfg.ControlAuthority, signer)
	if err != nil {
		return err
	}
	identities := etcdstore.NewAuthStore(client, backend.EtcdPrefix)

	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer)
	router.Get("/api/health", sourceValidationHealthHandler(client, backend.EtcdPrefix))
	router.Get("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, map[string]string{"version": Version})
	})
	router.Get("/api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeEtcdSourceJSON(w, http.StatusOK, map[string]interface{}{
			"protocolVersion": 1, "serverVersion": Version, "backend": "etcd", "mode": "normal-fleet",
			"features":    []string{"etcd-normal-router-v1", "managed-token-revocation", "signed-operation-acceptance", "fleet-inventory", "durable-fleet-capacity-plans"},
			"endpoints":   map[string]string{"fleetNodePools": "/api/v1/fleet/node-pools", "fleetPlans": "/api/v1/fleet/plans", "fleetPlan": "/api/v1/fleet/node-pools/{pool}/plan", "operation": "/api/v1/operations/{id}"},
			"unsupported": []string{"app-mutations", "fleet-runner-attempts", "fleet-github-bridge", "operation-cancellation"},
		})
	})
	// Fleet runners carry fleet:operate for their narrowly bound attempt
	// endpoints.  This normal router does not expose those endpoints, so that
	// scope must never become a general inventory or receipt-read capability.
	read := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)
	plan := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)
	router.With(read).Get("/api/v1/fleet/node-pools", etcdFleetInventory(cfg))
	router.With(read).Get("/api/v1/fleet/plans", etcdFleetPlans(operations))
	router.With(plan).Post("/api/v1/fleet/node-pools/{pool}/plan", etcdFleetPlan(cfg, operations))
	router.With(read).Get("/api/v1/operations/{id}", etcdFleetOperation(operations))
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			handler.WriteControlProblem(w, r, http.StatusNotImplemented, "backend_route_unsupported", "route is not implemented by the etcd normal Fleet capability set")
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Addr: cfg.BindAddr + ":" + cfg.Port, Handler: router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("norn %s etcd Fleet runtime listening on %s", Version, srv.Addr)
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			errCh <- e
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case e := <-errCh:
		return e
	case <-quit:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
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
func etcdFleetOperation(operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, err := operations.GetOperation(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
			return
		}
		op.AttachReceipt()
		writeEtcdSourceJSON(w, http.StatusOK, op)
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
