package validate

import (
	"context"
	"testing"

	"norn/api/model"
)

func minimalSpec() *model.InfraSpec {
	return &model.InfraSpec{
		App:    "test-app",
		Role:   "webserver",
		Port:   8080,
		Deploy: true,
		Healthcheck: "/health",
		Alerts: &model.AlertConfig{Window: "5m", Threshold: 3},
	}
}

func TestMinimalValidSpec(t *testing.T) {
	v := &Validator{}
	r := v.Validate(context.Background(), minimalSpec())
	if !r.Valid() {
		t.Errorf("expected valid, got %d errors: %+v", r.Errors, r.Findings)
	}
}

func TestMissingApp(t *testing.T) {
	spec := minimalSpec()
	spec.App = ""
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "spec.app.required")
}

func TestInvalidAppFormat(t *testing.T) {
	spec := minimalSpec()
	spec.App = "Invalid_Name"
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "spec.app.format")
}

func TestMissingRole(t *testing.T) {
	spec := minimalSpec()
	spec.Role = ""
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "spec.role.required")
}

func TestInvalidRole(t *testing.T) {
	spec := minimalSpec()
	spec.Role = "daemon"
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "spec.role.invalid")
}

func TestWebserverWithoutPort(t *testing.T) {
	spec := minimalSpec()
	spec.Port = 0
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "webserver.port.required")
}

func TestWebserverWithoutHealthcheck(t *testing.T) {
	spec := minimalSpec()
	spec.Healthcheck = ""
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "webserver.healthcheck.recommended")
	// Should be a warning, not an error
	for _, f := range r.Findings {
		if f.Check == "webserver.healthcheck.recommended" && f.Severity != model.SeverityWarning {
			t.Errorf("expected warning severity, got %s", f.Severity)
		}
	}
}

func TestCronWithoutSchedule(t *testing.T) {
	spec := &model.InfraSpec{
		App:    "cron-job",
		Role:   "cron",
		Deploy: true,
		Alerts: &model.AlertConfig{Window: "5m", Threshold: 3},
	}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "cron.schedule.required")
	assertHasCheck(t, r, "cron.command.required")
}

func TestBuildWithoutDockerfile(t *testing.T) {
	spec := minimalSpec()
	spec.Build = &model.BuildSpec{}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "build.dockerfile.required")
}

func TestVolumeWithoutNameAndMount(t *testing.T) {
	spec := minimalSpec()
	spec.Volumes = []model.VolumeSpec{{}}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "volumes.name.required")
	assertHasCheck(t, r, "volumes.mountPath.required")
}

func TestRepoWithoutURL(t *testing.T) {
	spec := minimalSpec()
	spec.Repo = &model.RepoSpec{}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "repo.url.required")
}

func TestMigrationsWithoutFields(t *testing.T) {
	spec := minimalSpec()
	spec.Migrations = &model.Migration{}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "migrations.command.required")
	assertHasCheck(t, r, "migrations.database.required")
}

func TestPostgresWithoutDatabase(t *testing.T) {
	spec := minimalSpec()
	spec.Services = &model.ServiceDeps{
		Postgres: &model.PostgresDep{},
	}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "services.postgres.database.required")
}

func TestInvalidAlertWindow(t *testing.T) {
	spec := minimalSpec()
	spec.Alerts = &model.AlertConfig{Window: "not-a-duration", Threshold: 3}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "alerts.window.invalid")
}

func TestAlertThresholdZero(t *testing.T) {
	spec := minimalSpec()
	spec.Alerts = &model.AlertConfig{Window: "5m", Threshold: 0}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "alerts.threshold.positive")
}

func TestFunctionWithoutTrigger(t *testing.T) {
	spec := &model.InfraSpec{
		App:    "my-func",
		Role:   "function",
		Deploy: true,
		Alerts: &model.AlertConfig{Window: "5m", Threshold: 3},
	}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	assertHasCheck(t, r, "function.trigger.recommended")
}

func TestWorkerPassesWithMinimalFields(t *testing.T) {
	spec := &model.InfraSpec{
		App:    "bg-worker",
		Role:   "worker",
		Deploy: true,
		Alerts: &model.AlertConfig{Window: "5m", Threshold: 3},
	}
	v := &Validator{}
	r := v.Validate(context.Background(), spec)
	if !r.Valid() {
		t.Errorf("expected valid worker, got %d errors: %+v", r.Errors, r.Findings)
	}
}

func assertHasCheck(t *testing.T, r *model.ValidationResult, check string) {
	t.Helper()
	for _, f := range r.Findings {
		if f.Check == check {
			return
		}
	}
	t.Errorf("expected finding with check %q, got: %+v", check, r.Findings)
}
