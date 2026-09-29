package supervisor

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"norn/v2/api/effect"
)

// MigrationPostconditionChecker must independently resolve the original
// accepted target and run the reviewed check named by PostconditionSHA256.
// The verifier treats a missing or changed target as unresolved, never as a
// failed-but-repeat-safe migration.
type MigrationPostconditionChecker interface {
	CheckMigrationPostcondition(context.Context, MigrationIntent) (MigrationPostconditionResult, error)
}

type MigrationPostconditionResult struct {
	TargetSHA256        string
	PostconditionSHA256 string
	Satisfied           bool
}

type MigrationVerifier struct {
	manager *Manager
	checker MigrationPostconditionChecker
}

func NewMigrationVerifier(manager *Manager, checker MigrationPostconditionChecker) (*MigrationVerifier, error) {
	if manager == nil || checker == nil {
		return nil, fmt.Errorf("migration verifier requires a supervisor and original-target checker")
	}
	return &MigrationVerifier{manager: manager, checker: checker}, nil
}

func (v *MigrationVerifier) Verify(ctx context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	descriptor, err := v.manager.verifyMigrationDescriptor(record.Reservation.LaunchPayload)
	if err != nil || record.Reservation.Stage != MigrationStage {
		return effect.Verification{}, fmt.Errorf("migration reservation is untrusted")
	}
	if observation.Evidence.Source != evidenceSource || len(observation.Evidence.Payload) == 0 {
		return effect.Verification{}, fmt.Errorf("migration observation source is untrusted")
	}
	var envelope signedEvidence
	if err := decodeStrict(observation.Evidence.Payload, &envelope); err != nil {
		return effect.Verification{}, fmt.Errorf("migration observation is malformed")
	}
	encoded, err := json.Marshal(envelope.Assertion)
	if err != nil || !hmac.Equal([]byte(envelope.MAC), []byte(v.manager.mac(encoded))) {
		return effect.Verification{}, fmt.Errorf("migration observation authentication failed")
	}
	a := envelope.Assertion
	if a.Protocol == MigrationProtocolV1 && a.InputDigest == record.Reservation.InputDigest &&
		a.SupervisorExecutionID == record.Reservation.SupervisorExecutionID &&
		a.Phase == effect.SupervisorNotFound && observation.Phase == effect.SupervisorNotFound &&
		a.RuntimeInstanceID == "" && observation.Identity.RuntimeInstanceID == "" &&
		record.Execution.RuntimeInstanceID == "" && a.ContainmentProven &&
		a.ExitCode == nil && observation.ExitCode == nil && len(observation.Output) == 0 &&
		a.EvidenceReference == observation.Evidence.Reference && !a.ObservedAt.IsZero() &&
		(strings.HasPrefix(a.EvidenceReference, "registered-not-launched/") ||
			strings.HasPrefix(a.EvidenceReference, "tombstone/")) {
		return effect.Verification{Decision: effect.VerificationNeverLaunched,
			InputDigest: a.InputDigest, SupervisorExecutionID: a.SupervisorExecutionID,
			EvidenceSource: evidenceSource, EvidenceReference: a.EvidenceReference,
			ObservedAt: time.Now().UTC()}, nil
	}
	if a.Protocol != MigrationProtocolV1 || a.InputDigest != record.Reservation.InputDigest ||
		a.SupervisorExecutionID != record.Reservation.SupervisorExecutionID ||
		a.RuntimeInstanceID == "" || a.RuntimeInstanceID != observation.Identity.RuntimeInstanceID ||
		(record.Execution.RuntimeInstanceID != "" && a.RuntimeInstanceID != record.Execution.RuntimeInstanceID) || a.Phase != observation.Phase ||
		a.EvidenceReference != observation.Evidence.Reference || a.ObservedAt.IsZero() ||
		!a.ContainmentProven || a.Phase != effect.SupervisorSucceeded || a.ExitCode == nil ||
		*a.ExitCode != 0 || a.TimedOut || observation.ExitCode == nil || *observation.ExitCode != 0 ||
		len(observation.Output) != 0 || a.ResultDigest != "" || a.ResultReference != "" {
		return effect.Verification{}, fmt.Errorf("migration has no contained successful command result")
	}
	checked, err := v.checker.CheckMigrationPostcondition(ctx, descriptor.MigrationIntent)
	if err != nil {
		return effect.Verification{}, fmt.Errorf("migration original-target postcondition is unavailable: %w", err)
	}
	if !checked.Satisfied || checked.TargetSHA256 != descriptor.TargetSHA256 ||
		checked.PostconditionSHA256 != descriptor.PostconditionSHA256 {
		return effect.Verification{}, fmt.Errorf("migration original-target postcondition is unproven")
	}
	return effect.Verification{
		Decision: effect.VerificationSucceeded, InputDigest: a.InputDigest,
		ResultDigest: effect.DigestInput(nil), ResultReference: "result/" + a.SupervisorExecutionID,
		SupervisorExecutionID: a.SupervisorExecutionID, RuntimeInstanceID: a.RuntimeInstanceID,
		EvidenceSource: evidenceSource, EvidenceReference: a.EvidenceReference,
		ObservedAt: time.Now().UTC(),
	}, nil
}

var _ effect.EvidenceVerifier = (*MigrationVerifier)(nil)
