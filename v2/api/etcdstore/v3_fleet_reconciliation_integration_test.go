package etcdstore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func fleetReconciliationAcceptance(t *testing.T, adapter *etcdstore.V3OperationStore, plan model.Operation, attempt fleet.RunnerAttempt, key, phase string) store.OperationAcceptance {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: phase, Status: "succeeded",
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, AttemptID: attempt.ID,
		EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
	}
	payload := map[string]interface{}{
		"schemaVersion": request.SchemaVersion, "phase": request.Phase, "status": request.Status,
		"commitSha": request.CommitSHA, "planSha256": request.PlanSHA256, "attemptId": request.AttemptID,
		"evidenceDigest": request.EvidenceDigest,
	}
	acceptance := store.OperationAcceptance{
		Identity:            store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:evidence"}, Kind: "fleet.reconciliation", Resource: plan.ID, Key: key},
		Operation:           model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: plan.ID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}},
		Audit:               store.AcceptanceAuditContext{Source: "test"},
		Semantics:           map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": request},
		FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: attempt.ID, RequireActiveAttempt: true, RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	return acceptance
}

func TestV3FleetReconciliationAcceptanceReplayAndPhaseEvidenceEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	created, err := adapter.Accept(context.Background(), fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "attempt", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	attempt := *created.FleetRunnerAttempt
	if attempt.CurrentPhase != "prechange_verified" {
		t.Fatalf("initial phase=%q", attempt.CurrentPhase)
	}
	prechange := fleetReconciliationAcceptance(t, adapter, plan, attempt, "prechange", "prechange_verified")
	first, err := adapter.Accept(context.Background(), prechange)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := adapter.UpdateFleetRunnerAttempt(context.Background(), plan.ID, attempt.ID, attempt.Revision, "advance", "provider_applying")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := adapter.Accept(context.Background(), prechange)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != first.Operation.ID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	provider := fleetReconciliationAcceptance(t, adapter, plan, *advanced, "provider", "provider_applying")
	if _, err := adapter.Accept(context.Background(), provider); err != nil {
		t.Fatalf("phase transition evidence rejected: %v", err)
	}
}

func TestV3FleetReconciliationRefusesWrongAttemptPhaseAndEvidenceEtcd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*store.OperationAcceptance)
	}{
		{name: "attempt", mutate: func(a *store.OperationAcceptance) {
			a.FleetReconciliation.AttemptID = uuid.NewString()
			a.Operation.Payload["attemptId"] = a.FleetReconciliation.AttemptID
			request := a.Semantics["request"].(fleet.ReconciliationRequest)
			request.AttemptID = a.FleetReconciliation.AttemptID
			a.Semantics["request"] = request
		}},
		{name: "phase", mutate: func(a *store.OperationAcceptance) {
			a.Operation.Payload["phase"] = "provider_applying"
			request := a.Semantics["request"].(fleet.ReconciliationRequest)
			request.Phase = "provider_applying"
			a.Semantics["request"] = request
		}},
		{name: "evidence-binding", mutate: func(a *store.OperationAcceptance) {
			a.Operation.Payload["commitSha"] = strings.Repeat("e", 40)
			request := a.Semantics["request"].(fleet.ReconciliationRequest)
			request.CommitSHA = strings.Repeat("e", 40)
			a.Semantics["request"] = request
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, _, _ := fleetRunnerEtcdStore(t)
			plan := fleetRunnerPlan(t, adapter, "scale")
			nonce := bindFleetRunnerDispatch(t, adapter, plan)
			created, err := adapter.Accept(context.Background(), fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "attempt", "7", "apply", ""))
			if err != nil {
				t.Fatal(err)
			}
			acceptance := fleetReconciliationAcceptance(t, adapter, plan, *created.FleetRunnerAttempt, tc.name, "prechange_verified")
			tc.mutate(&acceptance)
			acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Accept(context.Background(), acceptance); !errors.Is(err, store.ErrFleetReconciliationAdmission) {
				t.Fatalf("err=%v", err)
			}
			if _, err := adapter.GetOperation(context.Background(), acceptance.Operation.ID); !errors.Is(err, etcdstore.ErrNotFound) {
				t.Fatalf("rejected evidence persisted operation: %v", err)
			}
		})
	}
}

func TestV3FleetReconciliationTwoAdapterRaceEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	created, err := adapter.Accept(context.Background(), fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "attempt", "7", "apply", ""))
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
	second, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	requests := []store.OperationAcceptance{
		fleetReconciliationAcceptance(t, adapter, plan, *created.FleetRunnerAttempt, "race-a", "prechange_verified"),
		fleetReconciliationAcceptance(t, second, plan, *created.FleetRunnerAttempt, "race-b", "prechange_verified"),
	}
	start, outcomes := make(chan struct{}), make(chan error, len(requests))
	var wait sync.WaitGroup
	for index, request := range requests {
		current := adapter
		if index == 1 {
			current = second
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
		} else if errors.Is(err, store.ErrFleetReconciliationAdmission) {
			rejected++
		} else {
			t.Fatalf("race error=%v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}
