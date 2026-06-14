package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/store"
)

func (h *Handler) ListAccessGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := h.db.ListAccessGrants(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list grants")
		return
	}
	if grants == nil {
		grants = []store.AccessGrant{}
	}
	writeJSON(w, map[string]interface{}{"grants": grants})
}

func (h *Handler) CreateAccessGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP  string `json:"ip"`
		Note string `json:"note"`
		TTL  string `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IP == "" {
		writeError(w, http.StatusBadRequest, "ip is required")
		return
	}
	if req.TTL == "" {
		writeError(w, http.StatusBadRequest, "ttl is required")
		return
	}
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ttl duration")
		return
	}
	now := time.Now().UTC()
	g := &store.AccessGrant{
		IP:        req.IP,
		Note:      req.Note,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := h.db.CreateAccessGrant(r.Context(), g); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create grant")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, g)
}

func (h *Handler) DeleteAccessGrant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteAccessGrant(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete grant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
