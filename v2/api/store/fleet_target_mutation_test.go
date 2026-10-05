package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// goldenAcceptances are the fixed capacity-plan and reconciliation
// acceptances whose fingerprints must never change (WP9a).
func goldenAcceptances() (OperationAcceptance, OperationAcceptance) {
	identity := OperationRequestIdentity{Authority: "00000000-0000-4000-8000-000000000001", Actor: OperationActor{Issuer: "issuer", Subject: "subject"}, Kind: "fleet.capacity-plan", Resource: "golden-cluster", Key: "golden-key"}
	plan := OperationAcceptance{Identity: identity,
		Operation: model.Operation{ID: "golden-plan", Kind: "fleet.capacity-plan", Ref: "golden-cluster", Status: model.OperationSucceeded, MaxAttempts: 1,
			Payload: map[string]interface{}{"cluster": "golden-cluster", "planSha256": "abc"}, Metadata: map[string]interface{}{}},
		Semantics: map[string]interface{}{"policy": "golden"}}
	rec := OperationAcceptance{Identity: OperationRequestIdentity{Authority: identity.Authority, Actor: identity.Actor, Kind: "fleet.reconciliation", Resource: "00000000-0000-4000-8000-0000000000aa", Key: "golden-rec"},
		Operation: model.Operation{ID: "golden-rec-op", Kind: "fleet.reconciliation", Ref: "00000000-0000-4000-8000-0000000000aa", Status: model.OperationSucceeded, MaxAttempts: 1,
			Payload: map[string]interface{}{"phase": "complete"}, Metadata: map[string]interface{}{}},
		FleetReconciliation: &FleetReconciliationAdmission{PlanID: "00000000-0000-4000-8000-0000000000aa", AttemptID: "00000000-0000-4000-8000-0000000000bb", RequireActiveAttempt: true, RunnerAttemptID: "r1", WorkflowURL: "https://example.test/wf"}}
	return plan, rec
}

func (h fleetTargetTestHarness) operations() (*PGOperationStore, error) {
	signer, err := NewHMACAcceptanceSigner(acceptanceTestKey)
	if err != nil {
		return nil, err
	}
	return NewPGOperationStore(h.db, signer, AcceptancePolicy{})
}

func (h fleetTargetTestHarness) acceptMutation(ctx context.Context, key string, admission FleetTargetMutationAdmission, evidence *FleetTargetReleaseEvidence) (AcceptedOperation, error) {
	operations, err := h.operations()
	if err != nil {
		return AcceptedOperation{}, err
	}
	authority, err := operations.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	acceptance, err := NewFleetTargetMutationAcceptance(authority, OperationActor{Issuer: "conformance", Subject: "admin"}, key, AcceptanceAuditContext{Source: "conformance"}, admission, evidence)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return operations.Accept(ctx, acceptance)
}

func (h fleetTargetTestHarness) SignedRegister(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, key string) (string, bool, error) {
	accepted, err := h.acceptMutation(ctx, key, FleetTargetMutationAdmission{Kind: FleetTargetMutationRegister, Provider: identity.Provider, ProviderAccount: identity.ProviderAccount, StateBackend: identity.StateBackend, Aliases: aliases}, nil)
	if err != nil {
		return "", false, err
	}
	return accepted.Operation.Ref, accepted.Replayed, nil
}

func (h fleetTargetTestHarness) SignedRelease(ctx context.Context, targetID string, expectedGeneration int64, mode lifecycle.ReleaseMode, proof *lifecycle.TerminalProof, snapshotSHA256, key string) (bool, error) {
	// The proof is passed as server evidence, outside the signed admission,
	// exactly as the WP9b route must after its own observer calls.
	evidence := &FleetTargetReleaseEvidence{ListingSnapshotSHA256: snapshotSHA256}
	if proof != nil {
		evidence.BoundApplyRunCompleted = proof.BoundApplyRunCompleted
		for id, completed := range proof.CompletedRunnerAttemptIDs {
			if completed {
				evidence.CompletedRunnerAttemptIDs = append(evidence.CompletedRunnerAttemptIDs, id)
			}
		}
	}
	accepted, err := h.acceptMutation(ctx, key, FleetTargetMutationAdmission{Kind: FleetTargetMutationRelease, TargetID: targetID, Release: &FleetTargetReleaseAdmission{Mode: string(mode), ExpectedGeneration: expectedGeneration}}, evidence)
	return accepted.Replayed, err
}

func (h fleetTargetTestHarness) SignedAbandonPlan(ctx context.Context, planID, snapshotSHA256, key string) (bool, error) {
	accepted, err := h.acceptMutation(ctx, key, FleetTargetMutationAdmission{Kind: FleetTargetMutationAbandonPlan, PlanID: planID,
		Release: &FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon)}}, &FleetTargetReleaseEvidence{ListingSnapshotSHA256: snapshotSHA256})
	return accepted.Replayed, err
}

func (h fleetTargetTestHarness) SeedHolder(ctx context.Context, cluster, targetID string) (string, int64, error) {
	planID, err := h.SeedInFlightDispatch(ctx, cluster, "", "dispatched")
	if err != nil {
		return "", 0, err
	}
	if targetID == "" {
		return planID, 0, nil
	}
	_, err = h.db.Pool.Exec(ctx, `
		UPDATE fleet_target_fences
		SET generation=1, held=true, holder_plan_id=$2, holder_nonce_sha256=$3, revision=1,
		    authority_epoch=(SELECT epoch FROM fleet_authority_epoch WHERE singleton)
		WHERE target_id=$1
	`, targetID, planID, "nonce-"+planID)
	return planID, 1, err
}

