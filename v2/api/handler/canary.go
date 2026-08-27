package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

// CanaryStatus returns the latest Nomad deployment status for an app,
// including whether canary allocations are in progress.
func (h *Handler) CanaryStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireNomadConnector(w) {
		return
	}
	id := chi.URLParam(r, "id")
	region := r.URL.Query().Get("region")

	info, err := h.nomad.LatestDeploymentRegion(id, region)
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

// PromoteCanary promotes all canary allocations in the latest deployment for an app.
func (h *Handler) PromoteCanary(w http.ResponseWriter, r *http.Request) {
	if !h.requireNomadConnector(w) {
		return
	}
	id := chi.URLParam(r, "id")
	region := r.URL.Query().Get("region")

	if err := h.nomad.PromoteDeploymentRegion(id, region); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("promote canary: %v", err))
		return
	}

	if h.beacon != nil {
		h.beacon.Emit(r.Context(), model.BeaconEvent{
			App:       id,
			Type:      "canary.promoted",
			Severity:  model.BeaconInfo,
			Title:     fmt.Sprintf("%s canary promoted", id),
			Body:      fmt.Sprintf("Canary deployment for %s was manually promoted.", id),
			DedupeKey: fmt.Sprintf("%s:canary", id),
			Metadata: map[string]interface{}{
				"app":            id,
				"source":         "api",
				"correlationKey": fmt.Sprintf("%s:deploy", id),
			},
		})
	}

	writeJSON(w, map[string]string{"status": "promoted"})
}
