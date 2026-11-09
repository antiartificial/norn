package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestWorkloadReleaseReconcilerRecoversRouteLostResponseAfterRestartEtcd(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	newClient := func() *clientv3.Client {
		t.Helper()
		client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := newClient()
	prefix := "/norn-tests/workload-release-runtime-restart/" + uuid.NewString()
	t.Cleanup(func() {
		if client != nil {
			_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
			_ = client.Close()
		}
	})
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-runtime-restart-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: "norn-fleet-canary",
		Status: model.OperationQueued, Source: "runtime-restart-integration", StartedAt: time.Now().UTC(), MaxAttempts: 3}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority,
			Actor: store.OperationActor{Issuer: "runtime-restart-integration", Subject: "operator"},
			Kind:  operation.Kind, Resource: "app/" + operation.App, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit: store.AcceptanceAuditContext{Source: "runtime-restart-integration"},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "workload-release-controller-before-crash", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("initial claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("initial app lock acquired=%v err=%v", acquired, err)
	}

	intent := workloadReleaseRuntimeRestartIntent()
	state := &workloadReleaseRuntimeRestartState{routeWrites: make(map[int]int), routeCalls: make(map[int]int),
		routeDigests: make(map[int]string), loseAfterStep: 1}
	firstProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: intent, operationID: operation.ID}
	result, err := reconcileWorkloadRelease(ctx, claim, lock, firstProcess)
	var pending *effect.PendingError
	if !errors.As(err, &pending) || result != nil {
		t.Fatalf("pre-crash reconcile=(%+v,%v), want pending lost route response", result, err)
	}
	if state.routeWrites[1] != 1 || state.routeCalls[1] != 1 || state.lostResponseInjected != true {
		t.Fatalf("route effect before crash: writes=%v calls=%v lost=%t", state.routeWrites, state.routeCalls, state.lostResponseInjected)
	}
	record, receipt, err := firstProcess.loadReceipt(ctx)
	if err != nil || record == nil || receipt.RouteStep != 0 || receipt.RouteGeneration != intent.FirstRouteGeneration {
		t.Fatalf("durable predecessor after lost route response: record=%+v receipt=%+v err=%v", record, receipt, err)
	}

	// Revoke the operation lease and release the app lock to model a worker
	// process exiting without completing its in-flight route step.
	owner, err := client.Get(ctx, prefix+"/v3/owners/"+operation.ID)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("operation owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := client.Close(); err != nil {
		t.Fatalf("close pre-crash etcd client: %v", err)
	}
	client = newClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := operations.ClaimNextOperation(ctx, "workload-release-controller-after-crash", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != operation.ID || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("successor app lock acquired=%v err=%v", acquired, err)
	}
	secondProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: intent, operationID: operation.ID}
	result, err = reconcileWorkloadRelease(ctx, successor, lock, secondProcess)
	if err != nil || result == nil || result.Status != model.OperationSucceeded {
		t.Fatalf("post-restart reconcile=(%+v,%v), want succeeded", result, err)
	}
	completed, receipt, err := secondProcess.loadReceipt(ctx)
	if err != nil || completed == nil || receipt.Phase != pipeline.WorkloadReleaseComplete ||
		receipt.RouteStep != len(intent.RouteSteps)-1 || len(receipt.DrainedOldIDs) != len(intent.OldAllocationIDs) {
		t.Fatalf("terminal receipt=%+v record=%+v err=%v", receipt, completed, err)
	}
	for step := range intent.RouteSteps {
		if state.routeWrites[step] != 1 {
			t.Errorf("route step %d applied %d times, want once (calls=%d)", step, state.routeWrites[step], state.routeCalls[step])
		}
	}
	if state.routeCalls[1] != 2 || state.stopWrites != 1 {
		t.Fatalf("recovery did not adopt same route effect: route-1 calls=%d stop writes=%d", state.routeCalls[1], state.stopWrites)
	}
}

