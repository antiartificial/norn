package etcdstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
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
	plan := model.Operation{ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: map[string]interface{}{"id": "pending", "cluster": "norn-staging", "digest": "test-plan-digest", "action": action, "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}}, Metadata: map[string]interface{}{}}
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
	authority := mustFleetRunnerAuthority(t, adapter)
	planDigest, _ := plan.Payload["digest"].(string)
	payload := map[string]interface{}{"planId": plan.ID, "planDigest": planDigest, "sourceDigest": "", "planRunId": int64(7), "planSha256": strings.Repeat("b", 64), "approvedHeadSha": strings.Repeat("c", 40), "fleetEnvironment": "staging/nyc3", "allowDestructive": false}
	now := time.Now().UTC().Truncate(time.Microsecond)
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"fleetGitHub": payload}, Metadata: map[string]interface{}{}}
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: plan.ID}, Kind: op.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": payload}}
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	_, prepared, err := adapter.AcceptFleetGitHubDispatch(context.Background(), request, etcdstore.FleetGitHubDispatchPreparation{PlanID: plan.ID, PlanRunID: 7, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	return prepared.DispatchNonceSHA256
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

func verifiedFleetRecoveryAcceptance(t *testing.T, adapter *etcdstore.V3OperationStore, planID, nonce, key, runID string, predecessor *fleet.RunnerAttempt) store.OperationAcceptance {
	t.Helper()
	request := fleetRunnerAcceptance(t, adapter, planID, nonce, key, runID, "recover", predecessor.ID)
	proof := &store.FleetRunnerPredecessorStopEvidence{PredecessorID: predecessor.ID, SourceDispatchRunID: "7", RunAttempt: 1, WorkflowURL: "https://github.com/acme/fleet/actions/runs/7", Status: "completed", Conclusion: "failure", ObservedAt: time.Now().UTC()}
	request.FleetRunnerAttempt.PredecessorStop = proof
	request.Operation.Payload["predecessorStop"] = proof
	request.Semantics["predecessorStop"] = proof
	var err error
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

func TestV3FleetRunnerAttemptAllowsOneVerifiedRecoverySuccessorEtcd(t *testing.T) {
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
	requests := []store.OperationAcceptance{verifiedFleetRecoveryAcceptance(t, adapter, plan.ID, nonce, "recover-a", "8", root.FleetRunnerAttempt), verifiedFleetRecoveryAcceptance(t, adapter, plan.ID, nonce, "recover-b", "9", root.FleetRunnerAttempt)}
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
	accepted := 0
	rejected := 0
	for err := range outcomes {
		if err == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want one winner", accepted, rejected)
	}
	attempts, err := adapter.ListFleetRunnerAttempts(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 2 || attempts[0].RetryOf != root.FleetRunnerAttempt.ID || attempts[0].RootAttemptID != root.FleetRunnerAttempt.ID || attempts[1].ID != root.FleetRunnerAttempt.ID || attempts[1].Status != "failed" {
		t.Fatalf("attempts=%#v", attempts)
	}
	if _, err := adapter.Accept(context.Background(), verifiedFleetRecoveryAcceptance(t, adapter, plan.ID, nonce, "recover-third", "10", &attempts[0])); !errors.Is(err, store.ErrFleetRunnerAttemptAdmission) {
		t.Fatalf("third successor err=%v", err)
	} else {
		var admissionErr *store.FleetRunnerAttemptAdmissionError
		if !errors.As(err, &admissionErr) || admissionErr.Code != "fleet_runner_attempt_recovery_limit" {
			t.Fatalf("third successor admission=%v", err)
		}
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

// TestV3FleetRunnerFirstAttemptEmptyRegistryMatchesBaseEtcd pins the pre-branch
// behaviour with no fleet targets registered: the first attempt is admitted by
// the protected dispatch alone, writes no fence or registry state, and a
// missing dispatch preparation is refused exactly as before the fence work
// (the dispatch-completion verification already requires it).
func TestV3FleetRunnerFirstAttemptEmptyRegistryMatchesBaseEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	ctx := context.Background()
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	accepted, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "empty-registry", "7", "apply", ""))
	if err != nil || accepted.FleetRunnerAttempt == nil || accepted.FleetRunnerAttempt.Attempt != 1 || accepted.FleetRunnerAttempt.Status != "queued" {
		t.Fatalf("empty registry must admit the first attempt: %#v err=%v", accepted, err)
	}
	keys, err := client.Get(ctx, prefix+"/v3/fleet-target", clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil || len(keys.Kvs) != 0 {
		t.Fatalf("an empty registry must leave no fence or target state: %v err=%v", keys.Kvs, err)
	}

	other := fleetRunnerPlan(t, adapter, "scale")
	otherNonce := bindFleetRunnerDispatch(t, adapter, other)
	if _, err := client.Delete(ctx, prefix+"/v3/fleet-github-dispatch-preparations/"+other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, other.ID, otherNonce, "no-preparation", "7", "apply", "")); err == nil || !strings.Contains(err.Error(), "not signed and complete") {
		t.Fatalf("a missing preparation is refused by dispatch verification: %v", err)
	}
}
