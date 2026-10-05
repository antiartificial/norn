package etcdstore_test

// TestFleetAuthorityRestoreEtcd has two phases selected by subtest, so
// v2/scripts/test-etcd-three-member-fleet can run "seed" against the healthy
// cluster before the snapshot and "verify" against the cluster restored from
// that snapshot (plan.md WP14, plan-review.md M16). The data lives under a
// fixed prefix the script retains, because the acceptance index stores
// absolute keys and a prefix copy would resolve receipts from the old prefix.
//
// Run without a subtest filter, both phases run back to back against the same
// live cluster, which exercises the same assertions without a restore.

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

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/store"
)

const fleetAuthorityRestoreCluster = "norn-staging" // fleetRunnerPlan's fixed cluster

type fleetAuthorityRestoreManifest struct {
	Authority         string `json:"authority"`
	PlanID            string `json:"planId"`
	TargetID          string `json:"targetId"`
	Nonce             string `json:"nonce"`
	AttemptID         string `json:"attemptId"`
	AttemptRevision   int64  `json:"attemptRevision"`
	HeartbeatSequence int64  `json:"heartbeatSequence"`
	FenceGeneration   int64  `json:"fenceGeneration"`
}

func fleetAuthorityRestoreBase() string {
	if strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_ENDPOINTS")) != "" {
		base := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_PREFIX"))
		if base == "" {
			base = "/norn-test"
		}
		return strings.TrimSuffix(base, "/") + "/authority-restore/"
	}
	return "/norn-test/authority-restore/"
}

