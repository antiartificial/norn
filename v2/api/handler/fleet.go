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
	"regexp"
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

var (
	fleetCommitSHARe      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	fleetPlanSHARe        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fleetEvidenceDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

var fleetReconciliationPhases = []string{
	"infrastructure_applied",
	"inventory_generated",
	"nodes_configured",
	"nodes_enrolled",
	"readiness_verified",
	"old_nodes_drained",
	"complete",
}

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

func (h *Handler) ListFleetReconciliations(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_reconciliation_store_unavailable", "durable operation storage is unavailable")
		return
	}
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
		return
	}
	plan, err := h.db.GetOperation(r.Context(), planID)
	if err == pgx.ErrNoRows || (err == nil && plan.Kind != "fleet.capacity-plan") {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read fleet capacity plan")
		return
	}
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: "fleet.reconciliation", Ref: planID, Limit: 100})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_reconciliation_read_failed", "failed to read reconciliation checkpoints")
		return
	}
	for index := range ops {
		ops[index].AttachReceipt()
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"schemaVersion": fleet.ReconciliationSchemaVersion, "planId": planID, "reconciliations": ops, "count": len(ops)})
}

func (h *Handler) RecordFleetReconciliation(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIWrite)
	if !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_reconciliation_store_unavailable", "durable operation storage is unavailable")
		return
	}
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
		return
	}
	plan, err := h.db.GetOperation(r.Context(), planID)
	if err == pgx.ErrNoRows || (err == nil && plan.Kind != "fleet.capacity-plan") {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read fleet capacity plan")
		return
	}
	var request fleet.ReconciliationRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_reconciliation", err.Error())
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if err := validateFleetReconciliationRequest(request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_reconciliation", err.Error())
		return
	}
	existing, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: "fleet.reconciliation", Ref: planID, Limit: 100})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_reconciliation_read_failed", "failed to read reconciliation checkpoints")
		return
	}
	if err := validateFleetReconciliationTransition(plan, existing, request); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_reconciliation_out_of_order", err.Error())
		return
	}
	idempotencyKey, requestDigest := fleetReconciliationIdempotency(principal, planID, strings.TrimSpace(r.Header.Get("Idempotency-Key")), request)
	if replay, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); lookupErr == nil {
		if !matchesFleetReconciliationRequest(replay, requestDigest) {
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for different reconciliation evidence")
			return
		}
		replay.AttachReceipt()
		writeJSON(w, replay)
		return
	} else if lookupErr != pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to resolve idempotent reconciliation checkpoint")
		return
	}

	now := time.Now().UTC()
	finished := now
	payloadBytes, _ := json.Marshal(request)
	var payload map[string]interface{}
	_ = json.Unmarshal(payloadBytes, &payload)
	status := model.OperationSucceeded
	if request.Status == "failed" {
		status = model.OperationFailed
	}
	op := &model.Operation{
		ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: planID, Status: status,
		Risk: "append-only infrastructure reconciliation evidence", Source: "fleet-runner",
		Message: request.Message, Payload: payload,
		Metadata:  map[string]interface{}{"idempotencyKey": idempotencyKey, "requestDigest": requestDigest},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1,
	}
	if op.Message == "" {
		op.Message = request.Phase + " " + request.Status
	}
	if err := h.db.InsertCompletedOperation(r.Context(), op); err != nil {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			if replay, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); lookupErr == nil && matchesFleetReconciliationRequest(replay, requestDigest) {
				replay.AttachReceipt()
				writeJSON(w, replay)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_reconciliation_store_failed", "failed to store reconciliation checkpoint")
		return
	}
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func validateFleetReconciliationRequest(request fleet.ReconciliationRequest) error {
	if request.SchemaVersion != fleet.ReconciliationSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", fleet.ReconciliationSchemaVersion)
	}
	known := false
	for _, phase := range fleetReconciliationPhases {
		known = known || request.Phase == phase
	}
	if !known {
		return fmt.Errorf("phase is not supported")
	}
	if request.Status != "succeeded" && request.Status != "failed" {
		return fmt.Errorf("status must be succeeded or failed")
	}
	if !fleetCommitSHARe.MatchString(request.CommitSHA) {
		return fmt.Errorf("commitSha must be a lowercase 40-character Git SHA")
	}
	if !fleetPlanSHARe.MatchString(request.PlanSHA256) {
		return fmt.Errorf("planSha256 must be a lowercase SHA-256 digest")
	}
	if !fleetEvidenceDigestRe.MatchString(request.EvidenceDigest) {
		return fmt.Errorf("evidenceDigest must use sha256:<64 lowercase hex characters>")
	}
	if request.StateSerial < 0 {
		return fmt.Errorf("stateSerial cannot be negative")
	}
	if request.Status == "succeeded" && request.Phase == "infrastructure_applied" && request.StateSerial == 0 {
		return fmt.Errorf("successful infrastructure_applied evidence requires a positive stateSerial")
	}
	if len(request.Message) > 1000 {
		return fmt.Errorf("message must not exceed 1000 characters")
	}
	return nil
}

