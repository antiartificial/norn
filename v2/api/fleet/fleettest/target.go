package fleettest

import (
	"context"
	"testing"
	"time"

	"norn/v2/api/fleet/lifecycle"
)

// Golden request-fingerprint digests of one capacity-plan acceptance and one
// reconciliation acceptance (the fixed inputs each backend harness builds in
// ExistingFingerprints), computed before the WP9a admission field existed. A
// new optional admission field must leave them byte-identical.
const (
	goldenCapacityPlanFingerprint   = "d74a20a44a838031b939fb5ad35e03975a9121fb2bf385fb3d852f4461f9a2eb"
	goldenReconciliationFingerprint = "e763d1355eb3ed101f86d541fe995f923b3090780f8e93a0346702b319cb4349"
)

// codeExpectedGenerationMismatch is store.CodeFleetTargetExpectedGenerationMismatch
// (fleettest cannot import store).
const codeExpectedGenerationMismatch = "fleet_target_expected_generation_mismatch"

// SignedTargetHarness is the WP9a surface: signed target mutations through
// the backend's real OperationStore.Accept, plus the fixtures and probes the
// cases need. Every Signed* call mints a fresh signed acceptance under the
// given idempotency key and reports whether the result was a replay.
type SignedTargetHarness interface {
	SignedRegister(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, key string) (targetID string, replayed bool, err error)
	// SignedRelease releases targetID's fence with the claimed mode,
	// generation and proof (terminal), or snapshot digest (abandon).
	SignedRelease(ctx context.Context, targetID string, expectedGeneration int64, mode lifecycle.ReleaseMode, proof *lifecycle.TerminalProof, snapshotSHA256, key string) (replayed bool, err error)
	SignedAbandonPlan(ctx context.Context, planID, snapshotSHA256, key string) (replayed bool, err error)

	// SeedHolder creates a capacity plan on cluster with a dispatched
	// dispatch (no attempts) and, if targetID is non-empty, makes that
	// registered target's fence held by it at the returned generation.
	SeedHolder(ctx context.Context, cluster, targetID string) (planID string, generation int64, err error)
	AgeHolder(ctx context.Context, planID string, by time.Duration) error
	FenceState(ctx context.Context, targetID string) (held bool, generation int64, err error)
	PlanAbandoned(ctx context.Context, planID string) (bool, error)
	// OperationCount counts operations of kind in the one signed ledger.
	OperationCount(ctx context.Context, kind string) (int, error)
	// ErrorCode returns the lifecycle.FenceError code in err's chain, or "".
	ErrorCode(err error) string
	// ExistingFingerprints computes the request fingerprint digests of the
	// fixed capacity-plan and reconciliation acceptances.
	ExistingFingerprints() (capacityPlan, reconciliation string, err error)
}

// TargetHarness lets RunFleetTargetConformance exercise one backend's
// target, registry, alias and authority-epoch storage through a uniform
// surface. Each backend package wires its own Harness against its real
// storage and calls RunFleetTargetConformance from a top-level
// TestFleetTargetConformance{Postgres,Etcd} test (plan.md §3, WP3/WP4).
type TargetHarness interface {
	SignedTargetHarness

	// Register registers identity with the given aliases, attributed to
	// operationID, and returns the resulting target ID. It is idempotent:
	// replaying an identical identity and alias set must succeed without
	// bumping RegistryGeneration again.
	Register(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (targetID string, err error)
	// IsAliasConflict reports whether err is the backend's
	// fleet_target_alias_conflict refusal.
	IsAliasConflict(err error) bool
	// IsRegistrationInFlight reports whether err is the backend's
	// fleet_target_registration_in_flight refusal.
	IsRegistrationInFlight(err error) bool

	// RegistryGeneration returns the current singleton registry generation.
	RegistryGeneration(ctx context.Context) (int64, error)

	// SeedInFlightDispatch creates a dispatch in dispatchState (one of
	// "submitting", "dispatched" or "rerun_submitting" on PG, or the
	// backend's equivalent in-flight preparation/binding state) for a plan
	// whose capacity plan resolves to cluster and whose dispatch lane is
	// environment, so a registration attempt against those aliases can be
	// refused as in-flight (M1).
	SeedInFlightDispatch(ctx context.Context, cluster, environment, dispatchState string) (planID string, err error)

	// Epoch returns the current authority epoch.
	Epoch(ctx context.Context) (int64, error)
	// AdvanceEpoch CASes the epoch forward from expected.
	AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error)
	// IsEpochConflict reports whether err is the backend's CAS-conflict
	// refusal from AdvanceEpoch.
	IsEpochConflict(err error) bool

	// SeedFence inserts a fence row directly for targetID, bypassing
	// acquire/bind (which land in WP8), so epoch preservation can be
	// observed without them.
	SeedFence(ctx context.Context, targetID string, authorityEpoch int64) error
	// FenceAuthorityEpoch reads back the fence's stored authority epoch.
	FenceAuthorityEpoch(ctx context.Context, targetID string) (int64, error)
}