func (h fleetTargetTestHarness) AgeHolder(ctx context.Context, planID string, by time.Duration) error {
	_, err := h.db.Pool.Exec(ctx, `
		UPDATE fleet_github_dispatches
		SET submission_started_at = submission_started_at - ($2 * interval '1 second'),
		    created_at = created_at - ($2 * interval '1 second')
		WHERE plan_id=$1
	`, planID, by.Seconds())
	return err
}

func (h fleetTargetTestHarness) FenceState(ctx context.Context, targetID string) (bool, int64, error) {
	var held bool
	var generation int64
	err := h.db.Pool.QueryRow(ctx, `SELECT held, generation FROM fleet_target_fences WHERE target_id=$1`, targetID).Scan(&held, &generation)
	return held, generation, err
}

func (h fleetTargetTestHarness) PlanAbandoned(ctx context.Context, planID string) (bool, error) {
	return h.db.IsFleetPlanAbandonedForPlan(ctx, planID)
}

func (h fleetTargetTestHarness) OperationCount(ctx context.Context, kind string) (int, error) {
	var count int
	err := h.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind=$1`, kind).Scan(&count)
	return count, err
}

func (h fleetTargetTestHarness) ErrorCode(err error) string {
	var fence *lifecycle.FenceError
	if errors.As(err, &fence) {
		return fence.Code
	}
	return ""
}

func (h fleetTargetTestHarness) ExistingFingerprints() (string, string, error) {
	plan, reconciliation := goldenAcceptances()
	planFingerprint, err := CanonicalOperationRequestFingerprint(plan)
	if err != nil {
		return "", "", err
	}
	reconciliationFingerprint, err := CanonicalOperationRequestFingerprint(reconciliation)
	return planFingerprint.Digest, reconciliationFingerprint.Digest, err
}

// TestFleetTargetReleaseEvidenceServerOnly pins the WP9b contract (B2):
// release proof cannot arrive through a decoded request and never changes
// the request fingerprint; it is recorded only from the server-only field.
func TestFleetTargetReleaseEvidenceServerOnly(t *testing.T) {
	body := `{"fleetTargetMutation":{"kind":"release","targetId":"tgt_x","release":{"mode":"terminal","proof":{"boundApplyRunCompleted":true}}},` +
		`"fleetTargetReleaseEvidence":{"boundApplyRunCompleted":true},"FleetTargetReleaseEvidence":{"boundApplyRunCompleted":true}}`
	var decoded OperationAcceptance
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FleetTargetReleaseEvidence != nil {
		t.Fatal("release evidence was decoded from a request body")
	}
	admission := FleetTargetMutationAdmission{Kind: FleetTargetMutationRelease, TargetID: "tgt_x", Release: &FleetTargetReleaseAdmission{Mode: "terminal", ExpectedGeneration: 2, Reason: "done"}}
	actor := OperationActor{Issuer: "issuer", Subject: "admin"}
	authority := "00000000-0000-4000-8000-000000000001"
	bare, err := NewFleetTargetMutationAcceptance(authority, actor, "k", AcceptanceAuditContext{Source: "test"}, admission, nil)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &FleetTargetReleaseEvidence{BoundApplyRunCompleted: true, CompletedRunnerAttemptIDs: []string{"b", "a", "a"}, ListingSnapshotSHA256: strings.Repeat("a", 64)}
	proven, err := NewFleetTargetMutationAcceptance(authority, actor, "k", AcceptanceAuditContext{Source: "test"}, admission, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Fingerprint != proven.Fingerprint {
		t.Fatal("server evidence changed the request fingerprint")
	}
	if _, ok := bare.Operation.Metadata[FleetTargetReleaseEvidenceMetadataKey]; ok {
		t.Fatal("evidence recorded without server evidence")
	}
	recorded, ok := proven.Operation.Metadata[FleetTargetReleaseEvidenceMetadataKey].(map[string]interface{})
	if !ok || recorded["boundApplyRunCompleted"] != true || len(proven.FleetTargetReleaseEvidence.CompletedRunnerAttemptIDs) != 2 {
		t.Fatalf("evidence not recorded canonically: %#v", proven.Operation.Metadata)
	}
	forged := bare
	forged.Operation.Metadata = map[string]interface{}{FleetTargetReleaseEvidenceMetadataKey: map[string]interface{}{"boundApplyRunCompleted": true}}
	if err := normalizeFleetTargetMutationAcceptance(&forged); err != nil {
		t.Fatal(err)
	}
	if _, ok := forged.Operation.Metadata[FleetTargetReleaseEvidenceMetadataKey]; ok || forged.FleetTargetReleaseEvidence.TerminalProof().BoundApplyRunCompleted {
		t.Fatal("caller-supplied metadata evidence survived normalization")
	}
	register := FleetTargetMutationAdmission{Kind: FleetTargetMutationRegister, Provider: "aws", ProviderAccount: "1", StateBackend: "s3://b/state", Aliases: []string{"cluster:c"}}
	if _, err := NewFleetTargetMutationAcceptance(authority, actor, "r", AcceptanceAuditContext{Source: "test"}, register, evidence); err == nil {
		t.Fatal("register accepted release evidence")
	}
}