func validateFleetReconciliationTransition(plan *model.Operation, existing []model.Operation, request fleet.ReconciliationRequest) error {
	succeeded := map[string]bool{}
	for _, op := range existing {
		commit, _ := op.Payload["commitSha"].(string)
		planSHA, _ := op.Payload["planSha256"].(string)
		if commit != request.CommitSHA || planSHA != request.PlanSHA256 {
			return fmt.Errorf("checkpoint binding differs from the existing reconciliation")
		}
		if op.Status != model.OperationSucceeded {
			continue
		}
		phase, _ := op.Payload["phase"].(string)
		succeeded[phase] = true
	}
	if request.Status == "failed" || succeeded[request.Phase] {
		return nil
	}
	predecessor := map[string]string{
		"inventory_generated": "infrastructure_applied",
		"nodes_configured":    "inventory_generated",
		"nodes_enrolled":      "nodes_configured",
		"readiness_verified":  "nodes_enrolled",
		"old_nodes_drained":   "readiness_verified",
	}
	if request.Phase == "complete" {
		predecessor[request.Phase] = "readiness_verified"
		if fleetPlanRequiresDrain(plan) {
			predecessor[request.Phase] = "old_nodes_drained"
		}
	}
	if required := predecessor[request.Phase]; required != "" && !succeeded[required] {
		return fmt.Errorf("phase %s requires successful %s evidence", request.Phase, required)
	}
	return nil
}

func fleetPlanRequiresDrain(plan *model.Operation) bool {
	if plan == nil {
		return false
	}
	action, _ := plan.Payload["action"].(string)
	if action == "replace" {
		return true
	}
	current, _ := plan.Payload["current"].(map[string]interface{})
	proposed, _ := plan.Payload["proposed"].(map[string]interface{})
	currentDesired := numberAsFloat64(current["desired"])
	proposedDesired := numberAsFloat64(proposed["desired"])
	return action == "scale" && proposedDesired < currentDesired
}

func numberAsFloat64(value interface{}) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	default:
		return 0
	}
}

func fleetReconciliationIdempotency(principal AccessPrincipal, planID, key string, request fleet.ReconciliationRequest) (string, string) {
	requestBytes, _ := json.Marshal(request)
	requestSum := sha256.Sum256(requestBytes)
	requestDigest := "sha256:" + hex.EncodeToString(requestSum[:])
	if key == "" {
		key = request.Phase + ":" + requestDigest
	}
	identity := principal.TokenID
	if identity == "" {
		identity = principal.Subject
	}
	keySum := sha256.Sum256([]byte("fleet.reconciliation\x00" + identity + "\x00" + planID + "\x00" + key))
	return "fleet.reconciliation:" + hex.EncodeToString(keySum[:]), requestDigest
}

func matchesFleetReconciliationRequest(op *model.Operation, requestDigest string) bool {
	if op == nil || op.Kind != "fleet.reconciliation" {
		return false
	}
	stored, _ := op.Metadata["requestDigest"].(string)
	return stored == requestDigest
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
