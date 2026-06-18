package handler

import (
	"testing"
	"time"
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
		"blocked":              "critical",
		"parent_unavailable":   "critical",
		"missing":              "critical",
		"retention_over_limit": "critical",
		"paused":               "warning",
		"unknown":              "warning",
		"ok":                   "info",
		"":                     "info",
	}
	for risk, want := range cases {
		if got := severityForRisk(risk); got != want {
			t.Fatalf("severityForRisk(%q) = %q, want %q", risk, got, want)
		}
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
