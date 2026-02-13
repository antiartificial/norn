package validate

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"norn/api/k8s"
	"norn/api/model"
	"norn/api/secrets"
)

var validAppName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var validRoles = map[string]bool{
	"webserver": true,
	"worker":    true,
	"cron":      true,
	"function":  true,
}

type Validator struct {
	Secrets *secrets.Manager
	Kube    *k8s.Client
}

func (v *Validator) Validate(ctx context.Context, spec *model.InfraSpec) *model.ValidationResult {
	result := &model.ValidationResult{App: spec.App}
	checkStructure(spec, result)
	v.checkSecrets(spec, result)
	v.checkK8sState(ctx, spec, result)
	return result
}

func checkStructure(spec *model.InfraSpec, r *model.ValidationResult) {
	// App name
	if spec.App == "" {
		r.Add(model.ValidationFinding{
			Check:    "spec.app.required",
			Severity: model.SeverityError,
			Message:  "app name is required",
			Field:    "app",
		})
	} else if !validAppName.MatchString(spec.App) {
		r.Add(model.ValidationFinding{
			Check:    "spec.app.format",
			Severity: model.SeverityError,
			Message:  fmt.Sprintf("app name %q must match [a-z0-9][a-z0-9-]*", spec.App),
			Field:    "app",
		})
	}

	// Role
	if spec.Role == "" {
		r.Add(model.ValidationFinding{
			Check:    "spec.role.required",
			Severity: model.SeverityError,
			Message:  "role is required",
			Field:    "role",
		})
	} else if !validRoles[spec.Role] {
		r.Add(model.ValidationFinding{
			Check:    "spec.role.invalid",
			Severity: model.SeverityError,
			Message:  fmt.Sprintf("role %q is not valid (must be webserver, worker, cron, or function)", spec.Role),
			Field:    "role",
		})
	}

	// Webserver checks
	if spec.Role == "webserver" {
		if spec.Port == 0 {
			r.Add(model.ValidationFinding{
				Check:    "webserver.port.required",
				Severity: model.SeverityError,
				Message:  "webserver requires a port",
				Field:    "port",
			})
		}
		if spec.Healthcheck == "" {
			r.Add(model.ValidationFinding{
				Check:    "webserver.healthcheck.recommended",
				Severity: model.SeverityWarning,
				Message:  "webserver should define a healthcheck path",
				Field:    "healthcheck",
			})
		}
	}

	// Cron checks
	if spec.Role == "cron" {
		if spec.Schedule == "" {
			r.Add(model.ValidationFinding{
				Check:    "cron.schedule.required",
				Severity: model.SeverityError,
				Message:  "cron requires a schedule expression",
				Field:    "schedule",
			})
		}
		if spec.Command == "" {
			r.Add(model.ValidationFinding{
				Check:    "cron.command.required",
				Severity: model.SeverityError,
				Message:  "cron requires a command",
				Field:    "command",
			})
		}
	}

	// Function checks
	if spec.Role == "function" {
		if spec.Function == nil || spec.Function.Trigger == "" {
			r.Add(model.ValidationFinding{
				Check:    "function.trigger.recommended",
				Severity: model.SeverityWarning,
				Message:  "function should define a trigger type",
				Field:    "function.trigger",
			})
		}
	}

	// Build
	if spec.Build != nil && spec.Build.Dockerfile == "" {
		r.Add(model.ValidationFinding{
			Check:    "build.dockerfile.required",
			Severity: model.SeverityError,
			Message:  "build section requires a dockerfile path",
			Field:    "build.dockerfile",
		})
	}

	// Repo
	if spec.Repo != nil && spec.Repo.URL == "" {
		r.Add(model.ValidationFinding{
			Check:    "repo.url.required",
			Severity: model.SeverityError,
			Message:  "repo section requires a url",
			Field:    "repo.url",
		})
	}

	// Migrations
	if spec.Migrations != nil {
		if spec.Migrations.Command == "" {
			r.Add(model.ValidationFinding{
				Check:    "migrations.command.required",
				Severity: model.SeverityError,
				Message:  "migrations section requires a command",
				Field:    "migrations.command",
			})
		}
		if spec.Migrations.Database == "" {
			r.Add(model.ValidationFinding{
				Check:    "migrations.database.required",
				Severity: model.SeverityError,
				Message:  "migrations section requires a database",
				Field:    "migrations.database",
			})
		}
	}

	// Services
	if spec.Services != nil && spec.Services.Postgres != nil {
		if spec.Services.Postgres.Database == "" {
			r.Add(model.ValidationFinding{
				Check:    "services.postgres.database.required",
				Severity: model.SeverityError,
				Message:  "postgres service requires a database name",
				Field:    "services.postgres.database",
			})
		}
	}

	// Volumes
	for i, vol := range spec.Volumes {
		if vol.Name == "" {
			r.Add(model.ValidationFinding{
				Check:    "volumes.name.required",
				Severity: model.SeverityError,
				Message:  fmt.Sprintf("volume[%d] requires a name", i),
				Field:    "volumes.name",
			})
		}
		if vol.MountPath == "" {
			r.Add(model.ValidationFinding{
				Check:    "volumes.mountPath.required",
				Severity: model.SeverityError,
				Message:  fmt.Sprintf("volume[%d] requires a mountPath", i),
				Field:    "volumes.mountPath",
			})
		}
	}

	// Alerts
	if spec.Alerts != nil {
		if spec.Alerts.Window != "" {
			if _, err := time.ParseDuration(spec.Alerts.Window); err != nil {
				r.Add(model.ValidationFinding{
					Check:    "alerts.window.invalid",
					Severity: model.SeverityError,
					Message:  fmt.Sprintf("alert window %q is not a valid duration", spec.Alerts.Window),
					Field:    "alerts.window",
				})
			}
		}
		if spec.Alerts.Threshold <= 0 {
			r.Add(model.ValidationFinding{
				Check:    "alerts.threshold.positive",
				Severity: model.SeverityError,
				Message:  "alert threshold must be positive",
				Field:    "alerts.threshold",
			})
		}
	}
}