type workloadReleaseRuntimeRestartState struct {
	routeWrites          map[int]int
	routeCalls           map[int]int
	routeDigests         map[int]string
	loseAfterStep        int
	lostResponseInjected bool
	stopWrites           int
	privateInputDeletes  int
	privateInputDeleted  bool
}

type workloadReleaseEtcdReceiptDriver struct {
	operations  *etcdstore.V3OperationStore
	state       *workloadReleaseRuntimeRestartState
	intent      pipeline.WorkloadReleaseIntent
	operationID string
}

func (d workloadReleaseEtcdReceiptDriver) Verify(_ context.Context, claim store.OperationClaim, _ store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	if claim.OperationID() != d.operationID || d.intent.Validate() != nil {
		return pipeline.WorkloadReleaseIntent{}, errors.New("test workload release intent is invalid")
	}
	return d.intent, nil
}

func (d workloadReleaseEtcdReceiptDriver) EnsureTargetBound(context.Context, store.OperationClaim, store.AppOperationLock, pipeline.WorkloadReleaseIntent) error {
	return nil
}

func (d workloadReleaseEtcdReceiptDriver) Load(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	record, err := d.operations.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || record == nil {
		return record, pipeline.WorkloadReleaseReceipt{}, err
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if record.OperationID != d.operationID || json.Unmarshal(record.Receipt, &receipt) != nil || receipt.ValidateAgainst(intent) != nil {
		return nil, pipeline.WorkloadReleaseReceipt{}, errors.New("test driver loaded an invalid durable receipt")
	}
	return record, receipt, nil
}

func (d workloadReleaseEtcdReceiptDriver) Initialize(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	planned, err := json.Marshal(pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleasePlanned})
	if err != nil {
		return err
	}
	_, err = d.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", planned)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) Advance(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent,
	expected string, next pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	if expected != "" {
		_, current, err := d.Load(ctx, intent)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		if err := next.ValidateSuccessor(intent, current); err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
	} else if err := next.ValidateAgainst(intent); err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	return d.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, expected, encoded)
}

func (d workloadReleaseEtcdReceiptDriver) ObserveHealth(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("durable receipt was unavailable during target health readback")
	}
	if len(receipt.TargetAllocations) == intent.TargetReplicas && healthyWorkloadTargets(receipt) == intent.TargetReplicas {
		return nil
	}
	receipt.TargetAllocations = []pipeline.WorkloadReleaseAllocationObservation{
		{AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: true},
		{AllocationID: "target-b", NodeID: "node-b", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: true},
	}
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) PublishRouteStep(ctx context.Context, claim store.OperationClaim, _ store.AppOperationLock,
	intent pipeline.WorkloadReleaseIntent, step int) error {
	if step < 0 || step >= len(intent.RouteSteps) {
		return errors.New("route step is outside the admitted intent")
	}
	rendered, err := intent.RenderRouteStep(step)
	if err != nil {
		return err
	}
	d.state.routeCalls[step]++
	if prior, exists := d.state.routeDigests[step]; exists && prior != rendered.SHA256 {
		return errors.New("same route generation resolved to different content")
	}
	if _, exists := d.state.routeDigests[step]; !exists {
		d.state.routeDigests[step] = rendered.SHA256
		d.state.routeWrites[step]++
	}
	if step == d.state.loseAfterStep && !d.state.lostResponseInjected {
		d.state.lostResponseInjected = true
		return &effect.PendingError{Resource: "app/" + intent.App + "/workload-release/" + intent.Region,
			Reason: "simulated lost route response after external publication"}
	}
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return errors.New("durable receipt was unavailable while reconciling route")
	}
	stepPolicy := intent.RouteSteps[step]
	receipt.RouteStep, receipt.RouteOldWeight, receipt.RouteTargetWeight = step, stepPolicy.OldWeight, stepPolicy.TargetWeight
	receipt.RouteGeneration, receipt.RouteSHA256 = intent.FirstRouteGeneration+uint64(step), "sha256:"+rendered.SHA256
	if step == len(intent.RouteSteps)-1 {
		receipt.Phase = pipeline.WorkloadReleaseDraining
	} else {
		receipt.Phase = pipeline.WorkloadReleaseRouting
	}
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) StopAndReadback(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	if d.state.stopWrites == 0 {
		d.state.stopWrites++
	}
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return errors.New("durable receipt was unavailable while reconciling drain")
	}
	receipt.OldJobStoppedVersion = intent.OldJobVersion + 1
	receipt.OldJobStoppedModifyIndex = intent.OldJobModifyIndex + 2
	receipt.DrainedOldIDs = append([]string(nil), intent.OldAllocationIDs...)
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) Complete(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return store.WorkloadReleaseReceiptRecord{}, errors.New("durable receipt was unavailable at completion")
	}
	receipt.Phase = pipeline.WorkloadReleaseComplete
	return d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
}

