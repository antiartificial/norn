package etcdstore

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

var offlineTestPrivateKey = ed25519.NewKeyFromSeed([]byte("01234567890123456789012345678901"))

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

func offlineTestApproval(t *testing.T, binding offline.Binding, now time.Time) (offline.Binding, offline.VerifiedApproval) {
	t.Helper()
	id := sha256.Sum256([]byte(binding.PlanID))
	return offlineTestApprovalID(t, binding, now, hex.EncodeToString(id[:]))
}

func offlineTestApprovalID(t *testing.T, binding offline.Binding, now time.Time, authorizationID string) (offline.Binding, offline.VerifiedApproval) {
	t.Helper()
	binding.OwnerAuthorizationID = authorizationID
	intent := offline.ApprovalIntent{SchemaVersion: offline.ApprovalSchema, AuthorizationID: authorizationID,
		AllowDestructive: true, PlanID: binding.PlanID, TargetID: binding.TargetID, Resource: binding.Resource,
		SourceCommit: binding.SourceCommit, SavedPlanSHA256: binding.SavedPlanSHA256,
		EffectSHA256: binding.EffectSHA256, Backend: binding.Backend,
		ProviderAccountUUID: binding.ProviderAccountUUID, NodeName: binding.NodeName,
		DropletID: binding.DropletID, RunnerPrincipalSubject: binding.RunnerPrincipalSubject,
		DispatchNonceSHA256: binding.DispatchNonceSHA256, FenceGeneration: binding.FenceGeneration,
		ExpiresAt: binding.ExpiresAt}
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	signed := offline.SignedApproval{Intent: intent, KeyID: "pinned-owner", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(offlineTestPrivateKey, intentBytes))}
	envelope, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(envelope)
	binding.OwnerEnvelopeSHA256 = hex.EncodeToString(digest[:])
	verified, err := offline.VerifySignedApproval(signed, "pinned-owner", offlineTestPrivateKey.Public().(ed25519.PublicKey), binding, now)
	if err != nil {
		t.Fatal(err)
	}
	return binding, verified
}

func offlineAttemptClaim(binding offline.Binding) offline.AttemptClaim {
	return offline.AttemptClaim{PlanID: binding.PlanID, TargetID: binding.TargetID,
		Resource: binding.Resource, SourceCommit: binding.SourceCommit,
		SavedPlanSHA256: binding.SavedPlanSHA256, EffectSHA256: binding.EffectSHA256,
		Backend: binding.Backend, ProviderAccountUUID: binding.ProviderAccountUUID,
		NodeName: binding.NodeName, DropletID: binding.DropletID,
		OwnerAuthorizationID: binding.OwnerAuthorizationID, OwnerEnvelopeSHA256: binding.OwnerEnvelopeSHA256,
		RunnerPrincipalSubject: binding.RunnerPrincipalSubject,
		DispatchNonceSHA256:    binding.DispatchNonceSHA256, FenceGeneration: binding.FenceGeneration}
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
	first, verified := offlineTestApproval(t, first, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, verified, now); err != nil {
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
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, verified, now); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	changed := first
	changed.EffectSHA256 = strings.Repeat("1", 64)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), changed, verified, now); !errors.Is(err, offline.ErrInvalidBinding) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	second := offlineTestBinding(secondID, targetID, now)
	second, secondApproval := offlineTestApproval(t, second, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), second, secondApproval, now); err == nil {
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
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, verified, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("released fence allowed reservation replay: %v", err)
	}
}

