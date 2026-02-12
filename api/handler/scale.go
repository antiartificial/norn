package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/api/hub"
)

type ScaleRequest struct {
	Replicas int `json:"replicas"`
}

func (h *Handler) Scale(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "scale requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	var req ScaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Replicas < 0 || req.Replicas > 10 {
		http.Error(w, "replicas must be between 0 and 10", http.StatusBadRequest)
		return
	}

	if err := h.kube.SetReplicas(r.Context(), "default", appID, int32(req.Replicas)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{
		Type:    "app.scaled",
		AppID:   appID,
		Payload: map[string]int{"replicas": req.Replicas},
	})

	writeJSON(w, map[string]interface{}{"status": "scaled", "replicas": req.Replicas})
}
