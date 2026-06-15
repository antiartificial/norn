package nomad

import (
	"testing"

	"norn/v2/api/model"
)

func TestTranslatePeriodicUsesAppTimezone(t *testing.T) {
	spec := &model.InfraSpec{
		App: "field-harbor",
		Env: map[string]string{"TZ": "America/Chicago"},
	}
	proc := model.Process{
		Schedule: "10 8 * * *",
		Command:  "./scripts/sync-and-ingest.sh",
	}

	job := TranslatePeriodic(spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
	if job.Periodic == nil || job.Periodic.TimeZone == nil {
		t.Fatal("periodic timezone was not set")
	}
	if got := *job.Periodic.TimeZone; got != "America/Chicago" {
		t.Fatalf("timezone = %q, want America/Chicago", got)
	}
}

func TestTranslatePeriodicProcessTimezoneOverridesAppTimezone(t *testing.T) {
	spec := &model.InfraSpec{
		App: "field-harbor",
		Env: map[string]string{"TZ": "America/Chicago"},
	}
	proc := model.Process{
		Schedule: "10 8 * * *",
		Timezone: "UTC",
		Command:  "./scripts/sync-and-ingest.sh",
	}

	job := TranslatePeriodic(spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
	if job.Periodic == nil || job.Periodic.TimeZone == nil {
		t.Fatal("periodic timezone was not set")
	}
	if got := *job.Periodic.TimeZone; got != "UTC" {
		t.Fatalf("timezone = %q, want UTC", got)
	}
}
