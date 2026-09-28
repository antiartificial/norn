package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// seedFleetReleaseIngressInventory advances the same signed plan, dispatch,
// runner and reconciliation contracts used by a completed Fleet apply. Its
// private addresses are synthetic; no provider nodes are provisioned here.
func seedFleetReleaseIngressInventory(t *testing.T, adapter *etcdstore.V3OperationStore, authority string) {
	t.Helper()
	ctx := context.Background()
	addresses := []string{"10.43.0.21", "10.43.0.22"}
	if supplied := os.Getenv("NORN_TEST_INGRESS_PRIVATE_IPS"); supplied != "" {
		addresses = strings.Split(supplied, ",")
		if len(addresses) != 2 || addresses[0] == addresses[1] {
			t.Fatal("route rehearsal requires two distinct private ingress addresses")
		}
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
				t.Fatal("route rehearsal requires private IPv4 ingress addresses")
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := model.Operation{ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: map[string]interface{}{"cluster": "norn-staging", "digest": "route-plan", "action": "scale", "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}}}
	plan.Payload["id"] = plan.ID
	planAcceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: plan.Kind, Resource: plan.Ref, Key: "plan-" + plan.ID}, Operation: plan, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	var err error
	planAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(planAcceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, planAcceptance); err != nil {
		t.Fatal(err)
	}
	github := map[string]interface{}{"planId": plan.ID, "planDigest": "route-plan", "sourceDigest": "", "planRunId": int64(7), "planSha256": strings.Repeat("b", 64), "approvedHeadSha": strings.Repeat("c", 40), "fleetEnvironment": "staging/nyc3", "allowDestructive": false}
	dispatch := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"fleetGitHub": github}, Metadata: map[string]interface{}{}}
	dispatchRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: plan.ID}, Kind: dispatch.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: dispatch, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": github}}
	dispatchRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(dispatchRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, prepared, err := adapter.AcceptFleetGitHubDispatch(ctx, dispatchRequest, etcdstore.FleetGitHubDispatchPreparation{PlanID: plan.ID, PlanRunID: 7, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinishFleetGitHubDispatch(ctx, plan.ID, prepared.DispatchNonceSHA256, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	attemptID := uuid.NewString()
	runner := &store.FleetRunnerAttemptAdmission{PlanID: plan.ID, AttemptID: attemptID, RunnerAttemptID: "github:acme/fleet:7:1", CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/acme/fleet/actions/runs/7", DispatchNonceSHA256: prepared.DispatchNonceSHA256, SourceDispatchRunID: "7", HeartbeatTimeoutSeconds: 120, WorkloadIntent: "apply", WorkloadRunID: "7", WorkloadSHA: strings.Repeat("c", 40)}
	runnerPayload := map[string]interface{}{"attemptId": attemptID, "planId": plan.ID, "runnerAttemptId": runner.RunnerAttemptID, "commitSha": runner.CommitSHA, "planSha256": runner.PlanSHA256, "workflowUrl": runner.WorkflowURL, "dispatchNonceSha256": runner.DispatchNonceSHA256, "sourceDispatchRunId": "7", "resume": false, "heartbeatTimeoutSeconds": 120}
	runnerRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:7"}, Kind: "fleet.runner-attempt", Resource: plan.ID, Key: "route-attempt"}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: plan.ID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: runnerPayload, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.runner-attempt", "planId": plan.ID, "runnerAttemptId": runner.RunnerAttemptID, "commitSha": runner.CommitSHA, "planSha256": runner.PlanSHA256, "workflowUrl": runner.WorkflowURL, "dispatchNonceSha256": runner.DispatchNonceSHA256, "sourceDispatchRunId": "7", "resume": false, "heartbeatTimeoutSeconds": 120, "workload": map[string]interface{}{"intent": "apply", "runId": "7", "sha": runner.WorkloadSHA}}, FleetRunnerAttempt: runner}
	runnerRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(runnerRequest)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRunner, err := adapter.Accept(ctx, runnerRequest)
	if err != nil {
		t.Fatal(err)
	}
	attempt := *acceptedRunner.FleetRunnerAttempt
	snapshot := json.RawMessage(fmt.Sprintf(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"%s"},{"name":"ingress-02","privateIP":"%s"}],"nodesFileSHA256":"%s","schemaVersion":"norn.fleet-ingress-inventory/v1"}`, addresses[0], addresses[1], strings.Repeat("c", 64)))
	canonical, err := fleet.CanonicalIngressInventory(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	phases := []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}
	for index, phase := range phases[:len(phases)-1] {
		checkpoint := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: phase, Status: "succeeded", CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, AttemptID: attempt.ID, EvidenceDigest: "sha256:" + strings.Repeat("d", 64), StateSerial: 7}
		if phase == "nodes_configured" {
			checkpoint.IngressInventory, checkpoint.IngressInventoryDigest = snapshot, digest
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		checkRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:evidence"}, Kind: "fleet.reconciliation", Resource: plan.ID, Key: "route-" + phase}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: plan.ID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": checkpoint}, FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: attempt.ID, RequireActiveAttempt: true, RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL}}
		checkRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(checkRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, checkRequest); err != nil {
			t.Fatalf("accept %s: %v", phase, err)
		}
		updated, err := adapter.UpdateFleetRunnerAttempt(ctx, plan.ID, attempt.ID, attempt.Revision, "advance", phases[index+1])
		if err != nil {
			t.Fatalf("advance %s: %v", phase, err)
		}
		attempt = *updated
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082); err != nil {
		t.Fatal(err)
	}
}
