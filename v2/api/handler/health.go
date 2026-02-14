package handler

import (
	"net/http"
)

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	services := map[string]string{}

	if err := h.db.Healthy(r.Context()); err != nil {
		services["postgres"] = "down"
	} else {
		services["postgres"] = "up"
	}

	if h.nomad != nil {
		if err := h.nomad.Healthy(); err != nil {
			services["nomad"] = "down"
		} else {
			services["nomad"] = "up"
		}
	}

	if h.consul != nil {
		if err := h.consul.Healthy(); err != nil {
			services["consul"] = "down"
		} else {
			services["consul"] = "up"
		}
	}

	status := "ok"
	for _, v := range services {
		if v == "down" {
			status = "degraded"
			break
		}
	}

	writeJSON(w, map[string]interface{}{
		"status":   status,
		"services": services,
	})
}