func (v *Validator) checkSecrets(spec *model.InfraSpec, r *model.ValidationResult) {
	if len(spec.Secrets) == 0 || v.Secrets == nil {
		return
	}

	keys, err := v.Secrets.List(spec.App)
	if err != nil {
		if os.IsNotExist(err) {
			r.Add(model.ValidationFinding{
				Check:    "secrets.file.missing",
				Severity: model.SeverityError,
				Message:  "secrets.enc.yaml not found",
			})
			return
		}
		r.Add(model.ValidationFinding{
			Check:    "secrets.decrypt.error",
			Severity: model.SeverityWarning,
			Message:  fmt.Sprintf("could not decrypt secrets: %v", err),
		})
		return
	}

	encKeySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		encKeySet[k] = true
	}

	// Declared in spec but missing from encrypted file
	for _, declared := range spec.Secrets {
		if !encKeySet[declared] {
			r.Add(model.ValidationFinding{
				Check:    "secrets.key.missing",
				Severity: model.SeverityError,
				Message:  fmt.Sprintf("secret %q declared in spec but missing from secrets.enc.yaml", declared),
			})
		}
	}

	// In encrypted file but not declared in spec
	declaredSet := make(map[string]bool, len(spec.Secrets))
	for _, s := range spec.Secrets {
		declaredSet[s] = true
	}
	for _, k := range keys {
		if !declaredSet[k] {
			r.Add(model.ValidationFinding{
				Check:    "secrets.key.extra",
				Severity: model.SeverityWarning,
				Message:  fmt.Sprintf("secret %q in secrets.enc.yaml but not declared in spec (won't be injected)", k),
			})
		}
	}
}

func (v *Validator) checkK8sState(ctx context.Context, spec *model.InfraSpec, r *model.ValidationResult) {
	if v.Kube == nil {
		return
	}

	namespace := "default"

	// Check K8s secret exists if spec declares secrets
	if len(spec.Secrets) > 0 {
		secretName := spec.App + "-secrets"
		exists, err := v.Kube.SecretExists(ctx, namespace, secretName)
		if err == nil && !exists {
			r.Add(model.ValidationFinding{
				Check:    "k8s.secret.missing",
				Severity: model.SeverityWarning,
				Message:  fmt.Sprintf("K8s secret %q not found in namespace %q", secretName, namespace),
			})
		}
	}

	// Check deployment exists for webserver/worker
	if spec.Role == "webserver" || spec.Role == "worker" {
		_, err := v.Kube.GetDeployment(ctx, namespace, spec.App)
		if err != nil && k8s.IsNotFound(err) {
			r.Add(model.ValidationFinding{
				Check:    "k8s.deployment.missing",
				Severity: model.SeverityInfo,
				Message:  fmt.Sprintf("deployment %q not found — run forge to create it", spec.App),
			})
		}
	}
}
