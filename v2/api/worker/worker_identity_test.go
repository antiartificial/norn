package worker

import "testing"

func TestWorkerInstancesHaveDistinctClaimOwners(t *testing.T) {
	first := NewOperationWorkerForKinds(nil, nil, []string{"app.cron-resume"})
	second := NewOperationWorkerForKinds(nil, nil, []string{"app.cron-resume"})
	if first.id == "" || second.id == "" || first.id == second.id {
		t.Fatalf("operation worker claim owners are not distinct: %q, %q", first.id, second.id)
	}
	maintenanceFirst := NewMaintenanceWorker(nil, nil, nil)
	maintenanceSecond := NewMaintenanceWorker(nil, nil, nil)
	if maintenanceFirst.id == "" || maintenanceSecond.id == "" || maintenanceFirst.id == maintenanceSecond.id {
		t.Fatalf("maintenance worker claim owners are not distinct: %q, %q", maintenanceFirst.id, maintenanceSecond.id)
	}
}
