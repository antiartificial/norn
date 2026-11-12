package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	clientv3 "go.etcd.io/etcd/client/v3"
	"sync"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/fleet/fleetcreate"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
)

type fleetCreateAmbiguousKV struct {
	clientv3.KV
	failWrite *bool
}

func (kv fleetCreateAmbiguousKV) Txn(ctx context.Context) clientv3.Txn {
	return fleetCreateAmbiguousTxn{Txn: kv.KV.Txn(ctx), kv: kv}
}

type fleetCreateAmbiguousTxn struct {
	clientv3.Txn
	kv    fleetCreateAmbiguousKV
	write bool
}

func (txn fleetCreateAmbiguousTxn) If(compares ...clientv3.Cmp) clientv3.Txn {
	return fleetCreateAmbiguousTxn{Txn: txn.Txn.If(compares...), kv: txn.kv, write: txn.write}
}

func (txn fleetCreateAmbiguousTxn) Then(ops ...clientv3.Op) clientv3.Txn {
	for _, operation := range ops {
		if operation.IsPut() {
			txn.write = true
		}
	}
	return fleetCreateAmbiguousTxn{Txn: txn.Txn.Then(ops...), kv: txn.kv, write: txn.write}
}

func (txn fleetCreateAmbiguousTxn) Commit() (*clientv3.TxnResponse, error) {
	response, err := txn.Txn.Commit()
	if err != nil {
		return response, err
	}
	if txn.kv.failWrite != nil && *txn.kv.failWrite && txn.write && response.Succeeded {
		*txn.kv.failWrite = false
		return nil, context.DeadlineExceeded
	}
	return response, nil
}

func fleetCreateIntentForPlan(plan fleettest.ExecutorPlanRef) fleetcreate.Intent {
	digest := func(value string) string { return fleetcreate.SHA256([]byte(value)) }
	return fleetcreate.Intent{SchemaVersion: fleetcreate.IntentSchema, IntentID: uuid.NewString(), PlanID: plan.ID,
		Cluster: plan.Cluster, CommitSHA: plan.CommitSHA, PlanSHA256: plan.PlanSHA256,
		ProviderAccountSHA256: digest("provider-account"), StateBackendSHA256: digest("spaces://bucket/state-backend"),
		StateLineageSHA256: digest("state-lineage"), StateSerial: 3,
		TopologySHA256: digest("topology"), SSHInputsSHA256: digest("ssh-inputs")}
}

func fleetCreateStartForIntent(intent fleetcreate.Intent, attemptID string) fleetcreate.AttemptStartRequest {
	digest, _ := intent.Digest()
	dispatch := fleetCreateDispatchForIntent(intent)
	return fleetcreate.AttemptStartRequest{SchemaVersion: fleetcreate.StartSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, AttemptID: attemptID, IntentSHA256: digest,
		RunnerAttemptID: dispatch.RunnerAttemptID(), DispatchReceiptSHA256: dispatch.DispatchReceiptSHA256}
}

func fleetCreateDispatchForIntent(intent fleetcreate.Intent) fleetcreate.DispatchBinding {
	digest, _ := intent.Digest()
	return fleetcreate.DispatchBinding{SchemaVersion: fleetcreate.DispatchSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, IntentSHA256: digest,
		Repository: "antiartificial/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202",
		WorkflowPath: ".github/workflows/fresh-fleet-create.yml", WorkflowSHA: intent.CommitSHA,
		Environment: "work-staging", RunID: "100", RunAttempt: "1", CommitSHA: intent.CommitSHA,
		DispatchReceiptSHA256: fleetcreate.SHA256([]byte("verified-dispatch-receipt"))}
}

func bindFleetCreateDispatch(t *testing.T, h *etcdExecutorHarness, intent fleetcreate.Intent) {
	t.Helper()
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), fleetCreateDispatchForIntent(intent), fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
}

func fleetCreateAdmissionForAttempt(intent fleetcreate.Intent, attempt fleetcreate.Attempt) fleetcreate.Admission {
	digest, _ := intent.Digest()
	return fleetcreate.Admission{SchemaVersion: fleetcreate.AdmissionSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, AttemptID: attempt.AttemptID, IntentSHA256: digest,
		RemainderSHA256:        fleetcreate.SHA256([]byte("initial-nine-node-remainder")),
		ProviderReadbackSHA256: fleetcreate.SHA256([]byte("empty-provider-inventory")),
		StateReadbackSHA256:    fleetcreate.SHA256([]byte("empty-state-inventory")),
		StateLineageSHA256:     intent.StateLineageSHA256, StateSerial: intent.StateSerial,
		ExpectedNodeCount: 9, ObservedNodeCount: 0}
}

