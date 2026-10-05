package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h fleetTargetTestHarness) acceptMutation(ctx context.Context, key string, admission store.FleetTargetMutationAdmission, evidence *store.FleetTargetReleaseEvidence) (store.AcceptedOperation, error) {
	authority, err := h.store.Authority(ctx)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance, err := store.NewFleetTargetMutationAcceptance(authority, store.OperationActor{Issuer: "conformance", Subject: "admin"}, key, store.AcceptanceAuditContext{Source: "conformance"}, admission, evidence)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	return h.store.Accept(ctx, acceptance)
}

func (h fleetTargetTestHarness) SignedRegister(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, key string) (string, bool, error) {
	accepted, err := h.acceptMutation(ctx, key, store.FleetTargetMutationAdmission{Kind: store.FleetTargetMutationRegister, Provider: identity.Provider, ProviderAccount: identity.ProviderAccount, StateBackend: identity.StateBackend, Aliases: aliases}, nil)
	if err != nil {
		return "", false, err
	}
	return accepted.Operation.Ref, accepted.Replayed, nil
}

func (h fleetTargetTestHarness) SignedRelease(ctx context.Context, targetID string, expectedGeneration int64, mode lifecycle.ReleaseMode, proof *lifecycle.TerminalProof, snapshotSHA256, key string) (bool, error) {
	// The proof is passed as server evidence, outside the signed admission,
	// exactly as the WP9b route must after its own observer calls.
	evidence := &store.FleetTargetReleaseEvidence{ListingSnapshotSHA256: snapshotSHA256}
	if proof != nil {
		evidence.BoundApplyRunCompleted = proof.BoundApplyRunCompleted
		for id, completed := range proof.CompletedRunnerAttemptIDs {
			if completed {
				evidence.CompletedRunnerAttemptIDs = append(evidence.CompletedRunnerAttemptIDs, id)
			}
		}
	}
	accepted, err := h.acceptMutation(ctx, key, store.FleetTargetMutationAdmission{Kind: store.FleetTargetMutationRelease, TargetID: targetID, Release: &store.FleetTargetReleaseAdmission{Mode: string(mode), ExpectedGeneration: expectedGeneration}}, evidence)
	return accepted.Replayed, err
}

func (h fleetTargetTestHarness) SignedAbandonPlan(ctx context.Context, planID, snapshotSHA256, key string) (bool, error) {
	accepted, err := h.acceptMutation(ctx, key, store.FleetTargetMutationAdmission{Kind: store.FleetTargetMutationAbandonPlan, PlanID: planID,
		Release: &store.FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon)}}, &store.FleetTargetReleaseEvidence{ListingSnapshotSHA256: snapshotSHA256})
	return accepted.Replayed, err
}

func (h fleetTargetTestHarness) SeedHolder(ctx context.Context, cluster, targetID string) (string, int64, error) {
	planID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		StartedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: map[string]interface{}{"cluster": cluster}, Metadata: map[string]interface{}{},
	}
	planRecord, err := json.Marshal(v3Record{Operation: plan})
	if err != nil {
		return "", 0, err
	}
	if _, err := h.store.kv.Put(ctx, h.store.opKey(planID), string(planRecord)); err != nil {
		return "", 0, err
	}
	nonce := "nonce-" + planID
	preparation, err := json.Marshal(v3FleetGitHubDispatchPreparation{
		FleetGitHubDispatchPreparation: FleetGitHubDispatchPreparation{PlanID: planID, DispatchNonceSHA256: nonce},
		CreatedAt:                      now,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := h.store.kv.Put(ctx, h.store.fleetGitHubDispatchPreparationKey(planID), string(preparation)); err != nil {
		return "", 0, err
	}
	if targetID == "" {
		return planID, 0, nil
	}
	epoch, err := h.store.FleetAuthorityEpoch(ctx)
	if err != nil {
		return "", 0, err
	}
	fence, err := json.Marshal(v3FleetTargetFence{Generation: 1, Held: true, HolderPlanID: planID, HolderNonceSHA256: nonce, AuthorityEpoch: epoch, Revision: 1})
	if err != nil {
		return "", 0, err
	}
	_, err = h.store.kv.Put(ctx, h.store.fleetTargetFenceKey(targetID), string(fence))
	return planID, 1, err
}

func (h fleetTargetTestHarness) AgeHolder(ctx context.Context, planID string, by time.Duration) error {
	return h.store.AgeFleetTargetHolder(ctx, planID, by)
}

func (h fleetTargetTestHarness) FenceState(ctx context.Context, targetID string) (bool, int64, error) {
	fence, _, err := h.store.GetFleetTargetFence(ctx, targetID)
	return fence.Held, fence.Generation, err
}

func (h fleetTargetTestHarness) PlanAbandoned(ctx context.Context, planID string) (bool, error) {
	return h.store.CheckFleetTargetPlanAbandoned(ctx, planID)
}

func (h fleetTargetTestHarness) OperationCount(ctx context.Context, kind string) (int, error) {
	operations, err := h.store.ListOperationsByKind(ctx, kind, 100)
	return len(operations), err
}

func (h fleetTargetTestHarness) ErrorCode(err error) string {
	var fence *lifecycle.FenceError
	if errors.As(err, &fence) {
		return fence.Code
	}
	return ""
}

// ExistingFingerprints builds the same fixed acceptances as
// store.goldenAcceptances (a package-private test fixture), so both backends
// are checked against one pair of golden digests.
func (h fleetTargetTestHarness) ExistingFingerprints() (string, string, error) {
	identity := store.OperationRequestIdentity{Authority: "00000000-0000-4000-8000-000000000001", Actor: store.OperationActor{Issuer: "issuer", Subject: "subject"}, Kind: "fleet.capacity-plan", Resource: "golden-cluster", Key: "golden-key"}
	plan := store.OperationAcceptance{Identity: identity,
		Operation: model.Operation{ID: "golden-plan", Kind: "fleet.capacity-plan", Ref: "golden-cluster", Status: model.OperationSucceeded, MaxAttempts: 1,
			Payload: map[string]interface{}{"cluster": "golden-cluster", "planSha256": "abc"}, Metadata: map[string]interface{}{}},
		Semantics: map[string]interface{}{"policy": "golden"}}
	reconciliation := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: identity.Authority, Actor: identity.Actor, Kind: "fleet.reconciliation", Resource: "00000000-0000-4000-8000-0000000000aa", Key: "golden-rec"},
		Operation: model.Operation{ID: "golden-rec-op", Kind: "fleet.reconciliation", Ref: "00000000-0000-4000-8000-0000000000aa", Status: model.OperationSucceeded, MaxAttempts: 1,
			Payload: map[string]interface{}{"phase": "complete"}, Metadata: map[string]interface{}{}},
		FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: "00000000-0000-4000-8000-0000000000aa", AttemptID: "00000000-0000-4000-8000-0000000000bb", RequireActiveAttempt: true, RunnerAttemptID: "r1", WorkflowURL: "https://example.test/wf"}}
	planFingerprint, err := store.CanonicalOperationRequestFingerprint(plan)
	if err != nil {
		return "", "", err
	}
	reconciliationFingerprint, err := store.CanonicalOperationRequestFingerprint(reconciliation)
	return planFingerprint.Digest, reconciliationFingerprint.Digest, err
}
