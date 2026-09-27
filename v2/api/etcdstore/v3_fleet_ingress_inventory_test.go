package etcdstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func inventoryCheckpointOperation(t *testing.T, attempt fleet.RunnerAttempt, phase string, snapshot json.RawMessage, digest string) model.Operation {
	t.Helper()
	request := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, AttemptID: attempt.ID,
		Phase: phase, Status: "succeeded", CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256,
		StateSerial: 7, EvidenceDigest: "sha256:" + strings.Repeat("e", 64), IngressInventory: snapshot, IngressInventoryDigest: digest}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	return model.Operation{ID: phase + "-checkpoint", Kind: "fleet.reconciliation", Ref: attempt.PlanID, Status: model.OperationSucceeded, Payload: payload}
}

func TestResolveFleetIngressInventoryRequiresLatestCompletedAttempt(t *testing.T) {
	planID := "11111111-1111-4111-8111-111111111111"
	attempt := fleet.RunnerAttempt{ID: "22222222-2222-4222-8222-222222222222", PlanID: planID,
		Attempt: 2, Revision: 8, Status: "succeeded", CurrentPhase: "complete", CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64)}
	snapshot := json.RawMessage(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"10.43.0.21"},{"name":"ingress-02","privateIP":"10.43.0.22"}],"nodesFileSHA256":"` + strings.Repeat("c", 64) + `","schemaVersion":"norn.fleet-ingress-inventory/v1"}`)
	canonical, err := fleet.CanonicalIngressInventory(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	configured := inventoryCheckpointOperation(t, attempt, "nodes_configured", snapshot, digest)
	completed := inventoryCheckpointOperation(t, attempt, "readiness_verified", nil, "")
	revisions := map[string]int64{configured.ID: 42, completed.ID: 43}
	evidence, err := resolveFleetIngressInventory(planID, "norn-staging", "staging/nyc3", 18082, []fleet.RunnerAttempt{attempt}, []model.Operation{configured, completed}, revisions)
	if err != nil || evidence.Digest != digest || evidence.AttemptID != attempt.ID || evidence.StateSerial != 7 || evidence.CheckpointModRevision != 42 || len(evidence.Nodes) != 2 || evidence.Nodes[1].APIURL != "https://10.43.0.22:18082" {
		t.Fatalf("resolved inventory = %+v, %v", evidence, err)
	}
	evidence.PlanStateModRevision = 41
	compares, err := (&V3OperationStore{prefix: "/test"}).fleetIngressInventoryCompares(*evidence)
	if err != nil || len(compares) != 2 {
		t.Fatalf("inventory terminal compares = %d, %v", len(compares), err)
	}
	evidence.Cluster, evidence.Environment, evidence.ActivePointerRevision, evidence.ActiveClusterEpochRevision = "norn-staging", "staging/nyc3", 44, 45
	compares, err = (&V3OperationStore{prefix: "/test"}).fleetIngressInventoryCompares(*evidence)
	if err != nil || len(compares) != 4 {
		t.Fatalf("active inventory pointer compare = %d, %v", len(compares), err)
	}
	incomplete := *evidence
	incomplete.PlanStateModRevision = 0
	if _, err := (&V3OperationStore{prefix: "/test"}).fleetIngressInventoryCompares(incomplete); err == nil {
		t.Fatal("inventory without plan state fence was accepted")
	}
	pending := attempt
	pending.ID, pending.Attempt, pending.Status, pending.CurrentPhase = "33333333-3333-4333-8333-333333333333", 3, "running", "nodes_configured"
	for name, attemptsAndHistory := range map[string]struct {
		attempts []fleet.RunnerAttempt
		history  []model.Operation
	}{
		"newer pending attempt":   {[]fleet.RunnerAttempt{pending, attempt}, []model.Operation{configured, completed}},
		"missing terminal proof":  {[]fleet.RunnerAttempt{attempt}, []model.Operation{configured}},
		"duplicate configuration": {[]fleet.RunnerAttempt{attempt}, []model.Operation{configured, configured, completed}},
	} {
		if _, err := resolveFleetIngressInventory(planID, "norn-staging", "staging/nyc3", 18082, attemptsAndHistory.attempts, attemptsAndHistory.history, revisions); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := resolveFleetIngressInventory(planID, "other-cluster", "staging/nyc3", 18082, []fleet.RunnerAttempt{attempt}, []model.Operation{configured, completed}, revisions); err == nil {
		t.Fatal("wrong Fleet cluster accepted")
	}
	changedSerial := completed
	changedSerial.ID = "other-completion"
	changedSerial.Payload = map[string]interface{}{}
	for key, value := range completed.Payload {
		changedSerial.Payload[key] = value
	}
	changedSerial.Payload["stateSerial"] = float64(8)
	if _, err := resolveFleetIngressInventory(planID, "norn-staging", "staging/nyc3", 18082, []fleet.RunnerAttempt{attempt}, []model.Operation{configured, changedSerial}, revisions); err == nil {
		t.Fatal("changed Fleet state serial accepted")
	}
}
