package etcdstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func fleetRunnerEtcdStore(t *testing.T) (*etcdstore.V3OperationStore, *clientv3.Client, string) {
	t.Helper()
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/v3-fleet-runner/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-runner-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, client, prefix
}

func fleetRunnerPlan(t *testing.T, adapter *etcdstore.V3OperationStore, action string) model.Operation {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	finished := now
	plan := model.Operation{ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: map[string]interface{}{"id": "pending", "action": action, "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}}, Metadata: map[string]interface{}{}}
	plan.Payload["id"] = plan.ID
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: plan.Kind, Resource: plan.Ref, Key: "plan-" + plan.ID}, Operation: plan, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return plan
}

func bindFleetRunnerDispatch(t *testing.T, adapter *etcdstore.V3OperationStore, plan model.Operation) string {
	t.Helper()
	raw := strings.Repeat("f", 64)
	digest := sha256.Sum256([]byte(raw))
	nonce := hex.EncodeToString(digest[:])
	if err := adapter.BindFleetRunnerDispatch(context.Background(), etcdstore.FleetRunnerDispatchBinding{PlanID: plan.ID, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), DispatchNonceSHA256: nonce, RunID: 7, WorkflowURL: "https://github.com/acme/fleet/actions/runs/7"}); err != nil {
		t.Fatal(err)
	}
	return nonce
}

func fleetRunnerAcceptance(t *testing.T, adapter *etcdstore.V3OperationStore, planID, nonce, key, runID, intent, predecessor string) store.OperationAcceptance {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	attemptID := uuid.NewString()
	runnerID := "github:acme/fleet:" + runID + ":1"
	workflow := "https://github.com/acme/fleet/actions/runs/" + runID
	admission := &store.FleetRunnerAttemptAdmission{PlanID: planID, AttemptID: attemptID, ExpectedPredecessorID: predecessor, RunnerAttemptID: runnerID, CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64), WorkflowURL: workflow, DispatchNonceSHA256: nonce, SourceDispatchRunID: "7", Resume: intent == "recover", HeartbeatTimeoutSeconds: 120, WorkloadIntent: intent, WorkloadRunID: runID, WorkloadSHA: strings.Repeat("c", 40)}
	payload := map[string]interface{}{"attemptId": attemptID, "planId": planID, "runnerAttemptId": runnerID, "commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": workflow, "dispatchNonceSha256": nonce, "sourceDispatchRunId": "7", "resume": admission.Resume, "heartbeatTimeoutSeconds": admission.HeartbeatTimeoutSeconds}
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:" + runID}, Kind: "fleet.runner-attempt", Resource: planID, Key: key}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: planID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.runner-attempt", "planId": planID, "runnerAttemptId": runnerID, "commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": workflow, "dispatchNonceSha256": nonce, "sourceDispatchRunId": "7", "resume": admission.Resume, "heartbeatTimeoutSeconds": admission.HeartbeatTimeoutSeconds, "workload": map[string]interface{}{"intent": intent, "runId": runID, "sha": admission.WorkloadSHA}}, FleetRunnerAttempt: admission}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestV3FleetRunnerAttemptAcceptanceReplayAndRevisionCASEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	request := fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "first", "7", "apply", "")
	first, err := adapter.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.FleetRunnerAttempt == nil || first.FleetRunnerAttempt.Attempt != 1 || first.FleetRunnerAttempt.RootAttemptID != first.FleetRunnerAttempt.ID {
		t.Fatalf("accepted attempt=%#v", first.FleetRunnerAttempt)
	}
	replay := request
	replay.Operation.ID = uuid.NewString()
	replay.Operation.SagaID = uuid.NewString()
	second, err := adapter.Accept(context.Background(), replay)
	if err != nil || !second.Replayed || second.Operation.ID != first.Operation.ID || second.FleetRunnerAttempt.ID != first.FleetRunnerAttempt.ID {
		t.Fatalf("replay=%#v err=%v", second, err)
	}
	updated, err := adapter.UpdateFleetRunnerAttempt(context.Background(), plan.ID, first.FleetRunnerAttempt.ID, first.FleetRunnerAttempt.Revision, "heartbeat", int64(1), "alive")
	if err != nil {
		t.Fatal(err)
	}
	if updated.HeartbeatSequence != 1 || updated.Revision != 2 {
		t.Fatalf("heartbeat=%#v", updated)
	}
	if _, err := adapter.UpdateFleetRunnerAttempt(context.Background(), plan.ID, first.FleetRunnerAttempt.ID, first.FleetRunnerAttempt.Revision, "cancel", "stale"); !errors.Is(err, etcdstore.ErrNotFound) {
		t.Fatalf("stale cancel err=%v", err)
	}
	if _, err := adapter.Accept(context.Background(), fleetReconciliationAcceptance(t, adapter, plan, *updated, "prechange", "prechange_verified")); err != nil {
		t.Fatalf("prechange evidence: %v", err)
	}
	if _, err := adapter.UpdateFleetRunnerAttempt(context.Background(), plan.ID, first.FleetRunnerAttempt.ID, updated.Revision, "advance", "complete"); err != nil {
		t.Fatal(err)
	}
}

func TestV3FleetRunnerAttemptAcceptsOneConcurrentRecoveryEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	root, err := adapter.Accept(context.Background(), fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "root", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-runner-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	secondProcess, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	requests := []store.OperationAcceptance{fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "recover-a", "8", "recover", root.FleetRunnerAttempt.ID), fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "recover-b", "9", "recover", root.FleetRunnerAttempt.ID)}
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	var wait sync.WaitGroup
	for index, request := range requests {
		current := adapter
		if index == 1 {
			current = secondProcess
		}
		wait.Add(1)
		go func(current *etcdstore.V3OperationStore, request store.OperationAcceptance) {
			defer wait.Done()
			<-start
			_, err := current.Accept(context.Background(), request)
			outcomes <- err
		}(current, request)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	accepted, rejected := 0, 0
	for err := range outcomes {
		if err == nil {
			accepted++
		} else if errors.Is(err, store.ErrFleetRunnerAttemptAdmission) {
			rejected++
		} else {
			t.Fatalf("recovery err=%v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	attempts, err := adapter.ListFleetRunnerAttempts(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 2 || attempts[1].Attempt != 1 {
		t.Fatalf("attempts=%#v", attempts)
	}
}

func TestV3FleetRunnerAttemptRejectsForgedDispatchEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	request := fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "forged", "7", "apply", "")
	request.FleetRunnerAttempt.DispatchNonceSHA256 = strings.Repeat("a", 64)
	request.Operation.Payload["dispatchNonceSha256"] = strings.Repeat("a", 64)
	request.Semantics["dispatchNonceSha256"] = strings.Repeat("a", 64)
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(context.Background(), request); !errors.Is(err, store.ErrFleetRunnerAttemptAdmission) {
		t.Fatalf("forged dispatch err=%v", err)
	}
}
