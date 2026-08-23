package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type documentValidationRequest struct {
	Document      string `json:"document"`
	FleetDocument string `json:"fleetDocument,omitempty"`
}

func (h *Handler) ValidateFleetDocument(w http.ResponseWriter, r *http.Request) {
	var request documentValidationRequest
	if err := decodeControlJSONLimit(w, r, &request, maxDocumentValidationJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_validation_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Document) == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "validation_document_required", "document is required")
		return
	}
	if len(request.Document) > maxValidationDocumentBytes {
		WriteControlProblem(w, r, http.StatusBadRequest, "validation_document_too_large", "document must not exceed 65,536 bytes")
		return
	}
	_, report := fleet.ParseAndValidate([]byte(request.Document))
	writeJSON(w, report)
}

func (h *Handler) ValidateInfraSpecDocument(w http.ResponseWriter, r *http.Request) {
	var request documentValidationRequest
	if err := decodeControlJSONLimit(w, r, &request, maxDocumentValidationJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_validation_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Document) == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "validation_document_required", "document is required")
		return
	}
	if len(request.Document) > maxValidationDocumentBytes || len(request.FleetDocument) > maxValidationDocumentBytes {
		WriteControlProblem(w, r, http.StatusBadRequest, "validation_document_too_large", "document and fleetDocument must each not exceed 65,536 bytes")
		return
	}
	spec, err := model.ParseInfraSpecDocument([]byte(request.Document))
	if err != nil {
		writeJSON(w, model.ValidationResult{SchemaVersion: "norn.validation-report/v1", DocumentKind: "infraspec", Valid: false, Findings: []model.ValidationFinding{{Severity: "error", Code: "infraspec.document.decode-failed", Field: "document", Message: err.Error(), Remediation: "Fix YAML syntax and unknown fields."}}})
		return
	}
	options := model.ValidationOptions{NetworkMode: "local", StrictSecrets: r.URL.Query().Get("strictSecrets") == "true"}
	if h.cfg != nil {
		options.NetworkMode = h.cfg.NetworkMode
		options.StrictSecrets = options.StrictSecrets || h.cfg.StrictSecrets
	}
	report := model.ValidateSpecWithOptions(spec, options)
	report.SchemaVersion = "norn.validation-report/v1"
	report.DocumentKind = "infraspec"
	if strings.TrimSpace(request.FleetDocument) != "" {
		config, fleetReport := fleet.ParseAndValidate([]byte(request.FleetDocument))
		if !fleetReport.Valid {
			report.Valid = false
			for _, finding := range fleetReport.Findings {
				if finding.Severity == "error" {
					report.Findings = append(report.Findings, model.ValidationFinding{Severity: "error", Code: "infraspec.fleet-context.invalid", Field: "fleetDocument." + finding.Field, Message: finding.Message, Remediation: finding.Remediation})
				}
			}
		} else {
			fleet.ValidateInfraSpec(spec, config, report)
		}
	}
	writeJSON(w, report)
}

func (h *Handler) FleetInventory(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	inventory, err := h.loadFleetInventory()
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_config_read_failed", "failed to read configured fleet document")
		return
	}
	writeJSON(w, inventory)
}

func (h *Handler) loadFleetInventory() (*fleet.Inventory, error) {
	path := ""
	if h.cfg != nil {
		path = strings.TrimSpace(h.cfg.FleetConfig)
	}
	// Source is intentionally a stable logical identifier, never the configured
	// checkout path. Fleet inventory is an authenticated API response and must
	// not disclose control-plane filesystem layout.
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
	config, report := fleet.ParseAndValidate(document)
	inventory.Digest = fleet.Digest(document)
	inventory.Document = config
	inventory.Validation = report
	if config != nil {
		inventory.NodePools = config.NodePools
	}
	return inventory, nil
}

