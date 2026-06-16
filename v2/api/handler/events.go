package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	events, total, err := h.db.ListBeaconEvents(r.Context(), store.BeaconFilter{
		App:      r.URL.Query().Get("app"),
		Type:     r.URL.Query().Get("type"),
		Severity: r.URL.Query().Get("severity"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"events": events,
		"total":  total,
	})
}

func (h *Handler) ActiveIncidents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	incidents, err := h.db.ListActiveIncidents(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if incidents == nil {
		incidents = []store.ActiveIncident{}
	}
	writeJSON(w, map[string]interface{}{
		"incidents": incidents,
	})
}

func (h *Handler) CorrelatedEvents(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key parameter is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := h.db.ListCorrelatedEvents(r.Context(), key, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"events":         events,
		"correlationKey": key,
	})
}

func (h *Handler) GetEvent(w http.ResponseWriter, r *http.Request) {
	event, err := h.db.GetBeaconEvent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, event)
}

func (h *Handler) AcknowledgeEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		By   string `json:"by"`
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.By == "" {
		body.By = "operator"
	}
	event, err := h.db.AcknowledgeBeaconEvent(r.Context(), chi.URLParam(r, "id"), body.By, body.Note)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, event)
}

func (h *Handler) SnoozeEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		By       string `json:"by"`
		Note     string `json:"note"`
		Until    string `json:"until"`
		Duration string `json:"duration"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.By == "" {
		body.By = "operator"
	}
	until := time.Time{}
	if body.Until != "" {
		parsed, err := time.Parse(time.RFC3339, body.Until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
		until = parsed
	} else {
		if body.Duration == "" {
			body.Duration = "1h"
		}
		duration, err := time.ParseDuration(body.Duration)
		if err != nil || duration <= 0 {
			writeError(w, http.StatusBadRequest, "duration must be a positive Go duration such as 1h or 30m")
			return
		}
		until = time.Now().UTC().Add(duration)
	}
	event, err := h.db.SnoozeBeaconEvent(r.Context(), chi.URLParam(r, "id"), body.By, body.Note, until)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, event)
}

func (h *Handler) OpenEvent(w http.ResponseWriter, r *http.Request) {
	event, err := h.db.OpenBeaconEvent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, event)
}

func (h *Handler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	if h.beacon == nil {
		writeError(w, http.StatusServiceUnavailable, "beacon not initialized")
		return
	}

	var event model.BeaconEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if event.Type == "" || event.Title == "" {
		writeError(w, http.StatusBadRequest, "type and title are required")
		return
	}

	created, err := h.beacon.Emit(r.Context(), event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, created)
}

func (h *Handler) EventSinks(w http.ResponseWriter, r *http.Request) {
	if h.beacon == nil {
		writeJSON(w, model.BeaconSinkStatus{})
		return
	}
	writeJSON(w, h.beacon.SinkStatus())
}

func (h *Handler) TestEvent(w http.ResponseWriter, r *http.Request) {
	if h.beacon == nil {
		writeError(w, http.StatusServiceUnavailable, "beacon not initialized")
		return
	}

	var body struct {
		App string `json:"app"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.App == "" {
		body.App = "norn"
	}

	event, err := h.beacon.Emit(r.Context(), model.BeaconEvent{
		App:        body.App,
		Type:       "beacon.test",
		Severity:   model.BeaconInfo,
		Title:      "Norn Beacon test",
		Body:       "This is a test event emitted by Norn Beacon.",
		DedupeKey:  body.App + ":beacon.test",
		OccurredAt: time.Now().UTC(),
		Metadata: map[string]interface{}{
			"manual": true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, event)
}
