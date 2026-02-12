package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) CronHistory(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	execs, err := h.db.ListCronExecutions(r.Context(), app, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Also include current cron state
	state := h.scheduler.GetState(app)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"executions": execs,
		"state":      state,
	})
}

func (h *Handler) CronTrigger(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	if h.scheduler == nil {
		http.Error(w, "scheduler not initialized", http.StatusServiceUnavailable)
		return
	}
	if err := h.scheduler.Trigger(app); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "triggered"})
}

func (h *Handler) CronPause(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	if h.scheduler == nil {
		http.Error(w, "scheduler not initialized", http.StatusServiceUnavailable)
		return
	}
	if err := h.scheduler.Pause(app); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "paused"})
}

func (h *Handler) CronResume(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	if h.scheduler == nil {
		http.Error(w, "scheduler not initialized", http.StatusServiceUnavailable)
		return
	}
	if err := h.scheduler.Resume(app); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "resumed"})
}

func (h *Handler) CronUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	if h.scheduler == nil {
		http.Error(w, "scheduler not initialized", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Schedule string `json:"schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Schedule == "" {
		http.Error(w, "schedule is required", http.StatusBadRequest)
		return
	}

	if err := h.scheduler.UpdateSchedule(app, body.Schedule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}
