package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/model"
)

func offlineTestBinding(planID, targetID string, now time.Time) offline.Binding {
	return offline.Binding{
		SchemaVersion: offline.BindingSchema, PlanID: planID, TargetID: targetID,
		Resource: offline.WorkResource, SourceCommit: strings.Repeat("a", 40),
		SavedPlanSHA256: strings.Repeat("b", 64), EffectSHA256: strings.Repeat("c", 64),
		Backend: offline.Backend{Bucket: "work-state", Key: "staging/terraform.tfstate", VersionID: "version-1",
			Lineage: "lineage-1", Serial: 22, RawSHA256: strings.Repeat("d", 64)},
		ProviderAccountUUID: "work-account", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID,
		OwnerEnvelopeSHA256: strings.Repeat("e", 64), RunnerPrincipalSubject: "offline-runner-1",
		DispatchNonceSHA256: strings.Repeat("f", 64), FenceGeneration: 1,
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute),
	}
}

func offlineTestPlan(h *etcdExecutorHarness, id string) {
	h.t.Helper()
	h.putOp(model.Operation{ID: id, Kind: "fleet.capacity-plan", Ref: offline.WorkResource,
		Payload: map[string]interface{}{"id": id, "cluster": "tk-staging-nyc3", "pool": "app",
			"action": "scale", "current": map[string]interface{}{"desired": 3},
			"proposed": map[string]interface{}{"desired": 2}}})
}

func offlineTestHarness(t *testing.T) (*etcdExecutorHarness, string) {
	t.Helper()
	h := newEtcdExecutorHarness(t)
	target, err := h.s.RegisterFleetTarget(context.Background(), lifecycle.TargetIdentity{
		Provider: "digitalocean", ProviderAccount: "work-account", StateBackend: "s3://work-state/staging/terraform.tfstate",
	}, []string{"cluster:tk-staging-nyc3", "environment:staging"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return h, target.TargetID
}

func TestOfflineDispatchReservationAcquiresSharedFenceAtomically(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	firstID, secondID := uuid.NewString(), uuid.NewString()
	offlineTestPlan(h, firstID)
	offlineTestPlan(h, secondID)
	first := offlineTestBinding(firstID, targetID, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, now); err != nil {
		t.Fatal(err)
	}
	stored, err := h.s.GetOfflineDispatchReservation(h.ctx(), firstID)
	if err != nil || !sameOfflineBinding(stored, first) {
		t.Fatalf("stored binding differs: %v", err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil || !fence.Held || fence.HolderPlanID != firstID ||
		fence.HolderNonceSHA256 != first.DispatchNonceSHA256 || fence.Generation != 1 {
		t.Fatalf("fence is not held by exact offline dispatch: %+v, %v", fence, err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, now); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	changed := first
	changed.EffectSHA256 = strings.Repeat("1", 64)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), changed, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	second := offlineTestBinding(secondID, targetID, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), second, now); err == nil {
		t.Fatal("second plan acquired occupied shared fence")
	}
	if _, err := h.s.GetOfflineDispatchReservation(h.ctx(), secondID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser persisted a reservation: %v", err)
	}
	// A stale reservation record cannot claim success after a release or
	// authority change. Replays must prove the shared fence still belongs to it.
	fence.Held, fence.HolderPlanID, fence.HolderNonceSHA256 = false, "", ""
	encoded, err := json.Marshal(fleetTargetFenceRecord(fence))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(targetID), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("released fence allowed reservation replay: %v", err)
	}
}

func TestOfflineDispatchRefusesExistingProtectedPreparationAndWrongPlan(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	b := offlineTestBinding(planID, targetID, now)
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID), `{"prepared":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("protected preparation did not block offline reservation: %v", err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil || fence.Held {
		t.Fatalf("failed reservation touched fence: %+v, %v", fence, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, now.Add(11*time.Minute)); !errors.Is(err, offline.ErrInvalidBinding) {
		t.Fatalf("expired binding accepted: %v", err)
	}
	wrong := b
	wrong.FenceGeneration = 2
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), wrong, now); err == nil {
		t.Fatal("wrong fence generation accepted")
	}
	if _, err := h.s.GetOfflineDispatchReservation(h.ctx(), planID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused binding persisted: %v", err)
	}
}

func TestOfflineDispatchConcurrentPlansOnlyOneCommits(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		offlineTestPlan(h, id)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			<-start
			_, results[i] = h.s.ReserveOfflineDispatch(h.ctx(), offlineTestBinding(id, targetID, now), now)
		}(i, id)
	}
	close(start)
	wg.Wait()
	success := 0
	for _, err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("want one atomic winner, got %d: %v", success, results)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil || !fence.Held || fence.Generation != 1 {
		t.Fatalf("wrong shared fence: %+v, %v", fence, err)
	}
	for _, id := range ids {
		_, err := h.s.GetOfflineDispatchReservation(h.ctx(), id)
		if (id == fence.HolderPlanID) != (err == nil) {
			t.Fatalf("reservation and fence disagree for %s: %v", id, err)
		}
	}
}

func TestOfflineReservationTransactionHasNoEffectGrant(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	offlineTestPlan(h, id)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), offlineTestBinding(id, targetID, now), now); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{h.s.fleetRunnerAttemptPrefix(id), h.s.fleetExecutorMetaKey(id, "any")} {
		response, err := h.s.kv.Get(h.ctx(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
		if err != nil || response.Count != 0 {
			t.Fatalf("reservation created attempt or grant: %s, %v", prefix, err)
		}
	}
}