func fleetCreateRequestForAdmission(admission fleetcreate.Admission) fleetcreate.Request {
	return fleetcreate.Request{SchemaVersion: fleetcreate.RequestSchema, IntentID: admission.IntentID,
		PlanID: admission.PlanID, AttemptID: admission.AttemptID, IntentSHA256: admission.IntentSHA256,
		AdmissionSHA256: admission.Digest(), RemainderSHA256: admission.RemainderSHA256}
}

func admittedFleetCreateFixture(t *testing.T, cluster string) (*etcdExecutorHarness, fleetcreate.Request) {
	t.Helper()
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: cluster})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindFleetCreateDispatch(t, h, intent)
	attempt, err := h.s.StartFleetCreateAttempt(h.ctx(), fleetCreateStartForIntent(intent, uuid.NewString()), fleetCreateAttemptSubject)
	if err != nil {
		t.Fatal(err)
	}
	admission := fleetCreateAdmissionForAttempt(intent, attempt)
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), admission, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	return h, fleetCreateRequestForAdmission(admission)
}

func TestFleetCreateEffectGrantIssueConsumeIsOneUseRealEtcd(t *testing.T) {
	h, request := admittedFleetCreateFixture(t, "fresh-create-grant")
	grant, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request)
	if err != nil || !grant.MatchesRequest(request) {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	readback, consumedState, err := h.s.GetFleetCreateEffectGrant(h.ctx(), request.IntentID, request.AttemptID)
	if err != nil || readback != grant || consumedState {
		t.Fatalf("issued readback=%+v consumed=%v err=%v", readback, consumedState, err)
	}
	replayed, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request)
	if err != nil || replayed != grant {
		t.Fatalf("exact issue replay=%+v err=%v", replayed, err)
	}
	changed := request
	changed.RemainderSHA256 = fleetcreate.SHA256([]byte("substituted remainder"))
	if _, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), changed); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("changed remainder issue=%v", err)
	}
	consumed, err := h.s.ConsumeFleetCreateEffectGrant(h.ctx(), request)
	if err != nil || consumed != grant {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	readback, consumedState, err = h.s.GetFleetCreateEffectGrant(h.ctx(), request.IntentID, request.AttemptID)
	if err != nil || readback != grant || !consumedState {
		t.Fatalf("consumed readback=%+v consumed=%v err=%v", readback, consumedState, err)
	}
	if _, err := h.s.ConsumeFleetCreateEffectGrant(h.ctx(), request); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("second consume=%v", err)
	}
	if _, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("issue after consume=%v", err)
	}
}

func TestFleetCreateEffectGrantConcurrentConsumersHaveOneWinnerRealEtcd(t *testing.T) {
	h, request := admittedFleetCreateFixture(t, "fresh-create-grant-cas")
	if _, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = h.s.ConsumeFleetCreateEffectGrant(context.Background(), request)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrFleetCreateIntentRefused) {
			t.Fatalf("unexpected consumer result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one consumer, got %d: %v", winners, results)
	}
}

func TestFleetCreateEffectGrantRecoversIssueButDoesNotReplayAmbiguousConsume(t *testing.T) {
	h, request := admittedFleetCreateFixture(t, "fresh-create-grant-ambiguous")
	failWrite := true
	h.s.kv = fleetCreateAmbiguousKV{KV: h.s.kv, failWrite: &failWrite}
	grant, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request)
	if err != nil || !grant.MatchesRequest(request) || failWrite {
		t.Fatalf("ambiguous issue recovery grant=%+v err=%v failWrite=%v", grant, err, failWrite)
	}
	failWrite = true
	h.s.kv = fleetCreateAmbiguousKV{KV: h.s.kv, failWrite: &failWrite}
	consumed, err := h.s.ConsumeFleetCreateEffectGrant(h.ctx(), request)
	if err == nil || consumed != (fleetcreate.Grant{}) || failWrite {
		t.Fatalf("ambiguous consume recovery grant=%+v err=%v failWrite=%v", consumed, err, failWrite)
	}
	readback, consumedState, err := h.s.GetFleetCreateEffectGrant(h.ctx(), request.IntentID, request.AttemptID)
	if err != nil || readback != grant || !consumedState {
		t.Fatalf("recovered readback=%+v consumed=%v err=%v", readback, consumedState, err)
	}
}

