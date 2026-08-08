package handler

import (
	"net/http"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type HostStatusResponse struct {
	SchemaVersion   string            `json:"schemaVersion"`
	Status          string            `json:"status"`
	Services        map[string]string `json:"services"`
	LatestAssurance *model.Operation  `json:"latestAssurance,omitempty"`
	ObservedAt      time.Time         `json:"observedAt"`
}

func (h *Handler) HostStatus(w http.ResponseWriter, r *http.Request) {
	out := HostStatusResponse{
		SchemaVersion: "norn.host-status/v1", Status: "ok",
		Services: map[string]string{}, ObservedAt: time.Now().UTC(),
	}
	if h.db == nil || h.db.Healthy(r.Context()) != nil {
		out.Services["postgres"] = "down"
		out.Status = "degraded"
	} else {
		out.Services["postgres"] = "up"
		if operations, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: "host.assure", Limit: 1}); err == nil && len(operations) > 0 {
			operations[0].AttachReceipt()
			out.LatestAssurance = &operations[0]
		}
	}
	if h.nomad != nil {
		if err := h.nomad.Healthy(); err != nil {
			out.Services["nomad"], out.Status = "down", "degraded"
		} else {
			out.Services["nomad"] = "up"
		}
	}
	if h.consul != nil {
		if err := h.consul.Healthy(); err != nil {
			out.Services["consul"], out.Status = "down", "degraded"
		} else {
			out.Services["consul"] = "up"
		}
	}
	writeJSON(w, out)
}
