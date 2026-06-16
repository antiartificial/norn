package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/beacon"
	"norn/v2/api/model"
)

func (h *Handler) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.db.ListNotificationChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if channels == nil {
		channels = []model.NotificationChannel{}
	}
	writeJSON(w, map[string]interface{}{
		"channels": channels,
	})
}

func (h *Handler) CreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var ch model.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if ch.Provider == "" || ch.Name == "" {
		writeError(w, http.StatusBadRequest, "provider and name are required")
		return
	}

	switch ch.Provider {
	case "discord", "ntfy", "pushover", "webhook":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "provider must be discord, ntfy, pushover, or webhook")
		return
	}

	ch.ID = "nch_" + uuid.NewString()
	ch.CreatedAt = time.Now().UTC()

	if err := h.db.InsertNotificationChannel(r.Context(), &ch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, ch)
}

func (h *Handler) BootstrapNotificationChannels(w http.ResponseWriter, r *http.Request) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build service manifest")
		return
	}

	existing, err := h.db.ListNotificationChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var created []model.NotificationChannel
	var skipped []string

	for _, svc := range manifest.Services {
		if svc.App != "vigil-gateway" || svc.Type != "service" {
			continue
		}
		for _, ch := range existing {
			if ch.Provider == "webhook" && ch.Name == "vigil" {
				skipped = append(skipped, "vigil webhook already exists")
				goto done
			}
		}
		{
			var baseURL string
			for _, inst := range svc.Instances {
				if inst.Address != "" {
					if inst.Port > 0 {
						baseURL = fmt.Sprintf("http://%s:%d", inst.Address, inst.Port)
					} else {
						baseURL = "http://" + inst.Address
					}
					break
				}
			}
			if baseURL == "" {
				writeError(w, http.StatusUnprocessableEntity, "vigil-gateway has no reachable instance")
				return
			}
			ch := model.NotificationChannel{
				ID:         "nch_" + uuid.NewString(),
				Provider:   "webhook",
				Name:       "vigil",
				URL:        baseURL + "/api/events",
				Severities: []string{"warning", "critical"},
				CreatedAt:  time.Now().UTC(),
			}
			if err := h.db.InsertNotificationChannel(r.Context(), &ch); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			created = append(created, ch)
		}
	}
done:
	writeJSON(w, map[string]interface{}{
		"created": created,
		"skipped": skipped,
	})
}

func (h *Handler) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.db.DeleteNotificationChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (h *Handler) TestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.beacon == nil {
		writeError(w, http.StatusServiceUnavailable, "beacon not initialized")
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	ch, err := h.db.GetNotificationChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	notifier := beacon.NewNotifier(h.db)
	testEvent := model.BeaconEvent{
		ID:         "evt_test_" + uuid.NewString(),
		Source:     "norn",
		App:        "norn",
		Type:       "notification.test",
		Severity:   model.BeaconInfo,
		Title:      "Norn notification test",
		Body:       "This is a test notification from Norn.",
		OccurredAt: time.Now().UTC(),
	}

	if err := notifier.SendToChannel(r.Context(), *ch, testEvent); err != nil {
		writeError(w, http.StatusBadGateway, "delivery failed: "+err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "sent"})
}
