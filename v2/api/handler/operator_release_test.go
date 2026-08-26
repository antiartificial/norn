package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func TestIncidentSnoozeUntilParsesDurationAndUntil(t *testing.T) {
	before := time.Now().UTC()
	until, err := incidentSnoozeUntil("30m", "")
	if err != nil {
		t.Fatal(err)
	}
	if until.Before(before.Add(29*time.Minute)) || until.After(before.Add(31*time.Minute)) {
		t.Fatalf("duration until = %s, want about 30m from now", until)
	}

	explicit := "2026-06-18T01:23:45Z"
	until, err = incidentSnoozeUntil("", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got := until.Format(time.RFC3339); got != explicit {
		t.Fatalf("explicit until = %s, want %s", got, explicit)
	}

	if _, err := incidentSnoozeUntil("0s", ""); err == nil {
		t.Fatal("zero duration should fail")
	}
}

func TestOperatorRiskSeverityMapping(t *testing.T) {
	cases := map[string]string{
		"blocked":                "critical",
		"parent_unavailable":     "critical",
		"missing":                "critical",
		"retention_over_limit":   "critical",
		"oom_killed":             "critical",
		"failed":                 "critical",
		"lost":                   "critical",
		"hung":                   "critical",
		"paused":                 "warning",
		"restart_pressure":       "warning",
		"run_health_unavailable": "warning",
		"unknown":                "warning",
		"ok":                     "info",
		"":                       "info",
	}
	for risk, want := range cases {
		if got := severityForRisk(risk); got != want {
			t.Fatalf("severityForRisk(%q) = %q, want %q", risk, got, want)
		}
	}
}

func TestAssessOperatorCronRunSurfacesOOMBeforeHungRuntime(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 20, 0, 0, time.FixedZone("CDT", -5*60*60))
	run := nomad.CronRun{
		JobID:     "field-harbor-sync/periodic-1",
		Status:    "running",
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
	}
	health := &nomad.CronRunHealth{
		JobID:              run.JobID,
		RunningAllocations: 1,
		FailedAllocations:  1,
		Restarts:           2,
		OOMKilled:          true,
		LastEvent:          `Exit Message: "OOM Killed"`,
	}

	risk, evidence := assessOperatorCronRun(now, run, health)
	if risk != "oom_killed" {
		t.Fatalf("risk = %q, want oom_killed", risk)
	}
	joined := strings.Join(evidence, "; ")
	for _, want := range []string{"OOM kill detected", "failed allocations=1", "task restarts=2", "threshold=30m0s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence = %q, want %q", joined, want)
		}
	}
}

func TestAssessOperatorCronRunFlagsRestartPressureAndHungRuns(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 20, 0, 0, time.UTC)
	restarting := nomad.CronRun{JobID: "restart", Status: "running", StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)}
	if risk, _ := assessOperatorCronRun(now, restarting, &nomad.CronRunHealth{Restarts: 1}); risk != "restart_pressure" {
		t.Fatalf("restart risk = %q, want restart_pressure", risk)
	}
	hung := nomad.CronRun{JobID: "hung", Status: "running", StartedAt: now.Add(-31 * time.Minute).Format(time.RFC3339)}
	if risk, _ := assessOperatorCronRun(now, hung, &nomad.CronRunHealth{}); risk != "hung" {
		t.Fatalf("hung risk = %q, want hung", risk)
	}
}

func TestOperatorCronRiskRunsPrefersActiveChildren(t *testing.T) {
	runs := []nomad.CronRun{
		{JobID: "new-complete", Status: "dead", StartedAt: "2026-08-22T10:00:00Z"},
		{JobID: "active", Status: "running", StartedAt: "2026-08-22T09:00:00Z"},
	}
	selected := operatorCronRiskRuns(runs)
	if len(selected) != 1 || selected[0].JobID != "active" {
		t.Fatalf("selected = %#v, want active child", selected)
	}
}

func TestFormatOperatorLocalTime(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	utc := time.Date(2026, 6, 18, 1, 10, 0, 0, time.UTC)
	got := formatOperatorLocalTime(utc, chicago)
	want := "Wed Jun 17, 2026 8:10 PM CDT"
	if got != want {
		t.Fatalf("formatOperatorLocalTime() = %q, want %q", got, want)
	}
}

func TestOperatorDeployConfidenceEncodesEmptyRecentAsArray(t *testing.T) {
	recent := []model.Deployment{}
	payload, err := json.Marshal(operatorDeployConfidenceApp{
		App:          "sync-in",
		Confidence:   "unknown",
		Recent:       recent,
		PreflightURL: "/api/apps/sync-in/preflight",
		DeployURL:    "/api/apps/sync-in/deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"recent":[]`) {
		t.Fatalf("operator deploy confidence payload = %s, want empty recent array", payload)
	}
}
