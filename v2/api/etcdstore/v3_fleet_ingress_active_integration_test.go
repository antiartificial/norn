package etcdstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/store"
)

func TestV3CompletedFleetAttemptSelectsActiveIngressInventoryEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	ctx := context.Background()
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	accepted, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "ingress-attempt", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	attempt := *accepted.FleetRunnerAttempt
	snapshot := json.RawMessage(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"10.43.0.21"},{"name":"ingress-02","privateIP":"10.43.0.22"}],"nodesFileSHA256":"` + strings.Repeat("c", 64) + `","schemaVersion":"norn.fleet-ingress-inventory/v1"}`)
	canonical, err := fleet.CanonicalIngressInventory(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	phases := []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}
	for i, phase := range phases[:len(phases)-1] {
		if attempt.CurrentPhase != phase {
			t.Fatalf("attempt phase=%q, want %q", attempt.CurrentPhase, phase)
		}
		request := fleetReconciliationAcceptance(t, adapter, plan, attempt, "ingress-"+phase, phase)
		var checkpoint fleet.ReconciliationRequest
		encoded, err := json.Marshal(request.Operation.Payload)
		if err != nil || json.Unmarshal(encoded, &checkpoint) != nil {
			t.Fatal("decode checkpoint", err)
		}
		checkpoint.StateSerial = 7
		if phase == "nodes_configured" {
			checkpoint.IngressInventory = snapshot
			checkpoint.IngressInventoryDigest = digest
		}
		encoded, err = json.Marshal(checkpoint)
		if err != nil || json.Unmarshal(encoded, &request.Operation.Payload) != nil {
			t.Fatal("encode checkpoint", err)
		}
		request.Semantics["request"] = checkpoint
		request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, request); err != nil {
			t.Fatalf("accept %s checkpoint: %v", phase, err)
		}
		attemptResult, err := adapter.UpdateFleetRunnerAttempt(ctx, plan.ID, attempt.ID, attempt.Revision, "advance", phases[min(i+1, len(phases)-1)])
		if err != nil {
			t.Fatalf("advance %s: %v", phase, err)
		}
		attempt = *attemptResult
	}
	if attempt.Status != "succeeded" {
		t.Fatalf("attempt status=%q", attempt.Status)
	}
	proof, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082)
	if err != nil || proof.PlanID != plan.ID || proof.AttemptID != attempt.ID || proof.Digest != digest || proof.ActivePointerRevision <= 0 {
		t.Fatalf("active inventory=%+v err=%v", proof, err)
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "wrong-cluster", "staging/nyc3", 18082); err == nil {
		t.Fatal("unrelated cluster selected an active Fleet inventory")
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "wrong-environment", 18082); err == nil {
		t.Fatal("unrelated environment selected an active Fleet inventory")
	}
	checkpointKey := prefix + "/v3/fleet-reconciliations/" + plan.ID + "/" + proof.CheckpointID
	if _, err := client.Put(ctx, checkpointKey, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082); err == nil {
		t.Fatal("changed inventory checkpoint was accepted after pointer publication")
	}
}
