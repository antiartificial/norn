package etcdstore

// These phases run on either side of the real etcd snapshot/restore in
// test-etcd-three-member-fleet. A fixed prefix is required: copying keys to a
// different prefix would not exercise the recovery path used by Fleet.

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

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/store"
)

type offlineRestoreManifest struct {
	Authority       string `json:"authority"`
	PlanID          string `json:"planId"`
	TargetID        string `json:"targetId"`
	AuthorizationID string `json:"authorizationId"`
	AttemptID       string `json:"attemptId"`
	BindingSHA256   string `json:"bindingSHA256"`
	GrantID         string `json:"grantId"`
	RunnerSubject   string `json:"runnerSubject"`
	RunnerTokenID   string `json:"runnerTokenId"`
	RunnerDeviceID  string `json:"runnerDeviceId"`
}

func offlineRestoreBase() string {
	if strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_ENDPOINTS")) != "" {
		base := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_PREFIX"))
		if base == "" {
			base = "/norn-test"
		}
		return strings.TrimSuffix(base, "/") + "/offline-restore/"
	}
	return "/norn-test/offline-restore/"
}

func offlineRestoreStore(t *testing.T, client *clientv3.Client, authority string) *V3OperationStore {
	t.Helper()
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-offline-restore-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewV3OperationStore(client, offlineRestoreBase()+"store", authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestFleetOfflineRestoreEtcd(t *testing.T) {
	t.Run("seed", fleetOfflineRestoreSeed)
	t.Run("verify", fleetOfflineRestoreVerify)
}

func fleetOfflineRestoreSeed(t *testing.T) {
	client := integrationtest.EtcdClient(t)
	ctx := context.Background()
	if _, err := client.Delete(ctx, offlineRestoreBase(), clientv3.WithPrefix()); err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	h := &etcdExecutorHarness{t: t, s: offlineRestoreStore(t, client, authority)}
	target, err := h.s.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{
		Provider: "digitalocean", ProviderAccount: "work-account", StateBackend: "s3://work-state/staging/terraform.tfstate",
	}, []string{"cluster:tk-staging-nyc3", "environment:staging"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	auth := NewAuthStore(h.s.kv, h.s.prefix)
	deviceID, tokenID := uuid.NewString(), "norn_"+uuid.NewString()
	if err := auth.CreateAccessDevice(ctx, &store.AccessDevice{ID: deviceID, Name: "restore-offline-v2", Platform: "fleet-offline-runner", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	subject := "fleet-offline-device:" + deviceID
	if err := auth.RecordAccessToken(ctx, &store.AccessToken{JTI: tokenID, DeviceID: deviceID, Subject: subject, Scopes: []string{"fleet:offline-attempt"}, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	proof, err := auth.ProveOfflineRunner(ctx, subject, now)
	if err != nil {
		t.Fatal(err)
	}
	binding := offlineTestBinding(planID, target.TargetID, now)
	binding.SchemaVersion, binding.CandidateSHA256, binding.ProviderNodes = offline.BindingSchemaV2, strings.Repeat("9", 64), offlineProviderNodesV2()
	binding.RunnerPrincipalSubject = subject
	binding, approval := offlineTestApproval(t, binding, now)
	if _, err := h.s.ReserveOfflineDispatchWithRunnerProof(ctx, binding, approval, proof, now); err != nil {
		t.Fatal(err)
	}
	attempt, err := h.s.AdmitOfflineAttemptWithRunnerProof(ctx, offlineAttemptClaim(binding), proof, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	f := &offlineEffectV2Fixture{h: h, auth: auth, binding: binding, attempt: attempt, proof: proof, now: now}
	grant := f.issue(t)
	f.commit(t, grant)
	if _, err := h.s.DispatchOfflineDeleteV2(ctx, planID, attempt.ID, grant.ID, proof, f.effectObservation(now.Add(3*time.Second)), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	terminalAt := now.Add(4 * time.Second)
	terminal := offline.TerminalObservationV2{PlanID: planID, AttemptID: attempt.ID, GrantID: grant.ID, BindingSHA256: attempt.BindingSHA256,
		TargetID: binding.TargetID, TargetFenceGeneration: binding.FenceGeneration, AuthorityEpoch: 1, WriterFence: grant.WriterFence,
		ObservedAt: terminalAt, EvidenceSHA256: strings.Repeat("1", 64), ProviderEvidenceSHA256: strings.Repeat("2", 64),
		BackendEvidenceSHA256: strings.Repeat("3", 64), WorkloadEvidenceSHA256: strings.Repeat("4", 64), ProviderAbsent: true,
		BackendAdvanced: true, BackendTargetAbsent: true, AllNamespacesInspected: true, WorkloadTargetAbsent: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
	if _, err := h.s.RecordOfflineTerminalV2(ctx, planID, attempt.ID, grant.ID, proof, terminal, terminalAt); err != nil {
		t.Fatal(err)
	}
	manifest := offlineRestoreManifest{Authority: authority, PlanID: planID, TargetID: target.TargetID,
		AuthorizationID: binding.OwnerAuthorizationID, AttemptID: attempt.ID, BindingSHA256: attempt.BindingSHA256,
		GrantID: grant.ID, RunnerSubject: subject, RunnerTokenID: tokenID, RunnerDeviceID: deviceID}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, offlineRestoreBase()+"manifest", string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func fleetOfflineRestoreVerify(t *testing.T) {
	client := integrationtest.EtcdClient(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), offlineRestoreBase(), clientv3.WithPrefix()) })
	manifestResponse, err := client.Get(ctx, offlineRestoreBase()+"manifest")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestResponse.Kvs) != 1 {
		t.Fatal("offline restore seed absent")
	}
	var manifest offlineRestoreManifest
	if err := json.Unmarshal(manifestResponse.Kvs[0].Value, &manifest); err != nil {
		t.Fatal(err)
	}
	s := offlineRestoreStore(t, client, manifest.Authority)
	binding, err := s.GetOfflineDispatchReservation(ctx, manifest.PlanID)
	if err != nil || binding.TargetID != manifest.TargetID || binding.OwnerAuthorizationID != manifest.AuthorizationID {
		t.Fatalf("restored binding: %+v, %v", binding, err)
	}
	consumption, err := client.Get(ctx, s.offlineOwnerConsumptionKey(manifest.AuthorizationID))
	if err != nil || len(consumption.Kvs) != 1 || string(consumption.Kvs[0].Value) != binding.PlanID+":"+binding.OwnerEnvelopeSHA256 {
		t.Fatalf("restored one-use owner consumption missing or changed: %v", err)
	}
	attempt, err := s.GetOfflineAttempt(ctx, manifest.PlanID)
	if err != nil || attempt.ID != manifest.AttemptID || attempt.BindingSHA256 != manifest.BindingSHA256 {
		t.Fatalf("restored attempt: %+v, %v", attempt, err)
	}
	auth := NewAuthStore(s.kv, s.prefix)
	proof, err := auth.ProveOfflineRunner(ctx, manifest.RunnerSubject, time.Now().UTC())
	if err != nil || proof.TokenID != manifest.RunnerTokenID || proof.DeviceID != manifest.RunnerDeviceID {
		t.Fatalf("restored runner proof: %+v %v", proof, err)
	}
	grant, err := s.RecoverOfflineEffectGrantV2(ctx, manifest.PlanID, manifest.AttemptID, proof, time.Now().UTC())
	if err != nil || grant.ID != manifest.GrantID || grant.Phase != "terminal_verified" || grant.Commitment == nil || grant.Dispatch == nil || grant.Terminal == nil {
		t.Fatalf("restored v2 event chain: %+v %v", grant, err)
	}
	fence, _, err := s.GetFleetTargetFence(ctx, manifest.TargetID)
	if err != nil || !fence.Held || fence.HolderPlanID != manifest.PlanID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration {
		t.Fatalf("restored shared fence: %+v, %v", fence, err)
	}
	if state, reason, err := s.FleetTargetOutcome(ctx, manifest.TargetID, time.Now().UTC()); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineAttemptQueued {
		t.Fatalf("restored offline holder occupancy = %s/%s, %v", state, reason, err)
	}
	// Lost responses may replay the same reservation. Its one-use owner
	// tombstone must be present even for that exact idempotent readback.
	replayBinding, approval := offlineTestApprovalID(t, binding, binding.CreatedAt.Add(time.Minute), manifest.AuthorizationID)
	if !sameOfflineBinding(binding, replayBinding) {
		t.Fatal("restore fixture cannot reconstruct exact signed owner binding")
	}
	if _, err := s.ReserveOfflineDispatch(ctx, binding, approval, binding.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("exact restore replay refused: %v", err)
	}
	if _, err := client.Delete(ctx, s.offlineOwnerConsumptionKey(manifest.AuthorizationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveOfflineDispatch(ctx, binding, approval, binding.CreatedAt.Add(time.Minute)); !errors.Is(err, ErrOfflineDispatchReservationConflict) {
		t.Fatalf("missing restored one-use tombstone replayed: %v", err)
	}
	if _, err := client.Put(ctx, s.offlineOwnerConsumptionKey(manifest.AuthorizationID),
		binding.PlanID+":"+binding.OwnerEnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	// A restored queued attempt is durable evidence, not permission to execute.
	// The post-restore epoch change makes the old holder stale; a new admission
	// cannot silently reuse the already-consumed owner approval or attempt key.
	epoch, err := s.FleetAuthorityEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceFleetAuthorityEpoch(ctx, epoch, "offline-restore"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitOfflineAttempt(ctx, offlineAttemptClaim(binding), binding.RunnerPrincipalSubject,
		uuid.NewString(), time.Now().UTC()); err == nil {
		t.Fatal("restored stale offline holder admitted another attempt")
	}
	unchanged, err := s.GetOfflineAttempt(ctx, manifest.PlanID)
	if err != nil || unchanged != attempt {
		t.Fatalf("restore replay changed attempt: %+v, %v", unchanged, err)
	}
	if consumed, err := client.Get(ctx, s.offlineOwnerConsumptionKey(manifest.AuthorizationID)); err != nil || len(consumed.Kvs) != 1 {
		t.Fatalf("post-restore replay lost the one-use owner tombstone: %v", err)
	}
}
