package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/api/model"
)

func (h *Handler) ListClusterNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.db.ListClusterNodes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, nodes)
}

func (h *Handler) GetClusterNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")

	node, err := h.db.GetClusterNode(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if node == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	writeJSON(w, node)
}

func (h *Handler) AddClusterNode(w http.ResponseWriter, r *http.Request) {
	var node model.ClusterNode
	if err := json.NewDecoder(r.Body).Decode(&node); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	node.ID = uuid.New().String()
	node.Status = "provisioning"
	node.CreatedAt = time.Now()

	if err := h.db.InsertClusterNode(r.Context(), &node); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(node)
}

func (h *Handler) RemoveClusterNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")

	if err := h.db.DeleteClusterNode(r.Context(), nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