func TestFleetCreateTerminalReceiptHandsOffFenceAtomicallyAndRecoversLostResponse(t *testing.T) {
	h, request := admittedFleetCreateFixture(t, "fresh-create-terminal")
	grant, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ConsumeFleetCreateEffectGrant(h.ctx(), request); err != nil {
		t.Fatal(err)
	}
	intent, err := h.s.GetFleetCreateIntent(h.ctx(), "fresh-create-terminal")
	if err != nil {
		t.Fatal(err)
	}
	terminal := fleetcreate.TerminalReceipt{SchemaVersion: fleetcreate.TerminalSchema, IntentID: request.IntentID,
		PlanID: request.PlanID, Cluster: "fresh-create-terminal", AttemptID: request.AttemptID,
		IntentSHA256: request.IntentSHA256, AdmissionSHA256: request.AdmissionSHA256, GrantSHA256: grant.Digest(),
		ProviderReadbackSHA256: fleetcreate.SHA256([]byte("complete provider inventory")),
		StateReadbackSHA256:    fleetcreate.SHA256([]byte("complete state inventory")),
		NodeIdentitySetSHA256:  fleetcreate.SHA256([]byte("nine exact node identities")),
		StateLineageSHA256:     intent.StateLineageSHA256, StateSerial: intent.StateSerial + 1,
		ExpectedNodeCount: 9, ObservedNodeCount: 9, Provider: "digitalocean",
		ProviderAccount: "provider-account", StateBackend: "spaces://bucket/state-backend"}
	// Use values whose hashes match the saved intent's opaque account/backend
	// commitments; the store does not interpret either value.
	terminal.ProviderAccount = "provider-account"
	if terminal.Validate() != nil {
		t.Fatalf("terminal contract invalid: %+v", terminal)
	}
	if err := h.s.RecordFleetCreateTerminalReceipt(h.ctx(), terminal, "wrong-verifier"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong verifier = %v", err)
	}
	partial := terminal
	partial.ObservedNodeCount = 8
	if err := h.s.RecordFleetCreateTerminalReceipt(h.ctx(), partial, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("partial receipt = %v", err)
	}
	// The helper fixture commits the account/backend hashes from these exact
	// strings, so the successful receipt is bound to the saved intent.
	if fleetcreate.SHA256([]byte(terminal.ProviderAccount)) != intent.ProviderAccountSHA256 ||
		fleetcreate.SHA256([]byte(terminal.StateBackend)) != intent.StateBackendSHA256 {
		t.Fatal("test fixture account/backend values do not match the intent")
	}
	failWrite := true
	h.s.kv = fleetCreateAmbiguousKV{KV: h.s.kv, failWrite: &failWrite}
	if err := h.s.RecordFleetCreateTerminalReceipt(h.ctx(), terminal, fleetCreateVerifierSubject); err != nil || failWrite {
		t.Fatalf("record terminal after response loss = %v, failWrite=%v", err, failWrite)
	}
	if err := h.s.RecordFleetCreateTerminalReceipt(h.ctx(), terminal, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("terminal replay = %v", err)
	}
	failWrite = true
	target, err := h.s.RegisterFleetCreateTerminalTarget(h.ctx(), terminal)
	if err != nil || target == nil || target.RegistrationOperationID != "fleet-create-terminal:"+terminal.Digest() || failWrite {
		t.Fatalf("target handoff = %+v err=%v failWrite=%v", target, err, failWrite)
	}
	replayed, err := h.s.RegisterFleetCreateTerminalTarget(h.ctx(), terminal)
	if err != nil || replayed == nil || replayed.TargetID != target.TargetID {
		t.Fatalf("handoff replay = %+v err=%v", replayed, err)
	}
	if facts, _, err := h.s.GetFleetTargetFence(h.ctx(), target.TargetID); err != nil || facts.TargetID != target.TargetID || facts.Held {
		t.Fatalf("ordinary target fence unavailable after handoff: %+v err=%v", facts, err)
	}
	if fence, err := h.s.kv.Get(h.ctx(), h.s.fleetCreateFenceKey(terminal.Cluster)); err != nil || len(fence.Kvs) != 0 {
		t.Fatalf("create fence remains after handoff: rows=%d err=%v", len(fence.Kvs), err)
	}
}

