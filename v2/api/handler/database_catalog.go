package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
)

type catalogActivationRequest struct {
	ExpectedRevision *int64            `json:"expectedRevision"`
	Catalog          *database.Catalog `json:"catalog"`
}

// GetDatabaseCatalog is the redacted inspection of the active catalog.
func (h *Handler) GetDatabaseCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "database_catalog_unavailable", "the control store is unavailable")
		return
	}
	active, err := h.db.ActiveDatabaseCatalog(r.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		WriteControlProblem(w, r, http.StatusNotFound, "database_catalog_not_active", "no database catalog revision is active")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "database_catalog_unreadable", "the active database catalog could not be verified")
		return
	}
	writeJSON(w, database.InspectCatalog(active.Revision, active.Digest, active.Catalog))
}

// ActivateDatabaseCatalog accepts a catalog activation as a durable,
// idempotent operation bound to the request's mutation-audit receipt; the
// operation worker applies it with the expected-revision compare-and-set.
func (h *Handler) ActivateDatabaseCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopePlatformOperate); !ok {
		return
	}
	if h.db == nil || h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "durable operations are unavailable")
		return
	}
	var request catalogActivationRequest
	if err := decodeControlJSONLimit(w, r, &request, 1<<20); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_catalog_activation", err.Error())
		return
	}
	if request.ExpectedRevision == nil || request.Catalog == nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_catalog_activation", "expectedRevision and catalog are required")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"action": pipeline.CatalogActivationKind, "expectedRevision": *request.ExpectedRevision})
	if !ok {
		return
	}
	accepted, err := h.pipeline.QueueCatalogActivation(r.Context(), *request.Catalog, *request.ExpectedRevision, enqueue)
	var refused *pipeline.CatalogActivationError
	if errors.As(err, &refused) {
		WriteControlProblem(w, r, http.StatusConflict, "catalog_activation_refused", refused.Reason)
		return
	}
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	// The response never echoes the catalog payload.
	operation := accepted.Operation
	operation.Payload = map[string]interface{}{"expectedRevision": operation.Payload["expectedRevision"], "catalogDigest": operation.Payload["catalogDigest"]}
	if accepted.Replayed {
		writeJSON(w, operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, operation)
}

// RecordDatabaseBaseline accepts an operator attestation that the app's
// running writers already use its current named targets (a legacy-to-named
// transition). It is a durable, idempotent, audited operation; execution
// probes every writer database's identity. It cannot contradict recorded
// targets and is not a cutover.
func (h *Handler) RecordDatabaseBaseline(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopePlatformOperate); !ok {
		return
	}
	if h.db == nil || h.pipeline == nil || h.pipeline.DatabaseTargets == nil {
		WriteControlProblem(w, r, http.StatusConflict, "database_profile_not_configured", "a database baseline requires a database profile")
		return
	}
	var request appRecoveryConfirmRequest
	if err := decodeControlJSON(w, r, &request); err != nil || !request.Confirm {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "a database baseline requires confirm=true")
		return
	}
	appID := chi.URLParam(r, "id")
	spec := h.findSpec(appID)
	if spec == nil || !spec.NamedDatabases() {
		WriteControlProblem(w, r, http.StatusNotFound, "named_databases_not_declared", "app was not found or declares no named databases")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"action": pipeline.DatabaseBaselineKind, "app": appID})
	if !ok {
		return
	}
	enqueue.Admission.OneActiveMutablePerApp = true
	op := model.Operation{ID: uuid.NewString(), Kind: pipeline.DatabaseBaselineKind, App: appID, SagaID: uuid.NewString(), Status: model.OperationQueued,
		Risk: "attests running database writers' targets", Source: "control-api", Message: "queued database baseline",
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

// AppDatabaseHealth probes the app's current database targets.
func (h *Handler) AppDatabaseHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	spec := h.findSpec(chi.URLParam(r, "id"))
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or deployment is disabled")
		return
	}
	if h.pipeline == nil || h.pipeline.DatabaseTargets == nil {
		WriteControlProblem(w, r, http.StatusConflict, "database_profile_not_configured", "database health requires a database profile (NORN_DATABASE_PROFILE)")
		return
	}
	health, err := h.pipeline.DatabaseHealth(r.Context(), spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "database_health_unavailable", err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"app": spec.App, "databases": health})
}
