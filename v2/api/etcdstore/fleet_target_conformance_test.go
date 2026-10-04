package etcdstore

// Package etcdstore (not etcdstore_test), like several other integration
// test files in this directory (e.g. v3_fleet_app_target_integration_test.go),
// so the harness below can reach V3OperationStore's private key builders and
// fields directly -- mirroring store/fleet_target_conformance_test.go, which
// is "package store" for the same reason.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// fleetTargetConformanceStore returns a V3OperationStore rooted at a fresh,
// unique prefix on the real etcd integrationtest.Etcd points at (WP1's
// NORN_TEST_REQUIRE_INTEGRATION fail-fatal behavior applies).
func fleetTargetConformanceStore(t *testing.T) *V3OperationStore {
	t.Helper()
	client, prefix := integrationtest.Etcd(t)
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-target-conformance-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

// fleetTargetTestHarness implements fleettest.TargetHarness against a real
// etcd-backed V3OperationStore.
type fleetTargetTestHarness struct {
	store *V3OperationStore
}

func (h fleetTargetTestHarness) Register(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (string, error) {
	target, err := h.store.RegisterFleetTarget(ctx, identity, aliases, operationID)
	if err != nil {
		return "", err
	}
	return target.TargetID, nil
}

func (h fleetTargetTestHarness) IsAliasConflict(err error) bool {
	var fe *lifecycle.FenceError
	return errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetAliasConflict
}

func (h fleetTargetTestHarness) IsRegistrationInFlight(err error) bool {
	var fe *lifecycle.FenceError
	return errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetRegistrationInFlight
}

func (h fleetTargetTestHarness) RegistryGeneration(ctx context.Context) (int64, error) {
	response, err := h.store.kv.Get(ctx, h.store.fleetTargetRegistryKey())
	if err != nil {
		return 0, err
	}
	if len(response.Kvs) == 0 {
		return 0, nil
	}
	var registry v3FleetTargetRegistry
	if err := decodeV3Record(response.Kvs[0].Value, &registry); err != nil {
		return 0, err
	}
	return registry.Generation, nil
}

// SeedInFlightDispatch writes a dispatch preparation key directly, bypassing
// AcceptFleetGitHubDispatch's full signed flow and its "staging/nyc3"
// environment-format validation -- the same shortcut PG's harness takes with
// the lower-level CreateFleetGitHubDispatch instead of a fully signed
// dispatch. dispatchState is accepted for TargetHarness parity with PG's
// three dispatch_state values; etcd has no such column, so any preparation
// key counts as in flight regardless of its value (plan.md §2.2, WP4).
func (h fleetTargetTestHarness) SeedInFlightDispatch(ctx context.Context, cluster, environment, dispatchState string) (string, error) {
	_ = dispatchState
	planID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		StartedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: map[string]interface{}{"cluster": cluster}, Metadata: map[string]interface{}{},
	}
	planRecord, err := json.Marshal(v3Record{Operation: plan})
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.opKey(planID), string(planRecord)); err != nil {
		return "", err
	}
	preparation := v3FleetGitHubDispatchPreparation{
		FleetGitHubDispatchPreparation: FleetGitHubDispatchPreparation{PlanID: planID, FleetEnvironment: environment},
		CreatedAt:                      now,
	}
	preparationRecord, err := json.Marshal(preparation)
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.fleetGitHubDispatchPreparationKey(planID), string(preparationRecord)); err != nil {
		return "", err
	}
	return planID, nil
}

func (h fleetTargetTestHarness) Epoch(ctx context.Context) (int64, error) {
	return h.store.FleetAuthorityEpoch(ctx)
}

func (h fleetTargetTestHarness) AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	return h.store.AdvanceFleetAuthorityEpoch(ctx, expected, reason)
}

func (h fleetTargetTestHarness) IsEpochConflict(err error) bool {
	return errors.Is(err, ErrFleetAuthorityEpochConflict)
}

func (h fleetTargetTestHarness) SeedFence(ctx context.Context, targetID string, authorityEpoch int64) error {
	record := v3FleetTargetFence{Generation: 1, Held: true, HolderPlanID: "seed-plan", HolderNonceSHA256: "seed-nonce", AuthorityEpoch: authorityEpoch, Revision: 1}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = h.store.kv.Put(ctx, h.store.fleetTargetFenceKey(targetID), string(encoded))
	return err
}

func (h fleetTargetTestHarness) FenceAuthorityEpoch(ctx context.Context, targetID string) (int64, error) {
	facts, _, err := h.store.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return 0, err
	}
	return facts.AuthorityEpoch, nil
}

