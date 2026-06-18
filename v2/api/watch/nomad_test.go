package watch

import (
	"testing"

	"norn/v2/api/nomad"
)

func TestCronCorrelationKey(t *testing.T) {
	got := cronCorrelationKey("field-harbor", "field-harbor-sync-pm")
	want := "field-harbor:field-harbor-sync-pm:cron"
	if got != want {
		t.Fatalf("cronCorrelationKey() = %q, want %q", got, want)
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
