package handler

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	services := map[string]string{}

	if err := h.db.Healthy(r.Context()); err != nil {
		services["postgres"] = "down"
	} else {
		services["postgres"] = "up"
	}

	if h.cfg.WorkloadConnector == "nomad-consul" && h.nomad != nil {
		if err := h.nomad.Healthy(); err != nil {
			services["nomad"] = "down"
		} else {
			services["nomad"] = "up"
		}
	}

	if h.cfg.WorkloadConnector == "nomad-consul" && h.consul != nil {
		if err := h.consul.Healthy(); err != nil {
			services["consul"] = "down"
		} else {
			services["consul"] = "up"
		}
	}
	if h.workloads != nil {
		name := "workload-connector/" + h.workloads.Name()
		if err := h.workloads.Healthy(r.Context()); err != nil {
			services[name] = "down"
		} else {
			services[name] = "up"
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
		keyFile := sopsAgeKeyFile()
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
			"mode":              h.cfg.NetworkMode,
			"bindAddr":          h.cfg.BindAddr,
			"nomadAddr":         h.cfg.NomadAddr,
			"consulAddr":        h.cfg.ConsulAddr,
			"workloadConnector": h.cfg.WorkloadConnector,
		},
	})
}

// FleetAuthorityHealth deliberately proves only the management plane's
// database dependency. A Fleet-only control plane must not discover, probe,
// or report an absent workload substrate as degraded.
func (h *Handler) FleetAuthorityHealth(w http.ResponseWriter, r *http.Request) {
	services := map[string]string{"postgres": "down"}
	if h != nil && h.db != nil && h.db.Healthy(r.Context()) == nil {
		services["postgres"] = "up"
	}
	status := "ok"
	if services["postgres"] != "up" {
		status = "degraded"
	}
	writeJSON(w, map[string]interface{}{
		"status": status, "services": services,
		"authority": "fleet-only",
	})
}

func sopsAgeKeyFile() string {
	if configured := strings.TrimSpace(os.Getenv("SOPS_AGE_KEY_FILE")); configured != "" {
		return configured
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "sops", "age", "keys.txt")
}
