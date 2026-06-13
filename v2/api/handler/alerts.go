package handler

import "net/http"

type alertRule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Severity    string   `json:"severity"`
	EventTypes  []string `json:"eventTypes"`
	Description string   `json:"description"`
	Runbook     string   `json:"runbook,omitempty"`
}

func (h *Handler) AlertRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"rules": []alertRule{
			{
				ID:          "deploy-failed",
				Name:        "Deploy failed",
				Severity:    "critical",
				EventTypes:  []string{"deploy.failed", "rollback.failed"},
				Description: "A deploy or rollback failed and needs operator review.",
				Runbook:     "/v2/operations/deploying",
			},
			{
				ID:          "service-down",
				Name:        "Service down",
				Severity:    "critical",
				EventTypes:  []string{"service.health.critical", "nomad.allocation.failed", "nomad.allocation.lost"},
				Description: "A service became critical or its allocation failed/lost.",
				Runbook:     "/v2/operations/troubleshooting",
			},
			{
				ID:          "service-degraded",
				Name:        "Service degraded",
				Severity:    "warning",
				EventTypes:  []string{"service.health.warning", "nomad.allocation.unhealthy"},
				Description: "A service or allocation is unhealthy but not fully down.",
				Runbook:     "/v2/operations/troubleshooting",
			},
			{
				ID:          "cron-failed",
				Name:        "Cron failed",
				Severity:    "critical",
				EventTypes:  []string{"cron.failed", "cron.lost", "cron.hung"},
				Description: "A scheduled process failed, was lost, or appears hung.",
				Runbook:     "/v2/operations/cron",
			},
			{
				ID:          "service-recovered",
				Name:        "Service recovered",
				Severity:    "info",
				EventTypes:  []string{"service.health.recovered"},
				Description: "A previously degraded service returned to passing health.",
			},
		},
	})
}