func TestOfflineDispatchRefusesExistingProtectedPreparationAndWrongPlan(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	b := offlineTestBinding(planID, targetID, now)
	b, approval := offlineTestApproval(t, b, now)
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID), `{"prepared":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, approval, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("protected preparation did not block offline reservation: %v", err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil || fence.Held {
		t.Fatalf("failed reservation touched fence: %+v, %v", fence, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, approval, now.Add(11*time.Minute)); !errors.Is(err, offline.ErrInvalidBinding) {
		t.Fatalf("expired binding accepted: %v", err)
	}
	wrong := b
	wrong.FenceGeneration = 2
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), wrong, approval, now); err == nil {
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
	bindings := make([]offline.Binding, len(ids))
	approvals := make([]offline.VerifiedApproval, len(ids))
	for i, id := range ids {
		bindings[i], approvals[i] = offlineTestApproval(t, offlineTestBinding(id, targetID, now), now)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			<-start
			_, results[i] = h.s.ReserveOfflineDispatch(h.ctx(), bindings[i], approvals[i], now)
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

func TestOfflineOwnerAuthorizationIsConsumedOnceAcrossPlans(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	firstID, secondID := uuid.NewString(), uuid.NewString()
	offlineTestPlan(h, firstID)
	offlineTestPlan(h, secondID)
	ownerID := strings.Repeat("9", 64)
	first, firstApproval := offlineTestApprovalID(t, offlineTestBinding(firstID, targetID, now), now, ownerID)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), first, firstApproval, now); err != nil {
		t.Fatal(err)
	}
	consumed, err := h.s.kv.Get(h.ctx(), h.s.offlineOwnerConsumptionKey(ownerID))
	if err != nil || len(consumed.Kvs) != 1 || string(consumed.Kvs[0].Value) != firstID+":"+first.OwnerEnvelopeSHA256 {
		t.Fatalf("owner authorization was not durably consumed: %v", err)
	}
	// Simulate an independently proved non-submission release. This store
	// slice intentionally exposes no release API; the second valid signature
	// must still fail because the same authorization ID was already consumed.
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	fence.Held, fence.HolderPlanID, fence.HolderNonceSHA256 = false, "", ""
	fence.LastRelease = &lifecycle.Release{PlanID: firstID, Reason: "dispatch_not_submitted", At: now}
	fence.Revision++
	encoded, err := json.Marshal(fleetTargetFenceRecord(fence))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(targetID), string(encoded)); err != nil {
		t.Fatal(err)
	}
	second := offlineTestBinding(secondID, targetID, now)
	second.FenceGeneration = 2
	second, secondApproval := offlineTestApprovalID(t, second, now, ownerID)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), second, secondApproval, now); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("reused owner authorization was accepted: %v", err)
	}
	if _, err := h.s.GetOfflineDispatchReservation(h.ctx(), secondID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reused owner authorization persisted another reservation: %v", err)
	}
}

func TestOfflineReservationTransactionHasNoEffectGrant(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	offlineTestPlan(h, id)
	b, approval := offlineTestApproval(t, offlineTestBinding(id, targetID, now), now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, approval, now); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{h.s.fleetRunnerAttemptPrefix(id), h.s.fleetExecutorMetaKey(id, "any")} {
		response, err := h.s.kv.Get(h.ctx(), prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
		if err != nil || response.Count != 0 {
			t.Fatalf("reservation created attempt or grant: %s, %v", prefix, err)
		}
	}
}

func TestOfflineAttemptAdmissionRequiresExactHeldFenceAndOwnerConsumption(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	offlineTestPlan(h, id)
	binding, approval := offlineTestApproval(t, offlineTestBinding(id, targetID, now), now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), binding, approval, now); err != nil {
		t.Fatal(err)
	}
	claim := offlineAttemptClaim(binding)
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, "wrong-principal", uuid.NewString(), now); !errors.Is(err, offline.ErrAttemptMismatch) {
		t.Fatalf("wrong principal admitted: %v", err)
	}
	wrong := claim
	wrong.Backend.RawSHA256 = strings.Repeat("1", 64)
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), wrong, claim.RunnerPrincipalSubject, uuid.NewString(), now); !errors.Is(err, offline.ErrAttemptMismatch) {
		t.Fatalf("changed backend admitted: %v", err)
	}
	consumptionKey := h.s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	if _, err := h.s.kv.Delete(h.ctx(), consumptionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, claim.RunnerPrincipalSubject, uuid.NewString(), now); !errors.Is(err, offline.ErrAttemptMismatch) {
		t.Fatalf("attempt without owner consumption admitted: %v", err)
	}
	if _, err := h.s.kv.Put(h.ctx(), consumptionKey, binding.PlanID+":"+binding.OwnerEnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	fence.Held = false
	fence.HolderPlanID = ""
	fence.HolderNonceSHA256 = ""
	broken, err := json.Marshal(fleetTargetFenceRecord(fence))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(targetID), string(broken)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, claim.RunnerPrincipalSubject, uuid.NewString(), now); err == nil {
		t.Fatal("attempt without held shared fence admitted")
	}
	fence.Held = true
	fence.HolderPlanID = id
	fence.HolderNonceSHA256 = binding.DispatchNonceSHA256
	restored, err := json.Marshal(fleetTargetFenceRecord(fence))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(targetID), string(restored)); err != nil {
		t.Fatal(err)
	}
	attemptID := uuid.NewString()
	attempt, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, claim.RunnerPrincipalSubject, attemptID, now)
	if err != nil || attempt.ID != attemptID || attempt.CurrentPhase != "prechange_verified" || attempt.Status != "queued" {
		t.Fatalf("exact attempt refused or malformed: %+v, %v", attempt, err)
	}
	stored, err := h.s.GetOfflineAttempt(h.ctx(), id)
	if err != nil || stored != attempt {
		t.Fatalf("stored attempt differs: %+v, %v", stored, err)
	}
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, claim.RunnerPrincipalSubject, uuid.NewString(), now); !errors.Is(err, ErrOfflineAttemptConflict) {
		t.Fatalf("second attempt admitted: %v", err)
	}
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), claim, claim.RunnerPrincipalSubject, uuid.NewString(), binding.ExpiresAt); !errors.Is(err, offline.ErrAttemptMismatch) {
		t.Fatalf("expired attempt admitted: %v", err)
	}
}

func TestOfflineHolderOccupancyAndGenericRecoveryFailClosed(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	offlineTestPlan(h, id)
	binding, approval := offlineTestApproval(t, offlineTestBinding(id, targetID, now), now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), binding, approval, now); err != nil {
		t.Fatal(err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineReserved {
		t.Fatalf("reserved occupancy = %s/%s, %v", state, reason, err)
	}
	resourceHolder, _, _, err := h.s.fleetResourceHolderFacts(h.ctx(), id)
	if err != nil || !resourceHolder.OfflineReservation || resourceHolder.OfflineEvidenceIncomplete || resourceHolder.OfflineQueuedAttempt {
		t.Fatalf("resource holder omitted offline reservation: %+v, %v", resourceHolder, err)
	}
	if _, err := h.s.planFleetTargetRelease(h.ctx(), targetID, 1, lifecycle.ReleaseModeDispatchNotSubmitted,
		nil, uuid.NewString(), now); !errors.Is(err, lifecycle.ErrFleetTargetOfflineRecoveryRequired) {
		t.Fatalf("generic not-submitted release accepted offline holder: %v", err)
	}
	if _, err := h.s.AdmitOfflineAttempt(h.ctx(), offlineAttemptClaim(binding), binding.RunnerPrincipalSubject,
		uuid.NewString(), now); err != nil {
		t.Fatal(err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineAttemptQueued {
		t.Fatalf("queued occupancy = %s/%s, %v", state, reason, err)
	}
	resourceHolder, _, oldCompares, err := h.s.fleetResourceHolderFacts(h.ctx(), id)
	if err != nil || !resourceHolder.OfflineQueuedAttempt || resourceHolder.OfflineEvidenceIncomplete {
		t.Fatalf("resource holder omitted offline queued attempt: %+v, %v", resourceHolder, err)
	}
	attempt, err := h.s.GetOfflineAttempt(h.ctx(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineAttemptKey(id), "malformed"); err != nil {
		t.Fatal(err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineEvidenceIncomplete {
		t.Fatalf("corrupt attempt occupancy = %s/%s, %v", state, reason, err)
	}
	attemptBytes, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineAttemptKey(id), string(attemptBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)); err != nil {
		t.Fatal(err)
	}
	stale, err := h.s.kv.Txn(h.ctx()).If(oldCompares...).Then(clientv3.OpPut(h.s.prefix+"/stale-resource-commit", "bad")).Commit()
	if err != nil || stale.Succeeded {
		t.Fatalf("resource reconcile ignored changed owner consumption: %+v, %v", stale, err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineEvidenceIncomplete {
		t.Fatalf("missing owner proof occupancy = %s/%s, %v", state, reason, err)
	}
	if resourceHolder, _, _, err := h.s.fleetResourceHolderFacts(h.ctx(), id); err != nil || !resourceHolder.OfflineEvidenceIncomplete {
		t.Fatalf("resource holder treated missing owner proof as complete: %+v, %v", resourceHolder, err)
	}
	if _, err := h.s.planFleetTargetRelease(h.ctx(), targetID, 1, lifecycle.ReleaseModeAbandon,
		&lifecycle.TerminalProof{ListingSnapshotSHA256: strings.Repeat("a", 64)}, uuid.NewString(), now.Add(time.Hour)); !errors.Is(err, lifecycle.ErrFleetTargetOfflineRecoveryRequired) {
		t.Fatalf("generic abandon accepted incomplete offline holder: %v", err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID),
		binding.PlanID+":"+binding.OwnerEnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineDispatchKey(id), "malformed"); err != nil {
		t.Fatal(err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineEvidenceIncomplete {
		t.Fatalf("corrupt binding occupancy = %s/%s, %v", state, reason, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineDispatchKey(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineAttemptKey(id)); err != nil {
		t.Fatal(err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineEvidenceIncomplete {
		t.Fatalf("lost offline records occupancy = %s/%s, %v", state, reason, err)
	}
	if resourceHolder, _, _, err := h.s.fleetResourceHolderFacts(h.ctx(), id); err != nil || !resourceHolder.OfflineEvidenceIncomplete {
		t.Fatalf("resource holder missed lost offline records: %+v, %v", resourceHolder, err)
	}
	if _, err := h.s.planFleetTargetRelease(h.ctx(), targetID, 1, lifecycle.ReleaseModeDispatchNotSubmitted,
		nil, uuid.NewString(), now); !errors.Is(err, lifecycle.ErrFleetTargetOfflineRecoveryRequired) {
		t.Fatalf("generic release accepted holder after offline record loss: %v", err)
	}
}

func TestOfflineReservationBlocksTargetAliasChange(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	offlineTestPlan(h, id)
	binding, approval := offlineTestApproval(t, offlineTestBinding(id, targetID, now), now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), binding, approval, now); err != nil {
		t.Fatal(err)
	}
	_, err := h.s.RegisterFleetTarget(h.ctx(), lifecycle.TargetIdentity{
		Provider: "digitalocean", ProviderAccount: "work-account", StateBackend: "s3://work-state/staging/terraform.tfstate",
	}, []string{"cluster:tk-staging-nyc3", "environment:staging", "environment:staging-alt"}, uuid.NewString())
	var fenceErr *lifecycle.FenceError
	if !errors.As(err, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
		t.Fatalf("target alias changed under offline holder: %v", err)
	}
	// Simulate a partial restore: the immutable offline binding is missing,
	// but the authoritative shared fence still names this plan and target.
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineDispatchKey(id)); err != nil {
		t.Fatal(err)
	}
	_, err = h.s.RegisterFleetTarget(h.ctx(), lifecycle.TargetIdentity{
		Provider: "digitalocean", ProviderAccount: "work-account", StateBackend: "s3://work-state/staging/terraform.tfstate",
	}, []string{"cluster:tk-staging-nyc3", "environment:staging", "environment:staging-alt"}, uuid.NewString())
	if !errors.As(err, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
		t.Fatalf("target alias changed after offline binding loss: %v", err)
	}
	independent, err := h.s.RegisterFleetTarget(h.ctx(), lifecycle.TargetIdentity{
		Provider: "digitalocean", ProviderAccount: "other-account", StateBackend: "s3://other-state/independent.tfstate",
	}, []string{"cluster:independent", "environment:independent"}, uuid.NewString())
	if err != nil || independent == nil || independent.TargetID == targetID {
		t.Fatalf("unrelated target blocked by offline holder: %+v, %v", independent, err)
	}
}

func TestOfflineCheckpointCASAndCurrentFence(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	binding, approval := offlineTestApproval(t, offlineTestBinding(planID, targetID, now), now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), binding, approval, now); err != nil {
		t.Fatal(err)
	}
	attempt, err := h.s.AdmitOfflineAttempt(h.ctx(), offlineAttemptClaim(binding), binding.RunnerPrincipalSubject, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	evidence := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, id, principal, phase, digest string
		sequence                           uint64
	}{
		{"wrong runner", attempt.ID, "other", "prechange_observed", evidence, 0},
		{"wrong attempt", uuid.NewString(), binding.RunnerPrincipalSubject, "prechange_observed", evidence, 0},
		{"effect phase", attempt.ID, binding.RunnerPrincipalSubject, "provider_applying", evidence, 0},
		{"bad digest", attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", "bad", 0},
		{"wrong sequence", attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", evidence, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, tc.id, tc.principal, tc.phase, tc.digest, tc.sequence, now); err == nil {
				t.Fatal("invalid checkpoint admitted")
			}
		})
	}
	checkpoint, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", evidence, 0, now)
	if err != nil || checkpoint.Sequence != 1 || checkpoint.AuthorityEpoch != 1 {
		t.Fatalf("checkpoint: %+v, %v", checkpoint, err)
	}
	stored, err := h.s.GetOfflineCheckpoint(h.ctx(), planID)
	if err != nil || stored != checkpoint {
		t.Fatalf("stored checkpoint: %+v, %v", stored, err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID), `{"prepared":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", evidence, 0, now); !errors.Is(err, ErrOfflineCheckpointConflict) {
		t.Fatalf("protected-lane coexistence accepted: %v", err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(planID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", evidence, 0, now); !errors.Is(err, ErrOfflineCheckpointConflict) {
		t.Fatalf("replay accepted: %v", err)
	}
	if _, err := h.s.AdvanceFleetAuthorityEpoch(h.ctx(), 1, "test-fence"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, attempt.ID, binding.RunnerPrincipalSubject, "prechange_observed", evidence, 0, now); !errors.Is(err, ErrOfflineCheckpointConflict) {
		t.Fatalf("stale epoch accepted: %v", err)
	}
	stored, err = h.s.GetOfflineCheckpoint(h.ctx(), planID)
	if err != nil || stored != checkpoint {
		t.Fatalf("stale epoch changed checkpoint: %+v, %v", stored, err)
	}
}