func TestFleetCreateSuccessorRequiresConsumedGrantAndExactRecoveryReadback(t *testing.T) {
	h, request := admittedFleetCreateFixture(t, "fresh-create-successor")
	intent, err := h.s.GetFleetCreateIntent(h.ctx(), "fresh-create-successor")
	if err != nil {
		t.Fatal(err)
	}
	recoveryRequest := fleetCreateStartForIntent(intent, uuid.NewString())
	if _, err := h.s.StartFleetCreateRecoveryAttempt(h.ctx(), recoveryRequest, request.AttemptID, fleetCreateAttemptSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("successor without consumed grant/readback = %v", err)
	}
	grant, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ConsumeFleetCreateEffectGrant(h.ctx(), request); err != nil {
		t.Fatal(err)
	}
	readback := fleetcreate.RecoveryReadback{SchemaVersion: fleetcreate.RecoverySchema, IntentID: request.IntentID,
		PlanID: request.PlanID, Cluster: "fresh-create-successor", PriorAttemptID: request.AttemptID,
		IntentSHA256: request.IntentSHA256, PriorGrantSHA256: grant.Digest(),
		ProviderReadbackSHA256: fleetcreate.SHA256([]byte("eight provider nodes")),
		StateReadbackSHA256:    fleetcreate.SHA256([]byte("eight state instances")),
		StateLineageSHA256:     fleetcreate.SHA256([]byte("state-lineage")), StateSerial: 4,
		ExpectedNodeCount: 9, ObservedNodeCount: 8, RemainderSHA256: fleetcreate.SHA256([]byte("ninth-node-remainder"))}
	readback.StateLineageSHA256 = intent.StateLineageSHA256
	if err := h.s.RecordFleetCreateRecoveryReadback(h.ctx(), readback, "wrong-verifier"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong verifier = %v", err)
	}
	failWrite := true
	h.s.kv = fleetCreateAmbiguousKV{KV: h.s.kv, failWrite: &failWrite}
	if err := h.s.RecordFleetCreateRecoveryReadback(h.ctx(), readback, fleetCreateVerifierSubject); err != nil || failWrite {
		t.Fatalf("record recovery = %v", err)
	}
	if err := h.s.RecordFleetCreateRecoveryReadback(h.ctx(), readback, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("exact readback replay = %v", err)
	}
	recoveryRequest = fleetCreateStartForIntent(intent, uuid.NewString())
	failWrite = true
	started, err := h.s.StartFleetCreateRecoveryAttempt(h.ctx(), recoveryRequest, request.AttemptID, fleetCreateAttemptSubject)
	if err != nil || failWrite || started.Generation != 2 || started.AttemptID != recoveryRequest.AttemptID {
		t.Fatalf("successor=%+v err=%v", started, err)
	}
	replayed, err := h.s.StartFleetCreateRecoveryAttempt(h.ctx(), recoveryRequest, request.AttemptID, fleetCreateAttemptSubject)
	if err != nil || replayed != started {
		t.Fatalf("recovery replay=%+v err=%v", replayed, err)
	}
	priorAttempt, err := h.s.GetFleetCreateAttempt(h.ctx(), readback.Cluster, request.IntentID, request.AttemptID)
	if err != nil || priorAttempt.AttemptID != request.AttemptID || priorAttempt.Generation != 1 {
		t.Fatalf("prior attempt readback=%+v err=%v", priorAttempt, err)
	}
	priorAdmission, err := h.s.GetFleetCreateAdmission(h.ctx(), readback.Cluster, request.IntentID, request.AttemptID)
	if err != nil || priorAdmission.AttemptID != request.AttemptID {
		t.Fatalf("prior admission readback=%+v err=%v", priorAdmission, err)
	}
	nextAdmission := fleetCreateAdmissionForAttempt(intent, started)
	nextAdmission.ObservedNodeCount = 8
	nextAdmission.StateSerial = readback.StateSerial
	nextAdmission.RemainderSHA256 = readback.RemainderSHA256
	nextAdmission.ProviderReadbackSHA256 = readback.ProviderReadbackSHA256
	nextAdmission.StateReadbackSHA256 = readback.StateReadbackSHA256
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), nextAdmission, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("successor admission=%v", err)
	}
	nextRequest := fleetCreateRequestForAdmission(nextAdmission)
	if _, err := h.s.IssueFleetCreateEffectGrant(h.ctx(), nextRequest); err != nil {
		t.Fatalf("successor grant issue=%v", err)
	}
	if err := h.s.RecordFleetCreateRecoveryReadback(h.ctx(), readback, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("stale readback after successor = %v", err)
	}
}

