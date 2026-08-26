package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

var snapshotTimestampPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}$`)
var snapshotFilenamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,240}\.dump$`)

type appSnapshotRetentionRequest struct {
	Keep    int  `json:"keep"`
	Confirm bool `json:"confirm"`
}

type appRecoveryConfirmRequest struct {
	Confirm bool `json:"confirm"`
}

type appMigrationRequest struct {
	Ref     string `json:"ref,omitempty"`
	Confirm bool   `json:"confirm"`
}

type appRollbackRequest struct {
	Regions []string `json:"regions,omitempty"`
	Confirm bool     `json:"confirm"`
}

func (h *Handler) QueueAppSnapshot(w http.ResponseWriter, r *http.Request) {
	h.queueAppDataOperation(w, r, "app.snapshot", "database snapshot", map[string]interface{}{}, 2)
}

func (h *Handler) QueueAppSnapshotRetention(w http.ResponseWriter, r *http.Request) {
	var request appSnapshotRetentionRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_snapshot_retention", err.Error())
		return
	}
	if request.Keep < 1 || request.Keep > 1000 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_snapshot_retention", "keep must be between 1 and 1000")
		return
	}
	if !request.Confirm {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "snapshot pruning requires confirm=true")
		return
	}
	h.queueAppDataOperation(w, r, "app.snapshot-prune", "destructive snapshot retention", map[string]interface{}{"keep": request.Keep}, 1)
}

func (h *Handler) QueueAppSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	var request appRecoveryConfirmRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_snapshot_restore", err.Error())
		return
	}
	if !request.Confirm {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "snapshot restore requires confirm=true")
		return
	}
	identifier := chi.URLParam(r, "snapshot")
	if !snapshotTimestampPattern.MatchString(identifier) && !snapshotFilenamePattern.MatchString(identifier) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_snapshot_identifier", "snapshot identifier must be an inventory filename or legacy UTC timestamp")
		return
	}
	h.queueAppDataOperation(w, r, "app.snapshot-restore", "destructive database restore with safety snapshot", map[string]interface{}{"snapshot": identifier}, 1)
}

func (h *Handler) QueueAppMigration(w http.ResponseWriter, r *http.Request) {
	var request appMigrationRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_migration_request", err.Error())
		return
	}
	if !request.Confirm {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "schema migration requires confirm=true")
		return
	}
	request.Ref = strings.TrimSpace(request.Ref)
	if request.Ref == "" {
		request.Ref = "HEAD"
	}
	if !maintenanceRefPattern.MatchString(request.Ref) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_migration_ref", "migration ref is invalid")
		return
	}
	h.queueAppDataOperation(w, r, "app.migrate", "schema mutation with pre-migration snapshot", map[string]interface{}{"ref": request.Ref}, 1)
}

func (h *Handler) QueueAppRollback(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIWrite)
	if !ok {
		return
	}
	var request appRollbackRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_rollback_request", err.Error())
		return
	}
	if !request.Confirm {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "application rollback requires confirm=true")
		return
	}
	appID := chi.URLParam(r, "id")
	spec := h.findSpec(appID)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or deployment is disabled")
		return
	}
	declared := map[string]bool{}
	for _, region := range spec.ResolvedRegions() {
		declared[region.Name] = true
	}
	for _, region := range request.Regions {
		if !declared[region] {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_rollback_region", fmt.Sprintf("region %s is not declared", region))
			return
		}
	}
	idempotency, digest, ok := appOperationIdempotency(w, r, principal, appID, "app.rollback", request)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, idempotency, digest, "app.rollback", appID); handled {
		if existing != nil {
			existing.AttachReceipt()
			writeJSON(w, existing)
		}
		return
	}
	if !h.requireNoActiveAppOperation(w, r, appID) {
		return
	}
	deployments, err := h.db.ListDeployments(r.Context(), appID, 1)
	if err != nil || len(deployments) == 0 {
		WriteControlProblem(w, r, http.StatusNotFound, "rollback_current_deployment_missing", "no current deployment was found")
		return
	}
	previous, err := h.db.LastSuccessfulDeployment(r.Context(), appID, deployments[0].ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusNotFound, "rollback_target_missing", "no previous successful deployment is available")
		return
	}
	_, operationID, queueErr := h.pipeline.RollbackRegionsOperationContext(r.Context(), spec, deployments[0], previous, request.Regions, map[string]interface{}{
		"idempotencyKey": idempotency, "requestDigest": digest, "principal": principal.Subject,
	})
	if queueErr != nil {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotency); lookupErr == nil {
			storedDigest, _ := existing.Metadata["requestDigest"].(string)
			if existing.Kind == "app.rollback" && existing.App == appID && storedDigest == digest {
				existing.AttachReceipt()
				writeJSON(w, existing)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "rollback_queue_failed", "failed to persist rollback operation")
		return
	}
	op, err := h.db.GetOperation(r.Context(), operationID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "rollback_queue_failed", "failed to read the persisted rollback operation")
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusAccepted, op)
}