func (d workloadReleaseEtcdReceiptDriver) DeleteCompletedPrivateInput(ctx context.Context, _ store.OperationClaim, _ store.AppOperationLock,
	intent pipeline.WorkloadReleaseIntent, completed store.WorkloadReleaseReceiptRecord) error {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil || receipt.Phase != pipeline.WorkloadReleaseComplete || record.ReceiptDigest != completed.ReceiptDigest {
		return errors.New("private input cleanup did not receive the durable completed receipt")
	}
	d.state.privateInputDeletes++
	d.state.privateInputDeleted = true
	return nil
}

func (d workloadReleaseEtcdReceiptDriver) loadReceipt(ctx context.Context) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	return d.Load(ctx, d.intent)
}

func workloadReleaseRuntimeRestartIntent() pipeline.WorkloadReleaseIntent {
	return pipeline.WorkloadReleaseIntent{
		ID: "runtime-restart-intent", App: "norn-fleet-canary", Region: "nyc3", NomadRegion: "global", Namespace: "default",
		ControlEnvironment: "staging", FleetCluster: "norn-staging", FleetEnvironment: "staging/nyc3", FleetDatacenters: []string{"nyc3"}, FleetTargetGeneration: 3,
		Process: "web", Endpoint: "https://canary.example.test", PublicPaths: []string{"/route-proof", "/version"},
		RetryGETPaths: []string{"/version"}, PrivateReadinessPath: "/readyz", TrafficProbePath: "/route-proof",
		TrafficProbeBodySHA256: strings.Repeat("a", 64),
		SpecDigest:             "sha256:" + strings.Repeat("1", 64), SourceRevision: "git:runtime-restart-test", OldDeploymentID: "deployment-old",
		OldImageDigest: "sha256:" + strings.Repeat("2", 64), OldSpecDigest: "sha256:" + strings.Repeat("4", 64),
		OldJobID: "norn-fleet-canary-old", OldJobDigest: "sha256:" + strings.Repeat("a", 64), OldJobVersion: 7, OldJobModifyIndex: 41,
		TargetDeploymentID: "deployment-new", TargetImage: "registry.example.test/norn-fleet-canary@sha256:" + strings.Repeat("3", 64),
		TargetImageDigest: "sha256:" + strings.Repeat("3", 64), TargetJobID: "norn-fleet-canary-new",
		TargetJobDigest: "sha256:" + strings.Repeat("4", 64), TargetSpecDigest: "sha256:" + strings.Repeat("5", 64),
		TargetOperationID: "operation-target", TargetExecutionID: "nomad-deployment-target", OldAllocationIDs: []string{"old-a", "old-b"},
		TargetJobVersion: 8, TargetJobModifyIndex: 43, TargetReplicas: 2, SurgeLimit: 1,
		PolicyApprovalID: "11111111-1111-4111-8111-111111111111", PolicyDigest: "sha256:" + strings.Repeat("6", 64), FirstRouteGeneration: 11,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}},
	}
}