func TestFleetCreateIntentDurablyClaimsUnregisteredCluster(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-intent"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, "wrong-verifier"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong principal = %v", err)
	}
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("exact retry = %v", err)
	}
	readback, err := h.s.GetFleetCreateIntent(h.ctx(), intent.Cluster)
	if err != nil || readback != intent {
		t.Fatalf("intent readback=%+v err=%v", readback, err)
	}
	changed := intent
	changed.TopologySHA256 = fleetcreate.SHA256([]byte("substituted-topology"))
	if err := h.s.RecordFleetCreateIntent(h.ctx(), changed, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("changed intent = %v", err)
	}
	identity := lifecycle.TargetIdentity{Provider: "digitalocean", ProviderAccount: "account", StateBackend: "spaces://bucket/state"}
	if _, err := h.s.RegisterFleetTarget(h.ctx(), identity, []string{"cluster:" + intent.Cluster}, uuid.NewString()); err == nil {
		t.Fatal("target registration bypassed the active pre-registration create fence")
	} else {
		var fenceErr *lifecycle.FenceError
		if !errors.As(err, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
			t.Fatalf("registration error = %v", err)
		}
	}
}

func TestFleetCreateInitialAttemptIsImmutableAndOneUse(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-attempt"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	request := fleetCreateStartForIntent(intent, uuid.NewString())
	if _, err := h.s.StartFleetCreateAttempt(h.ctx(), request, "wrong-runner"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong principal = %v", err)
	}
	if _, err := h.s.StartFleetCreateAttempt(h.ctx(), request, fleetCreateAttemptSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("attempt started without protected dispatch binding = %v", err)
	}
	dispatch := fleetCreateDispatchForIntent(intent)
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), dispatch, "wrong-verifier"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong dispatch verifier = %v", err)
	}
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), dispatch, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), dispatch, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("exact dispatch replay = %v", err)
	}
	dispatchReadback, err := h.s.GetFleetCreateDispatch(h.ctx(), intent.IntentID)
	if err != nil || dispatchReadback != dispatch {
		t.Fatalf("dispatch readback=%+v err=%v", dispatchReadback, err)
	}
	changedDispatch := dispatch
	changedDispatch.RunAttempt = "2"
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), changedDispatch, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("changed dispatch binding = %v", err)
	}
	started, err := h.s.StartFleetCreateAttempt(h.ctx(), request, fleetCreateAttemptSubject)
	if err != nil || started.Status != "admitted" || started.Generation != 1 {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	if err := h.s.RecordFleetCreateDispatch(h.ctx(), dispatch, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("exact dispatch replay after attempt start = %v", err)
	}
	replayed, err := h.s.StartFleetCreateAttempt(h.ctx(), request, fleetCreateAttemptSubject)
	if err != nil || replayed != started {
		t.Fatalf("exact replay=%+v err=%v", replayed, err)
	}
	readback, err := h.s.GetFleetCreateAttempt(h.ctx(), intent.Cluster, intent.IntentID, request.AttemptID)
	if err != nil || readback != started {
		t.Fatalf("attempt readback=%+v err=%v", readback, err)
	}
	changed := request
	changed.DispatchReceiptSHA256 = fleetcreate.SHA256([]byte("substituted-dispatch"))
	if _, err := h.s.StartFleetCreateAttempt(h.ctx(), changed, fleetCreateAttemptSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("same attempt ID with changed dispatch = %v", err)
	}
	successor := fleetCreateStartForIntent(intent, uuid.NewString())
	if _, err := h.s.StartFleetCreateAttempt(h.ctx(), successor, fleetCreateAttemptSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("second initial attempt = %v", err)
	}
}

func TestFleetCreateInitialAttemptCASAllowsOneRunner(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-attempt-cas"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindFleetCreateDispatch(t, h, intent)
	requests := []fleetcreate.AttemptStartRequest{
		fleetCreateStartForIntent(intent, uuid.NewString()),
		fleetCreateStartForIntent(intent, uuid.NewString()),
	}
	var wg sync.WaitGroup
	results := make([]error, len(requests))
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = h.s.StartFleetCreateAttempt(context.Background(), requests[i], fleetCreateAttemptSubject)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrFleetCreateIntentRefused) {
			t.Fatalf("unexpected contender result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one initial attempt, got %d: %v", winners, results)
	}
}

