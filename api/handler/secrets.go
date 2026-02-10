package handler

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"norn/api/hub"
)

func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	// Try to list actual encrypted secret keys; fall back to spec-declared names
	keys, err := h.secrets.List(appID)
	if err != nil {
		if os.IsNotExist(err) {
			keys = spec.Secrets
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, map[string]interface{}{
		"app":     appID,
		"secrets": keys,
	})
}

func (h *Handler) UpdateSecrets(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	var updates map[string]string
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.secrets.Set(appID, updates); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Sync to K8s
	if err := h.secrets.SyncToK8s(r.Context(), appID, "default"); err != nil {
		http.Error(w, "secrets saved but k8s sync failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "secrets.updated", AppID: appID})

	writeJSON(w, map[string]string{"status": "updated"})
}
