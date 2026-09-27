package etcdstore

import (
	"context"
	"testing"
	"time"
)

func TestV3VerifyClaimedDeploymentRequiresSignedAggregateEtcd(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := adapter.ClaimNextOperation(ctx, "deployment-verifier", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	verified, err := adapter.VerifyClaimedDeployment(ctx, *claimed)
	if err != nil || verified.Deployment == nil || verified.Deployment.ID != accepted.Deployment.ID || len(verified.Regions) != 1 {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	forged := *claimed
	forged.Payload = map[string]interface{}{"deploymentId": "different"}
	if _, err := adapter.VerifyClaimedDeployment(ctx, forged); err == nil {
		t.Fatal("forged claimed payload accepted")
	}
	forged = *claimed
	forged.App = "other"
	if _, err := adapter.VerifyClaimedDeployment(ctx, forged); err == nil {
		t.Fatal("forged claimed app accepted")
	}
}