// RunFleetTargetConformance runs the WP3 target/registry/alias/epoch
// conformance cases against h. Subtests run in order (none is t.Parallel())
// because the epoch cases depend on the singleton epoch's starting value.
func RunFleetTargetConformance(t *testing.T, h TargetHarness) {
	ctx := context.Background()

	t.Run("RegisterIdempotentAndAliasConflict", func(t *testing.T) {
		identityA := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "111111111111", StateBackend: "s3://conformance-bucket-a/state"}
		before, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		idA, err := h.Register(ctx, identityA, []string{"cluster:conformance-a", "environment:conformance-a-prod"}, "op-a-1")
		if err != nil {
			t.Fatalf("expected the first registration to succeed, got %v", err)
		}
		afterFirst, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if afterFirst != before+1 {
			t.Fatalf("expected registration to bump the generation by exactly one, got %d -> %d", before, afterFirst)
		}

		idReplay, err := h.Register(ctx, identityA, []string{"cluster:conformance-a", "environment:conformance-a-prod"}, "op-a-2")
		if err != nil {
			t.Fatalf("expected an identical replay to succeed idempotently, got %v", err)
		}
		if idReplay != idA {
			t.Fatalf("a replay of the same identity must resolve to the same target ID, got %q != %q", idReplay, idA)
		}
		afterReplay, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if afterReplay != afterFirst {
			t.Fatalf("an identical replay must not bump the generation again, got %d -> %d", afterFirst, afterReplay)
		}

		identityB := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "222222222222", StateBackend: "s3://conformance-bucket-b/state"}
		if _, err := h.Register(ctx, identityB, []string{"cluster:conformance-a"}, "op-b-1"); err == nil || !h.IsAliasConflict(err) {
			t.Fatalf("registering a different target with an alias already bound elsewhere must refuse as an alias conflict, got %v", err)
		}
	})

	t.Run("AliasesImmutable", func(t *testing.T) {
		identityC := lifecycle.TargetIdentity{Provider: "gcp", ProviderAccount: "project-c", StateBackend: "gs://conformance-bucket-c/state"}
		identityD := lifecycle.TargetIdentity{Provider: "gcp", ProviderAccount: "project-d", StateBackend: "gs://conformance-bucket-d/state"}
		if _, err := h.Register(ctx, identityC, []string{"cluster:conformance-c", "environment:conformance-c-env"}, "op-c-1"); err != nil {
			t.Fatal(err)
		}
		// A second target cannot claim an alias already bound to the first,
		// even as just one of several aliases in the same call.
		if _, err := h.Register(ctx, identityD, []string{"cluster:conformance-d", "environment:conformance-c-env"}, "op-d-1"); err == nil || !h.IsAliasConflict(err) {
			t.Fatalf("an alias already bound to another target must refuse as a conflict even when mixed with a new alias, got %v", err)
		}
		// identityD must still be registerable on its own aliases: the
		// refused call above must not have partially written anything.
		if _, err := h.Register(ctx, identityD, []string{"cluster:conformance-d"}, "op-d-2"); err != nil {
			t.Fatalf("a refused mixed registration must not leave a partial write behind, got %v", err)
		}
	})

	t.Run("RegistryGenerationBumps", func(t *testing.T) {
		before, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		identityE := lifecycle.TargetIdentity{Provider: "azure", ProviderAccount: "sub-e", StateBackend: "https://conformance-e.blob.core.windows.net/state"}
		if _, err := h.Register(ctx, identityE, []string{"cluster:conformance-e"}, "op-e-1"); err != nil {
			t.Fatal(err)
		}
		after, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after != before+1 {
			t.Fatalf("registering a new target must bump the generation by exactly one, got %d -> %d", before, after)
		}
	})

	t.Run("RegisterRefusedWhileInFlight", func(t *testing.T) {
		planID, err := h.SeedInFlightDispatch(ctx, "conformance-inflight", "", "submitting")
		if err != nil {
			t.Fatal(err)
		}
		identityF := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "333333333333", StateBackend: "s3://conformance-bucket-f/state"}
		if _, err := h.Register(ctx, identityF, []string{"cluster:conformance-inflight"}, "op-f-1"); err == nil || !h.IsRegistrationInFlight(err) {
			t.Fatalf("registering a cluster alias with an in-flight dispatch must refuse as registration-in-flight, got %v (plan %s)", err, planID)
		}
	})

	t.Run("EpochStartsAtOneAndAdvancesByCAS", func(t *testing.T) {
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if epoch != 1 {
			t.Fatalf("the authority epoch must start at one, got %d", epoch)
		}
		if _, err := h.AdvanceEpoch(ctx, epoch+1, "conformance-stale-cas"); err == nil || !h.IsEpochConflict(err) {
			t.Fatalf("advancing with a stale expected epoch must refuse as a CAS conflict, got %v", err)
		}
		next, err := h.AdvanceEpoch(ctx, epoch, "conformance-advance")
		if err != nil {
			t.Fatalf("advancing with the correct expected epoch must succeed, got %v", err)
		}
		if next != epoch+1 {
			t.Fatalf("expected the epoch to advance by exactly one, got %d -> %d", epoch, next)
		}
	})

	t.Run("AdvanceEpochPreservesHistory", func(t *testing.T) {
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		identityG := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "444444444444", StateBackend: "s3://conformance-bucket-g/state"}
		targetID, err := h.Register(ctx, identityG, []string{"cluster:conformance-g"}, "op-g-1")
		if err != nil {
			t.Fatal(err)
		}
		if err := h.SeedFence(ctx, targetID, epoch); err != nil {
			t.Fatal(err)
		}
		if _, err := h.AdvanceEpoch(ctx, epoch, "conformance-preserves-history"); err != nil {
			t.Fatal(err)
		}
		fenceEpoch, err := h.FenceAuthorityEpoch(ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		if fenceEpoch != epoch {
			t.Fatalf("advancing the singleton epoch must not rewrite an existing fence's recorded epoch, got %d, want %d", fenceEpoch, epoch)
		}
		newEpoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if newEpoch != epoch+1 {
			t.Fatalf("the singleton epoch must still have advanced, got %d, want %d", newEpoch, epoch+1)
		}
	})
	runSignedTargetConformance(t, h)
}

