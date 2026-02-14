package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	keys, err := h.secrets.List(id)
	if err != nil {
		// No secrets file is OK
		writeJSON(w, []string{})
		return
	}
	writeJSON(w, keys)
}

func (h *Handler) UpdateSecrets(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var updates map[string]string
	if err := decodeJSON(r, &updates); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.secrets.Set(id, updates); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")
	if err := h.secrets.Delete(id, key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
