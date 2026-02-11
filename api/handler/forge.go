package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/api/hub"
	"norn/api/model"
)

type forgeRequest struct {
	Force bool `json:"force"`
}

func (h *Handler) Forge(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "forge requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	var req forgeRequest
	json.NewDecoder(r.Body).Decode(&req)

	state, err := h.db.GetForgeState(r.Context(), appID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Default to unforged if no record
	currentStatus := model.ForgeUnforged
	if state != nil {
		currentStatus = state.Status
	}

	switch currentStatus {
	case model.ForgeForging, model.ForgeTearingDown:
		http.Error(w, "operation already in progress", http.StatusConflict)
		return

	case model.ForgeForged:
		if !req.Force {
			http.Error(w, "already forged — use force to re-forge", http.StatusConflict)
			return
		}
		// Force re-forge: teardown first, then forge
		go h.forceReforge(state, spec)
		writeJSON(w, map[string]string{"status": "tearing_down", "app": appID})
		return

	case model.ForgeUnforged:
		// Fresh forge
		now := time.Now()
		state = &model.ForgeState{
			App:       appID,
			Status:    model.ForgeForging,
			Steps:     nil,
			Resources: model.ForgeResources{},
			StartedAt: &now,
		}
		if err := h.db.UpsertForgeState(r.Context(), state); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		h.ws.Broadcast(hub.Event{Type: "forge.queued", AppID: appID, Payload: map[string]string{
			"app": appID,
		}})

		go h.forgePipeline.Run(state, spec, 0)
		writeJSON(w, map[string]string{"status": "forging", "app": appID})
		return

	case model.ForgeFailed:
		// Resume from last completed step
		resumeFrom := countCompletedSteps(state.Steps)
		now := time.Now()
		state.Status = model.ForgeForging
		state.Steps = nil
		state.Error = ""
		state.StartedAt = &now
		state.FinishedAt = nil
		if err := h.db.UpsertForgeState(r.Context(), state); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		h.ws.Broadcast(hub.Event{Type: "forge.queued", AppID: appID, Payload: map[string]string{
			"app": appID,
		}})

		go h.forgePipeline.Run(state, spec, resumeFrom)
		writeJSON(w, map[string]string{"status": "forging", "app": appID})
		return
	}
}

func (h *Handler) Teardown(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "teardown requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	_, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	state, err := h.db.GetForgeState(r.Context(), appID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if state == nil {
		http.Error(w, "nothing to tear down — app is unforged", http.StatusBadRequest)
		return
	}

	switch state.Status {
	case model.ForgeUnforged:
		http.Error(w, "nothing to tear down — app is unforged", http.StatusBadRequest)
		return
	case model.ForgeForging, model.ForgeTearingDown:
		http.Error(w, "operation already in progress", http.StatusConflict)
		return
	case model.ForgeForged, model.ForgeFailed:
		// OK to tear down
	}

	now := time.Now()
	state.Status = model.ForgeTearingDown
	state.Error = ""
	state.StartedAt = &now
	state.FinishedAt = nil
	if err := h.db.UpsertForgeState(r.Context(), state); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "teardown.queued", AppID: appID, Payload: map[string]string{
		"app": appID,
	}})

	go h.teardownPipeline.Run(state)
	writeJSON(w, map[string]string{"status": "tearing_down", "app": appID})
}

func (h *Handler) GetForgeState(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	state, err := h.db.GetForgeState(r.Context(), appID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if state == nil {
		state = &model.ForgeState{
			App:    appID,
			Status: model.ForgeUnforged,
		}
	}

	writeJSON(w, state)
}

func (h *Handler) forceReforge(state *model.ForgeState, spec *model.InfraSpec) {
	ctx := context.Background()

	// Step 1: teardown
	now := time.Now()
	state.Status = model.ForgeTearingDown
	state.Error = ""
	state.StartedAt = &now
	state.FinishedAt = nil
	h.db.UpsertForgeState(ctx, state)

	h.teardownPipeline.Run(state)

	// If teardown failed, don't continue with forge
	if state.Status != model.ForgeUnforged {
		return
	}

	// Step 2: fresh forge
	now2 := time.Now()
	state.Status = model.ForgeForging
	state.Steps = nil
	state.Resources = model.ForgeResources{}
	state.StartedAt = &now2
	state.FinishedAt = nil
	h.db.UpsertForgeState(ctx, state)

	h.ws.Broadcast(hub.Event{Type: "forge.queued", AppID: spec.App, Payload: map[string]string{
		"app": spec.App,
	}})

	h.forgePipeline.Run(state, spec, 0)
}

func countCompletedSteps(steps []model.ForgeStepLog) int {
	count := 0
	for _, s := range steps {
		if s.Status == "completed" || s.Status == "skipped" {
			count++
		} else {
			break // Stop at first non-completed step
		}
	}
	return count
}