func (h *Handler) PlanFleetCapacity(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIWrite)
	if !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_store_unavailable", "durable operation storage is unavailable")
		return
	}
	var request fleet.PlanRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", err.Error())
		return
	}
	if len(request.Reason) > 1000 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "reason must not exceed 1000 characters")
		return
	}
	if request.Size != "" {
		request.Size = strings.TrimSpace(request.Size)
		if request.Size == "" {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "size must not be blank")
			return
		}
	}
	if request.Strategy != "" {
		request.Strategy = strings.TrimSpace(request.Strategy)
		if request.Strategy == "" {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "strategy must not be blank")
			return
		}
	}
	request.Reason = strings.TrimSpace(request.Reason)
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must not exceed 200 characters")
		return
	}
	poolName := chi.URLParam(r, "pool")
	if !validAppIDRe.MatchString(poolName) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_request", "node pool name is invalid")
		return
	}
	idempotencyKey, requestDigest := fleetPlanIdempotency(principal, poolName, idempotencyKey, request)
	if idempotencyKey != "" {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); lookupErr == nil {
			if !matchesFleetPlanRequest(existing, requestDigest) {
				WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different fleet capacity plan")
				return
			}
			existing.AttachReceipt()
			writeJSON(w, existing)
			return
		} else if lookupErr != pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to resolve idempotent fleet plan")
			return
		}
	}
	inventory, err := h.loadFleetInventory()
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_config_read_failed", "failed to read configured fleet document")
		return
	}
	if !inventory.Configured || inventory.Document == nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_not_configured", "set NORN_FLEET_CONFIG to a checked-out norn-fleet Cluster document")
		return
	}
	if inventory.Validation == nil || !inventory.Validation.Valid {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_config_invalid", "fleet document must pass validation before planning")
		return
	}
	current, ok := inventory.NodePools[poolName]
	if !ok {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_node_pool_not_found", "node pool not found")
		return
	}
	signingKey := ""
	if h.cfg != nil {
		signingKey = h.cfg.AuditSigningKey
	}
	plan, planErr := buildCapacityPlan(inventory, poolName, current, request, signingKey)
	if planErr != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "fleet_plan_unsafe", planErr.Error())
		return
	}
	now := time.Now().UTC()
	finished := now
	payloadBytes, _ := json.Marshal(plan)
	var payload map[string]interface{}
	_ = json.Unmarshal(payloadBytes, &payload)
	op := &model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Status: model.OperationSucceeded, Risk: "read-only infrastructure capacity plan; Git review and protected apply remain required", Source: "control-api", Message: "capacity plan recorded; no provider mutation performed", Payload: payload, Metadata: map[string]interface{}{"planId": plan.ID, "planDigest": plan.Digest, "signature": plan.Signature}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	if idempotencyKey != "" {
		op.Metadata["idempotencyKey"] = idempotencyKey
		op.Metadata["requestDigest"] = requestDigest
	}
	if err := h.db.InsertCompletedOperation(r.Context(), op); err != nil {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" && idempotencyKey != "" {
			if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); lookupErr == nil {
				if !matchesFleetPlanRequest(existing, requestDigest) {
					WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different fleet capacity plan")
					return
				}
				existing.AttachReceipt()
				writeJSON(w, existing)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_store_failed", "failed to durably store capacity plan")
		return
	}
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func (h *Handler) ListFleetPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_store_unavailable", "durable operation storage is unavailable")
		return
	}
	limit := 50
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: "fleet.capacity-plan", Limit: limit})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read capacity plans")
		return
	}
	if ops == nil {
		ops = []model.Operation{}
	}
	for index := range ops {
		ops[index].AttachReceipt()
	}
	writeJSON(w, map[string]interface{}{"plans": ops, "count": len(ops)})
}

func buildCapacityPlan(inventory *fleet.Inventory, poolName string, current fleet.NodePool, request fleet.PlanRequest, signingKey string) (*fleet.CapacityPlan, error) {
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
	strategy := request.Strategy
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
	if signingKey == "" {
		findings = append(findings, fleet.Finding{Severity: "warning", Code: "fleet.plan.unsigned-development", Field: "signature", Message: "plan is durable but unsigned because NORN_AUDIT_SIGNING_KEY is not configured", Remediation: "Production mode requires an audit signing key."})
	}
	id := uuid.NewString()
	plan := &fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: id, Cluster: inventory.Document.Cluster.Name, Pool: poolName, Current: current, Proposed: proposed, Action: action, Strategy: strategy, Reason: strings.TrimSpace(request.Reason), SourceDigest: inventory.Digest, WorkflowURL: inventory.Document.Metadata.WorkflowURL, Findings: findings}
	sort.SliceStable(plan.Findings, func(i, j int) bool { return plan.Findings[i].Code < plan.Findings[j].Code })
	unsigned, _ := canonicalCapacityPlan(plan)
	sum := sha256.Sum256(unsigned)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	if signingKey != "" {
		mac := hmac.New(sha256.New, []byte(signingKey))
		_, _ = mac.Write([]byte(plan.Digest))
		plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	}
	return plan, nil
}

func canonicalCapacityPlan(plan *fleet.CapacityPlan) ([]byte, error) {
	canonical := *plan
	canonical.Digest = ""
	canonical.Signature = ""
	canonical.Findings = append([]fleet.Finding(nil), plan.Findings...)
	sort.SliceStable(canonical.Findings, func(i, j int) bool { return canonical.Findings[i].Code < canonical.Findings[j].Code })
	return json.Marshal(canonical)
}

func fleetPlanIdempotency(principal AccessPrincipal, pool, key string, request fleet.PlanRequest) (string, string) {
	if key == "" {
		return "", ""
	}
	requestBytes, _ := json.Marshal(struct {
		Pool    string            `json:"pool"`
		Request fleet.PlanRequest `json:"request"`
	}{Pool: pool, Request: request})
	requestSum := sha256.Sum256(requestBytes)
	identity := principal.TokenID
	if identity == "" {
		identity = principal.Subject
	}
	keySum := sha256.Sum256([]byte("fleet.capacity-plan\x00" + identity + "\x00" + key))
	return "fleet.capacity-plan:" + hex.EncodeToString(keySum[:]), "sha256:" + hex.EncodeToString(requestSum[:])
}

func matchesFleetPlanRequest(op *model.Operation, requestDigest string) bool {
	if op == nil || op.Kind != "fleet.capacity-plan" {
		return false
	}
	stored, _ := op.Metadata["requestDigest"].(string)
	return stored == requestDigest
}
