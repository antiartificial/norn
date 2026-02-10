package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/api/hub"
)

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

	// Check if deployment already exists
	_, err = h.kube.GetDeployment(r.Context(), "default", appID)
	if err == nil {
		http.Error(w, "deployment already exists — use deploy instead", http.StatusConflict)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "forge.queued", AppID: appID, Payload: map[string]string{
		"app": appID,
	}})

	go h.forgePipeline.Run(spec)

	writeJSON(w, map[string]string{"status": "forging", "app": appID})
}
