package store

import (
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

func TestV3ExecutorInitialPhasePreservesSharedReconciliationOrder(t *testing.T) {
	const commit = "0123456789012345678901234567890123456789"
	planSHA := "sha256:" + strings.Repeat("a", 64)
	plan := &model.Operation{Payload: map[string]interface{}{
		"action": "scale", "current": map[string]interface{}{"desired": float64(1)}, "proposed": map[string]interface{}{"desired": float64(2)},
	}}
	request := fleet.ReconciliationRequest{Phase: lifecycle.ExecutorInitialPhase(lifecycle.V3), Status: "succeeded", CommitSHA: commit, PlanSHA256: planSHA, AttemptID: "attempt"}
	if request.Phase != "prechange_verified" {
		t.Fatalf("initial phase=%q", request.Phase)
	}
	if err := ValidateFleetReconciliationAdmissionTransition(plan, nil, request); err != nil {
		t.Fatalf("initial prechange checkpoint rejected: %v", err)
	}

	request.Phase = "provider_applying"
	if err := ValidateFleetReconciliationAdmissionTransition(plan, nil, request); err == nil {
		t.Fatal("provider checkpoint admitted without successful prechange evidence")
	}
	existing := []model.Operation{{Status: model.OperationSucceeded, Payload: map[string]interface{}{
		"phase": "prechange_verified", "commitSha": commit, "planSha256": planSHA,
	}}}
	if err := ValidateFleetReconciliationAdmissionTransition(plan, existing, request); err != nil {
		t.Fatalf("provider checkpoint rejected after prechange evidence: %v", err)
	}
}