func TestFleetCreateDispatchCASAllowsOneProtectedRun(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-dispatch-cas"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindings := []fleetcreate.DispatchBinding{fleetCreateDispatchForIntent(intent), fleetCreateDispatchForIntent(intent)}
	bindings[1].RunID = "101"
	bindings[1].DispatchReceiptSHA256 = fleetcreate.SHA256([]byte("another-verified-run"))
	var wg sync.WaitGroup
	results := make([]error, len(bindings))
	for i := range bindings {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.s.RecordFleetCreateDispatch(context.Background(), bindings[i], fleetCreateVerifierSubject)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrFleetCreateIntentRefused) {
			t.Fatalf("unexpected dispatch contender result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one protected run binding, got %d: %v", winners, results)
	}
	stored, err := h.s.GetFleetCreateDispatch(h.ctx(), intent.IntentID)
	if err != nil || (stored != bindings[0] && stored != bindings[1]) {
		t.Fatalf("stored dispatch=%+v err=%v", stored, err)
	}
}

func TestFleetCreateAdmissionIsBoundWriteOnceToAdmittedAttempt(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-admission"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindFleetCreateDispatch(t, h, intent)
	start := fleetCreateStartForIntent(intent, uuid.NewString())
	attempt, err := h.s.StartFleetCreateAttempt(h.ctx(), start, fleetCreateAttemptSubject)
	if err != nil {
		t.Fatal(err)
	}
	admission := fleetCreateAdmissionForAttempt(intent, attempt)
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), admission, "wrong-verifier"); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("wrong principal = %v", err)
	}
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), admission, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), admission, fleetCreateVerifierSubject); err != nil {
		t.Fatalf("exact retry = %v", err)
	}
	readback, err := h.s.GetFleetCreateAdmission(h.ctx(), intent.Cluster, intent.IntentID, attempt.AttemptID)
	if err != nil || readback != admission {
		t.Fatalf("admission readback=%+v err=%v", readback, err)
	}
	changed := admission
	changed.RemainderSHA256 = fleetcreate.SHA256([]byte("substituted-remainder"))
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), changed, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("changed remainder = %v", err)
	}
	changed = admission
	changed.AttemptID = uuid.NewString()
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), changed, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("unadmitted attempt = %v", err)
	}
}

func TestFleetCreateAdmissionRefusesStateLineageDrift(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-admission-lineage"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindFleetCreateDispatch(t, h, intent)
	attempt, err := h.s.StartFleetCreateAttempt(h.ctx(), fleetCreateStartForIntent(intent, uuid.NewString()), fleetCreateAttemptSubject)
	if err != nil {
		t.Fatal(err)
	}
	admission := fleetCreateAdmissionForAttempt(intent, attempt)
	admission.StateLineageSHA256 = fleetcreate.SHA256([]byte("other-state-lineage"))
	if err := h.s.RecordFleetCreateAdmission(h.ctx(), admission, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("state lineage drift = %v", err)
	}
	if _, err := h.s.GetFleetCreateAdmission(h.ctx(), intent.Cluster, intent.IntentID, attempt.AttemptID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused admission was persisted: %v", err)
	}
}

func TestFleetCreateAdmissionCASAllowsOnlyOneRemainder(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-admission-cas"})
	intent := fleetCreateIntentForPlan(plan)
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); err != nil {
		t.Fatal(err)
	}
	bindFleetCreateDispatch(t, h, intent)
	attempt, err := h.s.StartFleetCreateAttempt(h.ctx(), fleetCreateStartForIntent(intent, uuid.NewString()), fleetCreateAttemptSubject)
	if err != nil {
		t.Fatal(err)
	}
	admissions := []fleetcreate.Admission{fleetCreateAdmissionForAttempt(intent, attempt), fleetCreateAdmissionForAttempt(intent, attempt)}
	admissions[1].RemainderSHA256 = fleetcreate.SHA256([]byte("different-remainder"))
	var wg sync.WaitGroup
	results := make([]error, len(admissions))
	for i := range admissions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.s.RecordFleetCreateAdmission(context.Background(), admissions[i], fleetCreateVerifierSubject)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrFleetCreateIntentRefused) {
			t.Fatalf("unexpected admission contender result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one admitted remainder, got %d: %v", winners, results)
	}
	stored, err := h.s.GetFleetCreateAdmission(h.ctx(), intent.Cluster, intent.IntentID, attempt.AttemptID)
	if err != nil || (stored != admissions[0] && stored != admissions[1]) {
		t.Fatalf("stored admission=%+v err=%v", stored, err)
	}
}

