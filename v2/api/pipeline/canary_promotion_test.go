package pipeline

import (
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

func TestCanaryPromotionRequestBindsLogicalAndNomadRegionsAndDeployment(t *testing.T) {
	op := &model.Operation{App: "widgets", Payload: map[string]interface{}{"region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-123"}}
	request, err := canaryPromotionRequestFromOperation(op)
	if err != nil {
		t.Fatal(err)
	}
	if request.App != "widgets" || request.Region != "us-central" || request.NomadRegion != "global" || request.DeploymentID != "deployment-123" {
		t.Fatalf("request = %#v", request)
	}
	if got, want := canaryPromotionResource(request), "app/widgets/canary-promote/us-central/deployment-123"; got != want {
		t.Fatalf("resource = %q, want %q", got, want)
	}
}

func TestCanaryPromotionRejectsIncompleteAcceptedPayload(t *testing.T) {
	for _, payload := range []map[string]interface{}{
		{"region": "us-central", "nomadRegion": "global"},
		{"region": "us-central", "deploymentId": "deployment-123"},
		{"nomadRegion": "global", "deploymentId": "deployment-123"},
	} {
		if _, err := canaryPromotionRequestFromOperation(&model.Operation{App: "widgets", Payload: payload}); err == nil {
			t.Fatalf("incomplete payload accepted: %#v", payload)
		}
	}
}

func TestCanaryPromotionExecutionIdentityIncludesClaimGeneration(t *testing.T) {
	reservation := effect.Reservation{Authority: "authority", InputDigest: "sha256:input", OperationClaim: effect.OperationClaim{OperationID: "operation", Generation: 1}}
	first := canaryPromotionExecutionID(reservation)
	reservation.OperationClaim.Generation = 2
	if second := canaryPromotionExecutionID(reservation); first == second {
		t.Fatal("successor claim reused predecessor Nomad promotion identity")
	}
}
