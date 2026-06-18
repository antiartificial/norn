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
				EventTypes:  []string{"service.health.critical", "instance.failed"},
				Description: "A service became critical or an instance failed.",
				Runbook:     "/v2/operations/troubleshooting",
			},
			{
				ID:          "service-degraded",
				Name:        "Service degraded",
				Severity:    "warning",
				EventTypes:  []string{"service.health.warning", "instance.unhealthy"},
				Description: "A service or instance is unhealthy but not fully down.",
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
				ID:          "cron-missed-run",
				Name:        "Cron missed run",
				Severity:    "critical",
				EventTypes:  []string{"cron.missed_run"},
				Description: "A scheduled process did not run when expected — the cron scheduler may be stuck or the job may be paused.",
				Runbook:     "/v2/operations/cron",
			},
			{
				ID:          "service-recovered",
				Name:        "Service recovered",
				Severity:    "info",
				EventTypes:  []string{"service.health.recovered"},
				Description: "A previously degraded service returned to passing health.",
			},
			{
				ID:          "task-oom-killed",
				Name:        "Task OOM killed",
				Severity:    "critical",
				EventTypes:  []string{"instance.oom_killed"},
				Description: "A task was killed by the OOM killer — review resource limits.",
				Runbook:     "/v2/operations/troubleshooting",
			},
			{
				ID:          "task-restarted",
				Name:        "Task restarted",
				Severity:    "warning",
				EventTypes:  []string{"instance.restarted"},
				Description: "A task restarted — may indicate a crash, resource pressure, or configuration error.",
				Runbook:     "/v2/operations/troubleshooting",
			},
		},
	})
}