func runSignedTargetConformance(t *testing.T, h TargetHarness) {
	ctx := context.Background()
	fullProof := &lifecycle.TerminalProof{BoundApplyRunCompleted: true}
	snapshot := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("RegisterIsSignedAndReplays", func(t *testing.T) {
		identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "555500000001", StateBackend: "s3://signed-register/state"}
		aliases := []string{"cluster:signed-register", "environment:signed-register-prod"}
		id, replayed, err := h.SignedRegister(ctx, identity, aliases, "reg-1")
		if err != nil || replayed {
			t.Fatalf("first signed registration: replayed=%v err=%v", replayed, err)
		}
		generation, err := h.RegistryGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		again, replayed, err := h.SignedRegister(ctx, identity, aliases, "reg-1")
		if err != nil || !replayed || again != id {
			t.Fatalf("same key must replay the same target, got id=%q replayed=%v err=%v", again, replayed, err)
		}
		if _, _, err := h.SignedRegister(ctx, identity, []string{"cluster:signed-register-other"}, "reg-1"); err == nil {
			t.Fatal("the same key with a different request must conflict, not replay")
		}
		other, replayed, err := h.SignedRegister(ctx, identity, aliases, "reg-2")
		if err != nil || replayed || other != id {
			t.Fatalf("a fresh key for an identical registration must be idempotent, got id=%q replayed=%v err=%v", other, replayed, err)
		}
		if after, err := h.RegistryGeneration(ctx); err != nil || after != generation {
			t.Fatalf("an idempotent signed re-registration must not bump the registry generation, got %d -> %d, %v", generation, after, err)
		}
		conflicting := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "555500000002", StateBackend: "s3://signed-register-b/state"}
		if _, _, err := h.SignedRegister(ctx, conflicting, []string{"cluster:signed-register"}, "reg-3"); h.ErrorCode(err) != lifecycle.CodeFleetTargetAliasConflict {
			t.Fatalf("an alias already bound elsewhere must refuse as a conflict through the signed path, got %v", err)
		}
		count, err := h.OperationCount(ctx, "fleet.target.register")
		if err != nil || count != 2 {
			t.Fatalf("exactly the two accepted registrations must be in the one ledger (refusals leave none), got %d, %v", count, err)
		}
	})

	t.Run("ReleaseIsSignedReplaysAndAbandons", func(t *testing.T) {
		register := func(account, cluster string) string {
			t.Helper()
			id, _, err := h.SignedRegister(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: account, StateBackend: "s3://signed-release-" + account + "/state"}, []string{"cluster:" + cluster}, "reg-"+cluster)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		expectCode := func(err error, code string) {
			t.Helper()
			if got := h.ErrorCode(err); got != code {
				t.Fatalf("want refusal %q, got %q (%v)", code, got, err)
			}
		}

		// Terminal release.
		targetA := register("555500000010", "signed-release-a")
		planA, generation, err := h.SeedHolder(ctx, "signed-release-a", targetA)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.SignedRelease(ctx, targetA, generation+1, lifecycle.ReleaseModeTerminal, fullProof, "", "rel-a-1")
		expectCode(err, codeExpectedGenerationMismatch)
		_, err = h.SignedRelease(ctx, targetA, generation, lifecycle.ReleaseModeTerminal, &lifecycle.TerminalProof{}, "", "rel-a-2")
		expectCode(err, lifecycle.CodeFleetTargetTerminalProofIncomplete)
		_, err = h.SignedRelease(ctx, targetA, generation, lifecycle.ReleaseModeAbandon, nil, snapshot, "rel-a-3")
		expectCode(err, lifecycle.CodeFleetTargetAbandonTooSoon)
		if held, _, err := h.FenceState(ctx, targetA); err != nil || !held {
			t.Fatalf("refused releases must leave the fence held, held=%v err=%v", held, err)
		}
		if count, err := h.OperationCount(ctx, "fleet.target.fence-release"); err != nil || count != 0 {
			t.Fatalf("refused releases must leave no ledger entry, got %d, %v", count, err)
		}
		if replayed, err := h.SignedRelease(ctx, targetA, generation, lifecycle.ReleaseModeTerminal, fullProof, "", "rel-a-4"); err != nil || replayed {
			t.Fatalf("terminal release with full proof: replayed=%v err=%v", replayed, err)
		}
		if held, _, err := h.FenceState(ctx, targetA); err != nil || held {
			t.Fatalf("the fence must be free after release, held=%v err=%v", held, err)
		}
		if abandoned, err := h.PlanAbandoned(ctx, planA); err != nil || !abandoned {
			t.Fatalf("a terminal release must permanently abandon the holder plan, abandoned=%v err=%v", abandoned, err)
		}
		if replayed, err := h.SignedRelease(ctx, targetA, generation, lifecycle.ReleaseModeTerminal, fullProof, "", "rel-a-4"); err != nil || !replayed {
			t.Fatalf("the same key must replay even though the fence is now free, replayed=%v err=%v", replayed, err)
		}
		_, err = h.SignedRelease(ctx, targetA, generation, lifecycle.ReleaseModeTerminal, fullProof, "", "rel-a-5")
		expectCode(err, lifecycle.CodeFleetTargetFenceNotHeld)

		// Abandon release (needs the snapshot and the minimum age).
		targetB := register("555500000011", "signed-release-b")
		planB, generationB, err := h.SeedHolder(ctx, "signed-release-b", targetB)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.AgeHolder(ctx, planB, lifecycle.AbandonMinimumAge+time.Minute); err != nil {
			t.Fatal(err)
		}
		_, err = h.SignedRelease(ctx, targetB, generationB, lifecycle.ReleaseModeAbandon, nil, "", "rel-b-1")
		expectCode(err, lifecycle.CodeFleetTargetAbandonSnapshotRequired)
		if _, err := h.SignedRelease(ctx, targetB, generationB, lifecycle.ReleaseModeAbandon, nil, snapshot, "rel-b-2"); err != nil {
			t.Fatalf("abandon release after the minimum age: %v", err)
		}
		if abandoned, err := h.PlanAbandoned(ctx, planB); err != nil || !abandoned {
			t.Fatalf("abandon release must record the plan abandoned, abandoned=%v err=%v", abandoned, err)
		}

		// H7: abandon a plan whose cluster was never registered.
		planC, _, err := h.SeedHolder(ctx, "signed-release-unregistered", "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.SignedAbandonPlan(ctx, planC, snapshot, "abn-c-1")
		expectCode(err, lifecycle.CodeFleetTargetAbandonTooSoon)
		if err := h.AgeHolder(ctx, planC, lifecycle.AbandonMinimumAge+time.Minute); err != nil {
			t.Fatal(err)
		}
		if replayed, err := h.SignedAbandonPlan(ctx, planC, snapshot, "abn-c-2"); err != nil || replayed {
			t.Fatalf("abandon-plan after the minimum age: replayed=%v err=%v", replayed, err)
		}
		if replayed, err := h.SignedAbandonPlan(ctx, planC, snapshot, "abn-c-2"); err != nil || !replayed {
			t.Fatalf("abandon-plan must replay under the same key, replayed=%v err=%v", replayed, err)
		}
		_, err = h.SignedAbandonPlan(ctx, planC, snapshot, "abn-c-3")
		expectCode(err, lifecycle.CodeFleetTargetHolderAbandoned)
		if count, err := h.OperationCount(ctx, "fleet.target.abandon-plan"); err != nil || count != 1 {
			t.Fatalf("exactly one abandon-plan must be in the ledger, got %d, %v", count, err)
		}
		if count, err := h.OperationCount(ctx, "fleet.target.fence-release"); err != nil || count != 2 {
			t.Fatalf("exactly the two accepted releases must be in the ledger, got %d, %v", count, err)
		}
	})

	t.Run("ExistingFingerprintsUnchanged", func(t *testing.T) {
		plan, reconciliation, err := h.ExistingFingerprints()
		if err != nil {
			t.Fatal(err)
		}
		if plan != goldenCapacityPlanFingerprint || reconciliation != goldenReconciliationFingerprint {
			t.Fatalf("existing acceptance fingerprints changed: capacity-plan %s, reconciliation %s", plan, reconciliation)
		}
	})

}
