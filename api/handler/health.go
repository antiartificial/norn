package handler

import (
	"context"
	"net/http"
	"os/exec"
	"time"
)

type ServiceHealth struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // up, down, unknown
	Details string `json:"details,omitempty"`
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	services := []ServiceHealth{
		h.checkPostgres(ctx),
		h.checkKubernetes(ctx),
		h.checkValkey(ctx),
		h.checkRedpanda(ctx),
		h.checkS3(ctx),
		h.checkSOPS(),
	}

	allUp := true
	for _, s := range services {
		if s.Status == "down" {
			allUp = false
		}
	}

	status := "healthy"
	if !allUp {
		status = "degraded"
	}

	writeJSON(w, map[string]interface{}{
		"status":   status,
		"services": services,
	})
}

func (h *Handler) checkPostgres(ctx context.Context) ServiceHealth {
	row := h.db.QueryRow(ctx, "SELECT 1")
	var one int
	if err := row.Scan(&one); err != nil {
		return ServiceHealth{Name: "postgres", Status: "down", Details: err.Error()}
	}
	return ServiceHealth{Name: "postgres", Status: "up"}
}

func (h *Handler) checkKubernetes(ctx context.Context) ServiceHealth {
	if h.kube == nil {
		return ServiceHealth{Name: "kubernetes", Status: "unknown", Details: "not configured"}
	}
	_, err := h.kube.ListDeployments(ctx, "default")
	if err != nil {
		return ServiceHealth{Name: "kubernetes", Status: "down", Details: err.Error()}
	}
	return ServiceHealth{Name: "kubernetes", Status: "up"}
}

func (h *Handler) checkValkey(_ context.Context) ServiceHealth {
	cmd := exec.Command("redis-cli", "-p", "6379", "ping")
	out, err := cmd.Output()
	if err != nil || string(out) != "PONG\n" {
		return ServiceHealth{Name: "valkey", Status: "unknown", Details: "not checked"}
	}
	return ServiceHealth{Name: "valkey", Status: "up"}
}

func (h *Handler) checkRedpanda(_ context.Context) ServiceHealth {
	cmd := exec.Command("curl", "-sf", "http://localhost:9644/v1/status/ready")
	if err := cmd.Run(); err != nil {
		return ServiceHealth{Name: "redpanda", Status: "unknown", Details: "not checked"}
	}
	return ServiceHealth{Name: "redpanda", Status: "up"}
}

func (h *Handler) checkS3(ctx context.Context) ServiceHealth {
	if h.s3Client == nil {
		return ServiceHealth{Name: "s3/minio", Status: "unknown", Details: "not configured"}
	}
	if err := h.s3Client.Healthy(ctx); err != nil {
		return ServiceHealth{Name: "s3/minio", Status: "down", Details: err.Error()}
	}
	return ServiceHealth{Name: "s3/minio", Status: "up"}
}

func (h *Handler) checkSOPS() ServiceHealth {
	cmd := exec.Command("sops", "--version")
	if err := cmd.Run(); err != nil {
		return ServiceHealth{Name: "sops", Status: "down", Details: "sops not installed"}
	}
	return ServiceHealth{Name: "sops", Status: "up"}
}
