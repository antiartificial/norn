package etcdstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func fleetGitHubDispatchPreparation(planID string) etcdstore.FleetGitHubDispatchPreparation {
	return etcdstore.FleetGitHubDispatchPreparation{
		PlanID: planID, PlanRunID: 91, PlanSHA256: strings.Repeat("a", 64), ApprovedHeadSHA: strings.Repeat("b", 40),
		FleetEnvironment: "staging/nyc3", AllowDestructive: true,
	}
}

func TestV3FleetGitHubDispatchPreparationPersistsOpaqueNonceBeforeBindingEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	input := fleetGitHubDispatchPreparation(plan.ID)
	_, prepared, err := acceptFleetGitHubDispatch(t, adapter, &plan, input)
	created := err == nil
	if err != nil || !created || len(prepared.DispatchNonce) != 64 || prepared.DispatchNonceSHA256 == "" {
		t.Fatalf("prepared=%+v created=%v err=%v", prepared, created, err)
	}
	digest := sha256.Sum256([]byte(prepared.DispatchNonce))
	if prepared.DispatchNonceSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("nonce digest=%q", prepared.DispatchNonceSHA256)
	}
	_, recovered, err := acceptFleetGitHubDispatch(t, adapter, &plan, input)
	if err != nil || recovered != prepared {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	changed := input
	changed.PlanSHA256 = strings.Repeat("c", 64)
	if _, _, err := acceptFleetGitHubDispatch(t, adapter, &plan, changed); err == nil {
		t.Fatal("different approved plan reused private nonce")
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, strings.Repeat("0", 64), 93, "https://github.com/acme/norn-fleet/actions/runs/93"); err == nil {
		t.Fatal("wrong nonce digest bound workflow run")
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 93, "https://github.com/acme/norn-fleet/actions/runs/93"); err != nil {
		t.Fatal(err)
	}
	bound, err := adapter.GetFleetRunnerDispatchBinding(context.Background(), plan.ID)
	if err != nil || bound.PlanID != plan.ID || bound.PlanSHA256 != input.PlanSHA256 || bound.ApprovedHeadSHA != input.ApprovedHeadSHA || bound.DispatchNonceSHA256 != prepared.DispatchNonceSHA256 || bound.RunID != 93 {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	if err := adapter.VerifyFleetGitHubDispatchCompletion(context.Background(), bound); err != nil {
		t.Fatalf("signed terminal completion=%v", err)
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 93, "https://github.com/acme/norn-fleet/actions/runs/93"); err != nil {
		t.Fatalf("identical binding replay=%v", err)
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 94, "https://github.com/acme/norn-fleet/actions/runs/94"); err == nil {
		t.Fatal("bound workflow run was overwritten")
	}
	stored, err := client.Get(context.Background(), prefix+"/v3/operations/"+prepared.OperationID)
	if err != nil || len(stored.Kvs) != 1 {
		t.Fatalf("load terminal receipt: %v", err)
	}
	var tampered map[string]interface{}
	if err := json.Unmarshal(stored.Kvs[0].Value, &tampered); err != nil {
		t.Fatal(err)
	}
	completion := tampered["operation"].(map[string]interface{})["metadata"].(map[string]interface{})["fleetGitHubCompletion"].(map[string]interface{})
	completion["signature"] = "00"
	encoded, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(context.Background(), prefix+"/v3/operations/"+prepared.OperationID, string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyFleetGitHubDispatchCompletion(context.Background(), bound); err == nil {
		t.Fatal("tampered signed terminal completion verified")
	}
	if _, _, err := adapter.PrepareFleetGitHubDispatch(context.Background(), input); err == nil {
		t.Fatalf("bound dispatch prepared again: %v", err)
	}
}

func TestV3FleetGitHubDispatchPreparationHasOneConcurrentNonceEtcd(t *testing.T) {
	adapter, client, prefix := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	second, err := etcdstore.NewV3OperationStore(client, prefix, mustFleetRunnerAuthority(t, adapter), mustFleetRunnerSigner(t))
	if err != nil {
		t.Fatal(err)
	}
	input := fleetGitHubDispatchPreparation(plan.ID)
	type result struct {
		prepared etcdstore.FleetGitHubDispatchPreparation
		created  bool
		err      error
	}
	results := make([]result, 2)
	stores := []*etcdstore.V3OperationStore{adapter, second}
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range stores {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			_, results[i].prepared, results[i].err = acceptFleetGitHubDispatch(t, stores[i], &plan, input)
			results[i].created = results[i].err == nil
		}(i)
	}
	close(start)
	group.Wait()
	created := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
	}
	if created != 2 || results[0].prepared.DispatchNonce != results[1].prepared.DispatchNonce || results[0].prepared.DispatchNonceSHA256 != results[1].prepared.DispatchNonceSHA256 {
		t.Fatalf("concurrent preparations=%+v", results)
	}
}

func acceptFleetGitHubDispatch(t *testing.T, adapter *etcdstore.V3OperationStore, plan *model.Operation, prepared etcdstore.FleetGitHubDispatchPreparation) (store.AcceptedOperation, etcdstore.FleetGitHubDispatchPreparation, error) {
	t.Helper()
	encoded, _ := json.Marshal(plan.Payload)
	var capacity fleet.CapacityPlan
	if err := json.Unmarshal(encoded, &capacity); err != nil {
		t.Fatal(err)
	}
	authority := mustFleetRunnerAuthority(t, adapter)
	now := plan.StartedAt
	payload := map[string]interface{}{"planId": plan.ID, "planDigest": capacity.Digest, "sourceDigest": capacity.SourceDigest, "planRunId": prepared.PlanRunID, "planSha256": prepared.PlanSHA256, "approvedHeadSha": prepared.ApprovedHeadSHA, "fleetEnvironment": prepared.FleetEnvironment, "allowDestructive": prepared.AllowDestructive}
	operation := model.Operation{ID: "00000000-0000-4000-8000-000000000001", Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"fleetGitHub": payload}, Metadata: map[string]interface{}{}}
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: plan.ID}, Kind: operation.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: operation, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": payload}}
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.AcceptFleetGitHubDispatch(context.Background(), request, prepared)
}

func mustFleetRunnerAuthority(t *testing.T, adapter *etcdstore.V3OperationStore) string {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func mustFleetRunnerSigner(t *testing.T) *store.HMACAcceptanceSigner {
	t.Helper()
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-runner-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