func TestFleetTargetConformanceEtcd(t *testing.T) {
	s := fleetTargetConformanceStore(t)
	fleettest.RunFleetTargetConformance(t, fleetTargetTestHarness{store: s})
}

// acquireFleetTargetFenceForTest drives the WP4 primitives (resolve, read
// epoch, read fence) and lifecycle.DecideAcquire, then commits a Txn whose
// compares cover the registry, fence and epoch ModRevisions it read --
// standing in for WP8b's real fenced dispatch submit, exactly as
// store.acquireFleetTargetFenceForTest stands in for WP8a on PG.
func acquireFleetTargetFenceForTest(ctx context.Context, s *V3OperationStore, cluster, environment, planID string) error {
	targetID, empty, registryRevision, err := s.ResolveFleetTargetForPlan(ctx, cluster, environment)
	if err != nil {
		return err
	}
	if empty {
		return errors.New("registry unexpectedly empty")
	}
	epoch, epochRevision, err := s.fleetAuthorityEpochWithRevision(ctx)
	if err != nil {
		return err
	}
	fence, fenceRevision, err := s.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return err
	}
	next, err := lifecycle.DecideAcquire(fence, planID, "nonce-"+planID, time.Now(), epoch)
	if err != nil {
		return err
	}
	record, err := json.Marshal(fleetTargetFenceRecord(next))
	if err != nil {
		return err
	}
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetRegistryKey()), "=", registryRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(targetID)), "=", fenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", epochRevision),
	).Then(clientv3.OpPut(s.fleetTargetFenceKey(targetID), string(record))).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied}
	}
	return nil
}

