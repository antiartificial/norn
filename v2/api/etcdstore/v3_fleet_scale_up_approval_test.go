package etcdstore

import "testing"

func TestFleetScaleUpApprovalChangesDispatchPreparationIdentity(t *testing.T) {
	original := FleetGitHubDispatchPreparation{ScaleUpApprovalRunID: 12345}
	same := original
	if !sameFleetGitHubDispatchPreparation(original, same) {
		t.Fatal("same approval changed identity")
	}
	changed := original
	changed.ScaleUpApprovalRunID = 12346
	if sameFleetGitHubDispatchPreparation(original, changed) {
		t.Fatal("different approval reused dispatch preparation")
	}
}