func TestFleetCreateIntentRejectsPlanDriftAndRegisteredCluster(t *testing.T) {
	for _, test := range []struct {
		name       string
		suffix     string
		mutate     func(*fleetcreate.Intent)
		registered bool
	}{
		{name: "commit drift", suffix: "commit-drift", mutate: func(i *fleetcreate.Intent) { i.CommitSHA = fmt.Sprintf("%040x", 9) }},
		{name: "plan digest drift", suffix: "plan-digest-drift", mutate: func(i *fleetcreate.Intent) { i.PlanSHA256 = fleetcreate.SHA256([]byte("other-plan")) }},
		{name: "already registered", suffix: "already-registered", registered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEtcdExecutorHarness(t)
			plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-" + test.suffix})
			intent := fleetCreateIntentForPlan(plan)
			if test.mutate != nil {
				test.mutate(&intent)
			}
			if test.registered {
				h.RegisterTarget(intent.Cluster)
			}
			if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
				t.Fatalf("record mismatched/registered intent = %v", err)
			}
		})
	}
}

func TestFleetCreateIntentRequiresOperationIDToMatchEtcdKey(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-plan-key"})
	intent := fleetCreateIntentForPlan(plan)
	record, _, err := h.s.load(h.ctx(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Operation.ID = uuid.NewString()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.opKey(plan.ID), string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordFleetCreateIntent(h.ctx(), intent, fleetCreateVerifierSubject); !errors.Is(err, ErrFleetCreateIntentRefused) {
		t.Fatalf("operation ID/key mismatch = %v", err)
	}
}

func TestFleetCreateIntentClusterCASAllowsOneCompetingIntent(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	firstPlan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-cas"})
	secondPlan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-cas"})
	intents := []fleetcreate.Intent{fleetCreateIntentForPlan(firstPlan), fleetCreateIntentForPlan(secondPlan)}
	var wg sync.WaitGroup
	results := make([]error, len(intents))
	for i := range intents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.s.RecordFleetCreateIntent(context.Background(), intents[i], fleetCreateVerifierSubject)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrFleetCreateIntentRefused) {
			t.Fatalf("unexpected contender result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one cluster-fence winner, got %d: %v", winners, results)
	}
}

func TestFleetCreateIntentCASSerializesAgainstTargetRegistration(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "fresh-create-register-race"})
	intent := fleetCreateIntentForPlan(plan)
	identity := lifecycle.TargetIdentity{Provider: "digitalocean", ProviderAccount: "account", StateBackend: "spaces://bucket/state"}
	var wg sync.WaitGroup
	var intentErr, registrationErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		intentErr = h.s.RecordFleetCreateIntent(context.Background(), intent, fleetCreateVerifierSubject)
	}()
	go func() {
		defer wg.Done()
		_, registrationErr = h.s.RegisterFleetTarget(context.Background(), identity,
			[]string{"cluster:" + intent.Cluster}, uuid.NewString())
	}()
	wg.Wait()
	if (intentErr == nil) == (registrationErr == nil) {
		t.Fatalf("want exactly one winning lifecycle claim: intent=%v registration=%v", intentErr, registrationErr)
	}
	if intentErr != nil && !errors.Is(intentErr, ErrFleetCreateIntentRefused) {
		t.Fatalf("unexpected intent result: %v", intentErr)
	}
	if registrationErr != nil {
		var fenceErr *lifecycle.FenceError
		if !errors.As(registrationErr, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
			t.Fatalf("unexpected registration result: %v", registrationErr)
		}
	}
	_, lockErr := h.s.GetFleetCreateIntent(context.Background(), intent.Cluster)
	_, registryEmpty, _, resolveErr := h.s.ResolveFleetTargetForPlan(context.Background(), intent.Cluster, "")
	targetExists := resolveErr == nil && !registryEmpty
	if lockErr == nil && targetExists {
		t.Fatal("cluster is simultaneously create-fenced and target-registered")
	}
}
