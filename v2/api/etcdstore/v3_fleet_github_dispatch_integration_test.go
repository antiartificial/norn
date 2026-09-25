package etcdstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
)

func fleetGitHubDispatchPreparation(planID string) etcdstore.FleetGitHubDispatchPreparation {
	return etcdstore.FleetGitHubDispatchPreparation{
		PlanID: planID, PlanRunID: 91, PlanSHA256: strings.Repeat("a", 64), ApprovedHeadSHA: strings.Repeat("b", 40),
		FleetEnvironment: "staging/nyc3", AllowDestructive: true,
	}
}

func TestV3FleetGitHubDispatchPreparationPersistsOpaqueNonceBeforeBindingEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetRunnerPlan(t, adapter, "scale")
	input := fleetGitHubDispatchPreparation(plan.ID)
	prepared, created, err := adapter.PrepareFleetGitHubDispatch(context.Background(), input)
	if err != nil || !created || len(prepared.DispatchNonce) != 64 || prepared.DispatchNonceSHA256 == "" {
		t.Fatalf("prepared=%+v created=%v err=%v", prepared, created, err)
	}
	digest := sha256.Sum256([]byte(prepared.DispatchNonce))
	if prepared.DispatchNonceSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("nonce digest=%q", prepared.DispatchNonceSHA256)
	}
	recovered, created, err := adapter.PrepareFleetGitHubDispatch(context.Background(), input)
	if err != nil || created || recovered != prepared {
		t.Fatalf("recovered=%+v created=%v err=%v", recovered, created, err)
	}
	changed := input
	changed.PlanSHA256 = strings.Repeat("c", 64)
	if _, _, err := adapter.PrepareFleetGitHubDispatch(context.Background(), changed); err == nil {
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
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 93, "https://github.com/acme/norn-fleet/actions/runs/93"); err != nil {
		t.Fatalf("identical binding replay=%v", err)
	}
	if err := adapter.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 94, "https://github.com/acme/norn-fleet/actions/runs/94"); err == nil {
		t.Fatal("bound workflow run was overwritten")
	}
	if _, _, err := adapter.PrepareFleetGitHubDispatch(context.Background(), input); !errors.Is(err, etcdstore.ErrFleetGitHubDispatchBound) {
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
			results[i].prepared, results[i].created, results[i].err = stores[i].PrepareFleetGitHubDispatch(context.Background(), input)
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
	if created != 1 || results[0].prepared.DispatchNonce != results[1].prepared.DispatchNonce || results[0].prepared.DispatchNonceSHA256 != results[1].prepared.DispatchNonceSHA256 {
		t.Fatalf("concurrent preparations=%+v", results)
	}
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