// TestFleetTargetFenceConcurrencyEtcd races real etcd Txns from concurrent
// goroutines (M1, plan.md §2.2), mirroring
// TestFleetTargetFenceConcurrencyPostgres: N racers resolving the same
// target through both the cluster and the environment alias, exactly one
// winner.
func TestFleetTargetFenceConcurrencyEtcd(t *testing.T) {
	s := fleetTargetConformanceStore(t)
	ctx := context.Background()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "555555555555", StateBackend: "s3://race-bucket/state"}
	target, err := s.RegisterFleetTarget(ctx, identity, []string{"cluster:race", "environment:race-prod"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("AcquireRaceThroughBothAliasesHasOneWinner", func(t *testing.T) {
		const racers = 8
		start := make(chan struct{})
		errs := make(chan error, racers)
		for i := 0; i < racers; i++ {
			environment := ""
			if i%2 == 1 {
				environment = "race-prod" // resolves through the environment alias too
			}
			planID := uuid.NewString()
			go func() {
				<-start
				errs <- acquireFleetTargetFenceForTest(ctx, s, "race", environment, planID)
			}()
		}
		close(start)
		winners := 0
		for i := 0; i < racers; i++ {
			err := <-errs
			var fe *lifecycle.FenceError
			switch {
			case err == nil:
				winners++
			case errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetExecutionOccupied:
			default:
				t.Fatalf("racer failed with %v, want nil or fleet_target_execution_occupied", err)
			}
		}
		if winners != 1 {
			t.Fatalf("exactly one concurrent acquire must win, got %d", winners)
		}
		fence, _, err := s.GetFleetTargetFence(ctx, target.TargetID)
		if err != nil || !fence.Held || fence.Generation != 1 {
			t.Fatalf("the winning fence must be held at generation 1, got %+v, %v", fence, err)
		}
	})
}

// interleavingKV runs before exactly once, immediately before the first Txn
// is built, so a write can land between a method's reads and its commit.
type interleavingKV struct {
	clientv3.KV
	once   sync.Once
	before func()
}

func (k *interleavingKV) Txn(ctx context.Context) clientv3.Txn {
	k.once.Do(k.before)
	return k.KV.Txn(ctx)
}

// TestFleetTargetRegistrationAtomicityEtcd covers the etcd-specific half of
// M1 that the shared suite cannot reach: the registration Txn's prefix
// compare, H7 abandonment, the succeeded-attempt exclusion, concurrent
// registration, and fail-closed handling of deleted singleton keys.
func TestFleetTargetRegistrationAtomicityEtcd(t *testing.T) {
	s := fleetTargetConformanceStore(t)
	h := fleetTargetTestHarness{store: s}
	ctx := context.Background()

	t.Run("PreparationLandingBeforeCommitRefuses", func(t *testing.T) {
		// The plan goes in flight after registration's in-flight scan but
		// before its Txn: only the preparations prefix compare catches it.
		racy := *s
		var seedErr error
		racy.kv = &interleavingKV{KV: s.kv, before: func() {
			_, seedErr = h.SeedInFlightDispatch(ctx, "late-cluster", "late-env", "submitting")
		}}
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "666666666666", StateBackend: "s3://late-bucket/state"}
		_, err := racy.RegisterFleetTarget(ctx, identity, []string{"cluster:late-cluster"}, uuid.NewString())
		if seedErr != nil {
			t.Fatal(seedErr)
		}
		if !h.IsRegistrationInFlight(err) {
			t.Fatalf("a preparation created between the in-flight scan and the commit must refuse registration, got %v", err)
		}
		if target, err := s.GetFleetTarget(ctx, mustFleetTargetID(t, identity)); err != nil || target != nil {
			t.Fatalf("a refused registration must write nothing, got %+v, %v", target, err)
		}
	})

	t.Run("EnvironmentAliasInFlightRefuses", func(t *testing.T) {
		if _, err := h.SeedInFlightDispatch(ctx, "unrelated-cluster", "env-inflight", "submitting"); err != nil {
			t.Fatal(err)
		}
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "777777777777", StateBackend: "s3://env-bucket/state"}
		if _, err := s.RegisterFleetTarget(ctx, identity, []string{"cluster:env-target", "environment:env-inflight"}, uuid.NewString()); !h.IsRegistrationInFlight(err) {
			t.Fatalf("an in-flight dispatch on the environment alias must refuse registration, got %v", err)
		}
	})

	t.Run("AbandonedAndSucceededPlansAreNotInFlight", func(t *testing.T) {
		abandoned, err := h.SeedInFlightDispatch(ctx, "settled-cluster", "", "submitting")
		if err != nil {
			t.Fatal(err)
		}
		record, _ := json.Marshal(v3FleetTargetAbandoned{PlanID: abandoned, AbandonedAt: time.Now().UTC()})
		if _, err := s.kv.Put(ctx, s.fleetTargetAbandonedKey(abandoned), string(record)); err != nil {
			t.Fatal(err)
		}
		succeeded, err := h.SeedInFlightDispatch(ctx, "settled-cluster", "", "dispatched")
		if err != nil {
			t.Fatal(err)
		}
		attempt, _ := json.Marshal(fleet.RunnerAttempt{ID: "attempt-1", PlanID: succeeded, Attempt: 1, Status: "succeeded"})
		if _, err := s.kv.Put(ctx, s.fleetRunnerAttemptKey(succeeded, "attempt-1"), string(attempt)); err != nil {
			t.Fatal(err)
		}
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "888888888888", StateBackend: "s3://settled-bucket/state"}
		if _, err := s.RegisterFleetTarget(ctx, identity, []string{"cluster:settled-cluster"}, uuid.NewString()); err != nil {
			t.Fatalf("abandoned (H7) and succeeded plans must not block registration, got %v", err)
		}
	})

	t.Run("ConcurrentIdenticalRegistrationsBumpOnce", func(t *testing.T) {
		before, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "999999999999", StateBackend: "s3://concurrent-bucket/state"}
		const racers = 8
		start := make(chan struct{})
		errs := make(chan error, racers)
		for i := 0; i < racers; i++ {
			go func() {
				<-start
				_, err := s.RegisterFleetTarget(ctx, identity, []string{"cluster:concurrent", "environment:concurrent-prod"}, uuid.NewString())
				errs <- err
			}()
		}
		close(start)
		for i := 0; i < racers; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent identical registrations must all succeed, as they do on PG, got %v", err)
			}
		}
		after, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after != before+1 {
			t.Fatalf("concurrent identical registrations must bump the generation exactly once, got %d -> %d", before, after)
		}
	})

	t.Run("DeletedSingletonKeysFailClosed", func(t *testing.T) {
		if _, err := s.kv.Delete(ctx, s.fleetAuthorityEpochKey()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FleetAuthorityEpoch(ctx); err == nil {
			t.Fatal("a deleted epoch key must be an error once targets are registered, never epoch 1 again")
		}
		if _, err := s.kv.Delete(ctx, s.fleetTargetRegistryKey()); err != nil {
			t.Fatal(err)
		}
		if _, empty, _, err := s.ResolveFleetTargetForPlan(ctx, "race", ""); err == nil || empty {
			t.Fatalf("a deleted registry key with targets present must be an error, never an empty registry, got empty=%v err=%v", empty, err)
		}
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "101010101010", StateBackend: "s3://after-delete/state"}
		if _, err := s.RegisterFleetTarget(ctx, identity, []string{"cluster:after-delete"}, uuid.NewString()); err == nil {
			t.Fatal("registration must not restart the generation over a deleted registry key")
		}
	})
}

func mustFleetTargetID(t *testing.T, identity lifecycle.TargetIdentity) string {
	t.Helper()
	_, id, err := lifecycle.CanonicalTarget(identity)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
