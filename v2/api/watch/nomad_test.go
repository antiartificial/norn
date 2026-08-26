package watch

import (
	"testing"
	"time"

	"norn/v2/api/nomad"
)

func TestCronCorrelationKey(t *testing.T) {
	got := cronCorrelationKey("field-harbor", "field-harbor-sync-pm")
	want := "field-harbor:field-harbor-sync-pm:cron"
	if got != want {
		t.Fatalf("cronCorrelationKey() = %q, want %q", got, want)
	}
}

func TestFormatCronLocalTime(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	utc := time.Date(2026, 6, 17, 1, 10, 0, 0, time.UTC)
	got := formatCronLocalTime(utc, chicago)
	want := "Tue Jun 16, 2026 8:10 PM CDT"
	if got != want {
		t.Fatalf("formatCronLocalTime() = %q, want %q", got, want)
	}
	if got := formatCronLocalTime(time.Time{}, chicago); got != "never" {
		t.Fatalf("formatCronLocalTime(zero) = %q, want never", got)
	}
}

func TestCronHasPrunedChildHistory(t *testing.T) {
	if cronHasPrunedChildHistory(nil, nil) {
		t.Fatal("nil info should not look like pruned history")
	}
	info := &nomad.PeriodicJobInfo{ChildrenDead: 2}
	if !cronHasPrunedChildHistory(info, nil) {
		t.Fatal("dead child summary without child runs should look like pruned history")
	}
	runs := []nomad.CronRun{{JobID: "field-harbor-sync/periodic-1"}}
	if cronHasPrunedChildHistory(info, runs) {
		t.Fatal("detailed child runs should not look like pruned history")
	}
}
