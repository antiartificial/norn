package handler

import (
	"net/http"
	"strconv"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	deliveries, err := h.db.ListWebhookDeliveries(r.Context(), store.WebhookFilter{
		Provider: r.URL.Query().Get("provider"),
		Status:   r.URL.Query().Get("status"),
		App:      r.URL.Query().Get("app"),
		Limit:    limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deliveries == nil {
		deliveries = []model.WebhookDelivery{}
	}
	writeJSON(w, map[string]interface{}{
		"deliveries": deliveries,
		"count":      len(deliveries),
	})
}