func (h *Handler) queueAppDataOperation(w http.ResponseWriter, r *http.Request, kind, risk string, payload map[string]interface{}, maxAttempts int) {
	principal, ok := requireControlScope(w, r, ScopeAPIWrite)
	if !ok {
		return
	}
	if h.db == nil || h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "durable app operations are unavailable")
		return
	}
	appID := chi.URLParam(r, "id")
	spec := h.findSpec(appID)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or deployment is disabled")
		return
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		WriteControlProblem(w, r, http.StatusConflict, "postgres_not_configured", "app has no postgres database")
		return
	}
	if kind == "app.migrate" && strings.TrimSpace(spec.Migrations) == "" {
		WriteControlProblem(w, r, http.StatusConflict, "migration_not_configured", "app has no migrations command")
		return
	}
	request := map[string]interface{}{"payload": payload}
	idempotency, digest, ok := appOperationIdempotency(w, r, principal, appID, kind, request)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, idempotency, digest, kind, appID); handled {
		if existing != nil {
			existing.AttachReceipt()
			writeJSON(w, existing)
		}
		return
	}
	if !h.requireNoActiveAppOperation(w, r, appID) {
		return
	}
	ref := ""
	if kind == "app.migrate" {
		ref, _ = payload["ref"].(string)
	}
	now := time.Now().UTC()
	op := &model.Operation{
		ID: uuid.NewString(), Kind: kind, App: appID, SagaID: uuid.NewString(), Ref: ref,
		Status: model.OperationQueued, Risk: risk, Source: "control-api", Message: "queued " + kind,
		Payload: payload, Metadata: map[string]interface{}{
			"idempotencyKey": idempotency, "requestDigest": digest, "principal": principal.Subject,
		}, StartedAt: now, MaxAttempts: maxAttempts,
	}
	if err := h.db.InsertOperation(r.Context(), op); err != nil {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotency); lookupErr == nil && existing.Kind == kind && existing.App == appID {
			storedDigest, _ := existing.Metadata["requestDigest"].(string)
			if storedDigest == digest {
				existing.AttachReceipt()
				writeJSON(w, existing)
				return
			}
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different app operation")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_create_failed", "failed to durably queue app operation")
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusAccepted, op)
}

func appOperationIdempotency(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, appID, kind string, request interface{}) (string, string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
		return "", "", false
	}
	requestBytes, _ := json.Marshal(request)
	requestSum := sha256.Sum256(requestBytes)
	identity := principal.TokenID
	if identity == "" {
		identity = principal.Subject
	}
	keySum := sha256.Sum256([]byte(kind + "\x00" + identity + "\x00" + appID + "\x00" + key))
	return kind + ":" + hex.EncodeToString(keySum[:]), "sha256:" + hex.EncodeToString(requestSum[:]), true
}

func (h *Handler) resolveAppOperationIdempotency(w http.ResponseWriter, r *http.Request, key, digest, kind, appID string) (*model.Operation, bool) {
	existing, err := h.db.GetOperationByIdempotencyKey(r.Context(), key)
	if err == pgx.ErrNoRows {
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to resolve idempotent app operation")
		return nil, true
	}
	storedDigest, _ := existing.Metadata["requestDigest"].(string)
	if existing.Kind != kind || existing.App != appID || storedDigest != digest {
		WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different app operation")
		return nil, true
	}
	return existing, true
}

func (h *Handler) requireNoActiveAppOperation(w http.ResponseWriter, r *http.Request, appID string) bool {
	operations, err := h.db.ListOperations(r.Context(), store.OperationFilter{App: appID, Active: true, Limit: 1})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to inspect active app operations")
		return false
	}
	if len(operations) > 0 {
		WriteControlProblem(w, r, http.StatusConflict, "app_operation_active", "another durable operation is already active for this app")
		return false
	}
	return true
}
