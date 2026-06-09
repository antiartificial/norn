package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

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
