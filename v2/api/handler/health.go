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

	if h.engine != nil {
		if err := h.engine.Healthy(); err != nil {
			services["engine"] = "down"
		} else {
			services["engine"] = "up"
		}
	}

	if h.s3 != nil {
		if err := h.s3.Healthy(r.Context()); err != nil {
			services["s3"] = "down"
		} else {
			services["s3"] = "up"
		}
	}

	if h.redpanda != nil {
		if err := h.redpanda.Healthy(r.Context()); err != nil {
			services["redpanda"] = "down"
		} else {
			services["redpanda"] = "up"
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
		"network": map[string]string{
			"mode":     h.cfg.NetworkMode,
			"bindAddr": h.cfg.BindAddr,
		},
	})
}
