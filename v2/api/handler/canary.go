package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// CanaryStatus returns the latest Nomad deployment status for an app,
// including whether canary allocations are in progress.
func (h *Handler) CanaryStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, nomadRegion, ok := h.canaryRegion(w, r, id)
	if !ok {
		return
	}

	info, err := h.nomad.LatestDeploymentRegion(id, nomadRegion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("latest deployment: %v", err))
		return
	}
	if info == nil {
		writeJSON(w, map[string]string{"status": "none"})
		return
	}

	writeJSON(w, info)
}

// PromoteCanary accepts a signed, durable promotion of the current canary
// deployment. It captures the deployment ID before acceptance, so a later
// Nomad deployment cannot be promoted by a recovered worker.
func (h *Handler) PromoteCanary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.pipeline == nil || !h.pipeline.CanaryPromotionAvailable() || h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_canary_promotion_unavailable", "durable Nomad canary promotion is unavailable")
		return
	}
	logicalRegion, nomadRegion, ok := h.canaryRegion(w, r, id)
	if !ok {
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "region": logicalRegion, "nomadRegion": nomadRegion})
	if !ok {
		return
	}
	// Recover an existing signed identity before reading Nomad. Retrying after a
	// successful promotion has no live canary, but must still return its receipt.
	if accepted, replayed, err := h.resolveCanaryPromotionReplay(r.Context(), enqueue, id, logicalRegion, nomadRegion); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if replayed {
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		writeJSON(w, accepted.Operation)
		return
	}
	info, err := h.nomad.LatestDeploymentRegion(id, nomadRegion)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "canary_state_unavailable", "could not determine the current Nomad canary deployment")
		return
	}
	if info == nil || info.ID == "" || info.JobID != id || !info.IsCanary {
		WriteControlProblem(w, r, http.StatusConflict, "no_current_canary", "the current app region has no promotable canary deployment")
		return
	}
	enqueue.Semantics = map[string]interface{}{"app": id, "region": logicalRegion, "nomadRegion": nomadRegion, "deploymentId": info.ID}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.canary-promote", App: id, SagaID: uuid.NewString(), Ref: logicalRegion + "/" + info.ID, Status: model.OperationQueued, Risk: "Nomad canary promotion", Source: "app-control-api", Message: fmt.Sprintf("queued canary promotion for %s in %s", id, logicalRegion), StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"app": id, "region": logicalRegion, "nomadRegion": nomadRegion, "deploymentId": info.ID}}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

func (h *Handler) canaryRegion(w http.ResponseWriter, r *http.Request, app string) (string, string, bool) {
	if h == nil || h.cfg == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_discovery_unavailable", "app intent is unavailable")
		return "", "", false
	}
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_discovery_failed", "failed to discover app intent")
		return "", "", false
	}
	var spec *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == app {
			spec = candidate
			break
		}
	}
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found")
		return "", "", false
	}
	logicalRegion := r.URL.Query().Get("region")
	regions := spec.ResolvedRegions()
	if logicalRegion == "" && len(regions) == 1 {
		logicalRegion = regions[0].Name
	}
	for _, region := range regions {
		if region.Name == logicalRegion && region.NomadRegion != "" {
			return logicalRegion, region.NomadRegion, true
		}
	}
	WriteControlProblem(w, r, http.StatusBadRequest, "invalid_canary_region", "region is required and must be declared for the app")
	return "", "", false
}

func (h *Handler) resolveCanaryPromotionReplay(ctx context.Context, request pipeline.EnqueueRequest, app, region, nomadRegion string) (store.AcceptedOperation, bool, error) {
	accepted, err := h.pipeline.ResolveEnqueue(ctx, request, "app.canary-promote", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, false, nil
	}
	if err != nil {
		return store.AcceptedOperation{}, false, err
	}
	if accepted.Operation.Kind != "app.canary-promote" || accepted.Operation.App != app || canaryPayloadString(accepted.Operation.Payload, "region") != region || canaryPayloadString(accepted.Operation.Payload, "nomadRegion") != nomadRegion || canaryPayloadString(accepted.Operation.Payload, "deploymentId") == "" {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.canary-promote", Resource: app}}
	}
	return accepted, true, nil
}

func canaryPayloadString(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}
