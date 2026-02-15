package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/api/dispatch"
)

// WorkerHandler handles worker management API endpoints.
type WorkerHandler struct {
	registry *dispatch.Registry
}

func NewWorkerHandler(registry *dispatch.Registry) *WorkerHandler {
	return &WorkerHandler{
		registry: registry,
	}
}

func (h *WorkerHandler) ListWorkers(w http.ResponseWriter, r *http.Request) {
	workers := h.registry.List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workers)
}

func (h *WorkerHandler) GetWorker(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	worker := h.registry.Get(id)
	if worker == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(worker)
}

func (h *WorkerHandler) DrainWorker(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.registry.Drain(id) {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "draining"})
}

func (h *WorkerHandler) RemoveWorker(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.registry.Unregister(id)
	w.WriteHeader(http.StatusNoContent)
}
