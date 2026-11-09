package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestDeleteCompletedWorkloadReleasePrivateInputIsClaimFencedAndRestartSafe(t *testing.T) {
	adapter, client, prefix := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	app := "widgets-retention-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging",
		FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default",
		Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseEtcdIntentFixture(app, target)
	intent.Endpoint = "https://widgets.example.test"
	intent.PublicPaths = []string{"/status"}
	intent.RetryGETPaths = []string{"/status"}
	intent.PrivateReadinessPath = "/ready"
	intent.TrafficProbePath = "/status"
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	plan := pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		MaxAttempts: 3, Source: "retention-integration"}
	keys, err := store.NewWorkloadReleaseInputKeyRing("input-v1", map[string][]byte{"input-v1": bytes.Repeat([]byte{0x5a}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	binding := store.WorkloadReleaseInputBinding{Authority: adapter.authority, Operation: operation.ID, App: app, PlanDigest: planDigest}
	envelope, err := keys.Seal(binding, []byte("pinned encrypted workload material"))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := store.WorkloadReleaseInputDigest(envelope)
	if err != nil {
		t.Fatal(err)
	}
	operation.Payload = map[string]interface{}{
		"workloadReleasePlan":               string(planBytes),
		"workloadReleasePrivateRecordId":    operation.ID,
		"workloadReleasePrivateInputDigest": inputDigest,
		"workloadReleasePrivateKeyId":       envelope.KeyID,
	}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority,
			Actor: store.OperationActor{Issuer: "retention-integration", Subject: "operator"},
			Kind:  operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit: store.AcceptanceAuditContext{Source: "retention-integration"},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	privateBytes, err := json.Marshal(v3WorkloadReleasePrivateInputRecord{SchemaVersion: workloadReleasePrivateInputRecordSchema,
		KeyID: envelope.KeyID, Digest: inputDigest, Envelope: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.workloadReleasePrivateInputKey(operation.ID), string(privateBytes)); err != nil {
		t.Fatal(err)
	}

	claimed, claim, err := adapter.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	planReservation, err := adapter.WorkloadReleaseStageTargetReservation(ctx, claim, lock)
	if err != nil {
		t.Fatal(err)
	}
	stageEffects, err := NewV3WorkloadReleaseEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := stageEffects.Reserve(ctx, planReservation)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := adapter.MarkWorkloadReleaseStageTargetAttempt(ctx, claim, lock, reserved.Record.Token.EffectID, "retention-test-runtime"); err != nil || !allowed {
		t.Fatalf("target attempt allowed=%t err=%v", allowed, err)
	}
	observer := &workloadReleaseTargetObserver{observation: nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 9, JobModifyIndex: 47}}
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer); err != nil {
		t.Fatalf("bind exact target: %v", err)
	}
	resolved, _, _, err := adapter.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, reserved.Record.Token.EffectID)
	if err != nil {
		t.Fatal(err)
	}
	plan.Intent = resolved

	if err := adapter.DeleteCompletedWorkloadReleasePrivateInput(ctx, claim, lock, resolved,
		store.WorkloadReleaseReceiptRecord{IntentID: resolved.ID, OperationID: operation.ID, ReceiptDigest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Fatal("deleted pinned inputs before a complete durable release receipt")
	}
	if _, err := adapter.OpenPrivateWorkloadRelease(ctx, *claimed, keys); err != nil {
		t.Fatalf("pending release lost its recovery inputs: %v", err)
	}

	lastStep := len(resolved.RouteSteps) - 1
	route, err := resolved.RenderRouteStep(lastStep)
	if err != nil {
		t.Fatal(err)
	}
	complete := pipeline.WorkloadReleaseReceipt{IntentID: resolved.ID, Phase: pipeline.WorkloadReleaseComplete,
		TargetAllocations: []pipeline.WorkloadReleaseAllocationObservation{{AllocationID: "target-a", NodeID: "node-a", JobID: resolved.TargetJobID,
			Namespace: resolved.Namespace, TaskGroup: resolved.Process, JobVersion: resolved.TargetJobVersion,
			JobModifyIndex: resolved.TargetJobModifyIndex, ImageDigest: resolved.TargetImageDigest, Healthy: true}},
		RouteStep: lastStep, RouteOldWeight: 0, RouteTargetWeight: 100,
		RouteGeneration: resolved.FirstRouteGeneration + uint64(lastStep), RouteSHA256: "sha256:" + route.SHA256,
		OldJobStoppedVersion: resolved.OldJobVersion + 1, OldJobStoppedModifyIndex: resolved.OldJobModifyIndex + 2,
		DrainedOldIDs: append([]string(nil), resolved.OldAllocationIDs...)}
	complete.TargetAllocations = []pipeline.WorkloadReleaseAllocationObservation{{AllocationID: "target-a", NodeID: "node-target-a", JobID: resolved.TargetJobID,
		Namespace: resolved.Namespace, TaskGroup: resolved.Process, JobVersion: resolved.TargetJobVersion,
		JobModifyIndex: resolved.TargetJobModifyIndex, ImageDigest: resolved.TargetImageDigest, Healthy: true}}
	if err := complete.ValidateAgainst(resolved); err != nil {
		t.Fatalf("test completion receipt is invalid: %v", err)
	}
	receiptBytes, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, resolved.ID, "", receiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.DeleteCompletedWorkloadReleasePrivateInput(ctx, claim, lock, resolved, completed); err != nil {
		t.Fatalf("delete inputs after completion: %v", err)
	}
	if err := adapter.DeleteCompletedWorkloadReleasePrivateInput(ctx, claim, lock, resolved, completed); err != nil {
		t.Fatalf("repeat completed input cleanup: %v", err)
	}
	if _, err := adapter.OpenPrivateWorkloadRelease(ctx, *claimed, keys); err == nil {
		t.Fatal("completed release private input remained readable after cleanup")
	}
	if ids, err := adapter.RequiredWorkloadReleaseInputKeys(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("deleted release still pins key IDs=%v err=%v", ids, err)
	}
	deletionBytes, err := client.Get(ctx, adapter.workloadReleasePrivateInputDeletionKey(operation.ID))
	if err != nil || len(deletionBytes.Kvs) != 1 {
		t.Fatalf("deletion receipt missing: values=%d err=%v", len(deletionBytes.Kvs), err)
	}
	var deletion workloadReleasePrivateInputDeletion
	if decodeV3Record(deletionBytes.Kvs[0].Value, &deletion) != nil || deletion.InputDigest != inputDigest ||
		deletion.ReceiptDigest != completed.ReceiptDigest || deletion.IntentID != resolved.ID {
		t.Fatalf("deletion receipt does not bind the accepted input to terminal evidence: %+v", deletion)
	}

	owner, err := client.Get(ctx, adapter.ownerKey(operation.ID))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner lease=%v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	lock.Release()

	restartedClient, err := clientv3.New(clientv3.Config{Endpoints: client.Endpoints(), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedClient.Close() })
	restartedSigner, err := store.NewHMACAcceptanceSigner("norn-etcd-function-effects-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewV3OperationStore(restartedClient, prefix, adapter.authority, restartedSigner)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := restarted.ClaimNextOperation(ctx, "retention-worker-after-crash", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != operation.ID || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	successorLock, acquired, err := restarted.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("successor app lock acquired=%v err=%v", acquired, err)
	}
	defer successorLock.Release()
	if err := restarted.DeleteCompletedWorkloadReleasePrivateInput(ctx, successor, successorLock, resolved, completed); err != nil {
		t.Fatalf("successor could not reconcile the durable deletion receipt: %v", err)
	}
}
