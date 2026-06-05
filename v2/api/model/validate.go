package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type ValidationResult struct {
	App      string              `json:"app"`
	Valid    bool                `json:"valid"`
	Findings []ValidationFinding `json:"findings"`
}

type ValidationFinding struct {
	Severity string `json:"severity"` // error, warning, info
	Field    string `json:"field"`
	Message  string `json:"message"`
}

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func ValidateSpec(spec *InfraSpec) *ValidationResult {
	r := &ValidationResult{App: spec.App, Valid: true}
	declaredSecrets := map[string]bool{}
	for _, key := range spec.Secrets {
		declaredSecrets[strings.ToUpper(strings.TrimSpace(key))] = true
	}

	// App name
	if spec.App == "" {
		r.add("error", "name", "app name is required")
	} else if !appNameRe.MatchString(spec.App) {
		r.add("error", "name", "app name must match ^[a-z0-9][a-z0-9-]*$")
	}

	// Processes
	if len(spec.Processes) == 0 {
		r.add("error", "processes", "at least one process is required")
	}

	for name, proc := range spec.Processes {
		field := fmt.Sprintf("processes.%s", name)

		// Port without health check
		if proc.Port > 0 && proc.Health == nil {
			r.add("warning", field+".health", "port defined without health check")
		}

		// Resource bounds
		if proc.Resources != nil {
			if proc.Resources.CPU > 0 && (proc.Resources.CPU < 10 || proc.Resources.CPU > 10000) {
				r.add("warning", field+".resources.cpu", fmt.Sprintf("cpu %d outside recommended range (10-10000 MHz)", proc.Resources.CPU))
			}
			if proc.Resources.Memory > 0 && (proc.Resources.Memory < 32 || proc.Resources.Memory > 8192) {
				r.add("warning", field+".resources.memory", fmt.Sprintf("memory %d outside recommended range (32-8192 MB)", proc.Resources.Memory))
			}
		}

		// Schedule format (5-6 fields)
		if proc.Schedule != "" {
			fields := strings.Fields(proc.Schedule)
			if len(fields) < 5 || len(fields) > 6 {
				r.add("error", field+".schedule", fmt.Sprintf("cron expression should have 5-6 fields, got %d", len(fields)))
			}
		}

		validateEnvSecrets(r, field+".env", proc.Env, declaredSecrets)
	}

	validateEnvSecrets(r, "env", spec.Env, declaredSecrets)

	// Build requires dockerfile
	if spec.Build != nil && spec.Build.Dockerfile == "" {
		r.add("warning", "build.dockerfile", "build block present without dockerfile")
	}

	// Repo requires URL
	if spec.Repo != nil && spec.Repo.URL == "" {
		r.add("error", "repo.url", "repo block present without URL")
	}

	// Endpoint URLs valid
	for i, ep := range spec.Endpoints {
		if ep.URL == "" {
			r.add("error", fmt.Sprintf("endpoints[%d].url", i), "endpoint URL is required")
			continue
		}
		if _, err := url.Parse(ep.URL); err != nil {
			r.add("error", fmt.Sprintf("endpoints[%d].url", i), fmt.Sprintf("invalid URL: %v", err))
		}
	}

	// Volumes
	for i, vol := range spec.Volumes {
		field := fmt.Sprintf("volumes[%d]", i)
		if vol.Name == "" {
			r.add("error", field+".name", "volume name is required")
		}
		if vol.Mount == "" {
			r.add("error", field+".mount", "volume mount path is required")
		} else if !strings.HasPrefix(vol.Mount, "/") {
			r.add("error", field+".mount", "volume mount path must be absolute")
		}
	}

	// Postgres infra requires database name
	if spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil {
		if spec.Infrastructure.Postgres.Database == "" {
			r.add("error", "infrastructure.postgres.database", "postgres database name is required")
		}
	}

	return r
}

func validateEnvSecrets(r *ValidationResult, field string, env map[string]string, declaredSecrets map[string]bool) {
	for key, value := range env {
		if !looksSecretLike(key, value) {
			continue
		}
		secretKey := strings.ToUpper(strings.TrimSpace(key))
		if declaredSecrets[secretKey] {
			r.add("warning", field+"."+key, "secret-like value is declared in secrets but also appears in plain env; remove the plaintext env entry")
			continue
		}
		r.add("warning", field+"."+key, "secret-like value should move to secrets.enc.yaml and be listed in secrets")
	}
}

func looksSecretLike(key, value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	secretMarkers := []string{
		"API_KEY",
		"AUTH_TOKEN",
		"CLIENT_SECRET",
		"CREDENTIAL",
		"DATABASE_URL",
		"DB_PASSWORD",
		"DSN",
		"PASSWORD",
		"PRIVATE_KEY",
		"SECRET",
		"TOKEN",
	}
	for _, marker := range secretMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	lowerValue := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lowerValue, "postgres://") ||
		strings.HasPrefix(lowerValue, "mysql://") ||
		strings.HasPrefix(lowerValue, "mongodb://") ||
		strings.HasPrefix(lowerValue, "redis://")
}

func (r *ValidationResult) add(severity, field, message string) {
	if severity == "error" {
		r.Valid = false
	}
	r.Findings = append(r.Findings, ValidationFinding{
		Severity: severity,
		Field:    field,
		Message:  message,
	})
}