func fleetAuthorityRestoreStore(t *testing.T, client *clientv3.Client, authority string) *etcdstore.V3OperationStore {
	t.Helper()
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-authority-restore-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := etcdstore.NewV3OperationStore(client, fleetAuthorityRestoreBase()+"store", authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestFleetAuthorityRestoreEtcd(t *testing.T) {
	t.Run("seed", fleetAuthorityRestoreSeed)
	t.Run("verify", fleetAuthorityRestoreVerify)
}

func fleetAuthorityRestoreSeed(t *testing.T) {
	client, _ := integrationtest.Etcd(t)
	ctx := context.Background()
	manifestKey := fleetAuthorityRestoreBase() + "manifest"
	if _, err := client.Delete(ctx, fleetAuthorityRestoreBase(), clientv3.WithPrefix()); err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	adapter := fleetAuthorityRestoreStore(t, client, authority)

	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "authority-restore", StateBackend: "s3://authority-restore/state"}
	target, err := adapter.RegisterFleetTarget(ctx, identity, []string{"cluster:" + fleetAuthorityRestoreCluster}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	plan := fleetRunnerPlan(t, adapter, "scale")
	nonce := bindFleetRunnerDispatch(t, adapter, plan)
	accepted, err := adapter.Accept(ctx, fleetRunnerAcceptance(t, adapter, plan.ID, nonce, "restore-root", "7", "apply", ""))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := adapter.UpdateFleetRunnerAttempt(ctx, plan.ID, accepted.FleetRunnerAttempt.ID, accepted.FleetRunnerAttempt.Revision, "heartbeat", int64(1), "seeded")
	if err != nil {
		t.Fatal(err)
	}
	fence, _, err := adapter.GetFleetTargetFence(ctx, target.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if epoch, err := adapter.FleetAuthorityEpoch(ctx); err != nil || epoch != 1 {
		t.Fatalf("seed epoch = %d err=%v, want 1", epoch, err)
	}
	if !fence.Held || fence.HolderPlanID != plan.ID || fence.AuthorityEpoch != 1 {
		t.Fatalf("seed fence = %+v", fence)
	}
	encoded, err := json.Marshal(fleetAuthorityRestoreManifest{
		Authority: authority, PlanID: plan.ID, TargetID: target.TargetID, Nonce: nonce, AttemptID: attempt.ID,
		AttemptRevision: attempt.Revision, HeartbeatSequence: attempt.HeartbeatSequence, FenceGeneration: fence.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, manifestKey, string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func fleetAuthorityRestoreVerify(t *testing.T) {
	client, _ := integrationtest.Etcd(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), fleetAuthorityRestoreBase(), clientv3.WithPrefix()) })
	response, err := client.Get(ctx, fleetAuthorityRestoreBase()+"manifest")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Kvs) == 0 {
		t.Fatal("seed manifest is absent: the seed phase did not run against this cluster's data")
	}
	var seeded fleetAuthorityRestoreManifest
	if err := json.Unmarshal(response.Kvs[0].Value, &seeded); err != nil {
		t.Fatal(err)
	}
	adapter := fleetAuthorityRestoreStore(t, client, seeded.Authority)

	// Restored data still carries the seeded epoch, fence and attempt.
	if epoch, err := adapter.FleetAuthorityEpoch(ctx); err != nil || epoch != 1 {
		t.Fatalf("restored epoch = %d err=%v, want 1", epoch, err)
	}
	fenceBefore, _, err := adapter.GetFleetTargetFence(ctx, seeded.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if !fenceBefore.Held || fenceBefore.HolderPlanID != seeded.PlanID || fenceBefore.Generation != seeded.FenceGeneration || fenceBefore.AuthorityEpoch != 1 {
		t.Fatalf("restored fence = %+v, seeded %+v", fenceBefore, seeded)
	}

	// The post-restore stage advances the epoch (Q4).
	newEpoch, err := adapter.AdvanceFleetAuthorityEpoch(ctx, 1, "post-restore")
	if err != nil || newEpoch != 2 {
		t.Fatalf("advance epoch = %d err=%v, want 2", newEpoch, err)
	}
	if _, err := adapter.AdvanceFleetAuthorityEpoch(ctx, 1, "stale"); !errors.Is(err, etcdstore.ErrFleetAuthorityEpochConflict) {
		t.Fatalf("stale epoch advance err=%v, want conflict", err)
	}

	// History is preserved: advancing rewrites no fence or attempt row.
	fenceAfter, _, err := adapter.GetFleetTargetFence(ctx, seeded.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if fenceAfter != fenceBefore {
		t.Fatalf("epoch advance changed the fence: before=%+v after=%+v", fenceBefore, fenceAfter)
	}
	attempt, err := adapter.GetFleetRunnerAttempt(ctx, seeded.PlanID, seeded.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Revision != seeded.AttemptRevision || attempt.HeartbeatSequence != seeded.HeartbeatSequence {
		t.Fatalf("epoch advance changed the attempt: %+v, seeded %+v", attempt, seeded)
	}
	if attempts, err := adapter.ListFleetRunnerAttempts(ctx, seeded.PlanID); err != nil || len(attempts) != 1 {
		t.Fatalf("attempt history = %d err=%v, want 1", len(attempts), err)
	}

	// Old ownership is invalid: the fence is Uncertain/AuthoritySuperseded.
	occupancy, reason, err := adapter.FleetTargetOutcome(ctx, seeded.TargetID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if occupancy != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonAuthoritySuperseded {
		t.Fatalf("occupancy = %s/%s, want Uncertain/AuthoritySuperseded", occupancy, reason)
	}

	// The old attempt's heartbeat is refused with the superseded code.
	_, err = adapter.UpdateFleetRunnerAttempt(ctx, seeded.PlanID, seeded.AttemptID, attempt.Revision, "heartbeat", attempt.HeartbeatSequence+1, "after restore")
	var fenceErr *lifecycle.FenceError
	if !errors.As(err, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetAuthoritySuperseded {
		t.Fatalf("heartbeat after epoch advance err=%v, want %s", err, lifecycle.CodeFleetTargetAuthoritySuperseded)
	}
	if refused, err := adapter.GetFleetRunnerAttempt(ctx, seeded.PlanID, seeded.AttemptID); err != nil || refused.Revision != seeded.AttemptRevision || refused.HeartbeatSequence != seeded.HeartbeatSequence {
		t.Fatalf("refused heartbeat changed the attempt: %+v err=%v", refused, err)
	}

	// A recovery attempt re-binds under the new epoch: Generation+1, epoch 2.
	recovery := verifiedFleetRecoveryAcceptance(t, adapter, seeded.PlanID, seeded.Nonce, "restore-recover", "8", attempt)
	rebound, err := adapter.Accept(ctx, recovery)
	if err != nil {
		t.Fatalf("recovery re-bind after epoch advance: %v", err)
	}
	fenceRebound, _, err := adapter.GetFleetTargetFence(ctx, seeded.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if fenceRebound.Generation != seeded.FenceGeneration+1 || fenceRebound.AuthorityEpoch != 2 || fenceRebound.HolderPlanID != seeded.PlanID {
		t.Fatalf("re-bound fence = %+v, want generation %d epoch 2", fenceRebound, seeded.FenceGeneration+1)
	}
	if _, err := adapter.UpdateFleetRunnerAttempt(ctx, seeded.PlanID, rebound.FleetRunnerAttempt.ID, rebound.FleetRunnerAttempt.Revision, "heartbeat", int64(1), "rebound"); err != nil {
		t.Fatalf("heartbeat on the re-bound attempt: %v", err)
	}
}
