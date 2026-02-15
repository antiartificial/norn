package handler

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	if h.s3 != nil {
		if err := h.s3.Healthy(r.Context()); err != nil {
			services["s3"] = "down"
		} else {
			services["s3"] = "up"
		}
	}

	// SOPS check: binary on PATH + age key file
	if _, err := exec.LookPath("sops"); err != nil {
		services["sops"] = "down"
	} else {
		home, _ := os.UserHomeDir()
		keyFile := filepath.Join(home, ".config", "sops", "age", "keys.txt")
		if _, err := os.Stat(keyFile); err != nil {
			services["sops"] = "down"
		} else {
			services["sops"] = "up"
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
