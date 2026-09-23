package supervisor

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/effect"
)

type Verifier struct {
	manager *Manager
}

func NewVerifier(manager *Manager) (*Verifier, error) {
	if manager == nil {
		return nil, fmt.Errorf("effect supervisor verifier requires a manager")
	}
	return &Verifier{manager: manager}, nil
}

func (v *Verifier) Verify(_ context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	if observation.Evidence.Source != evidenceSource || len(observation.Evidence.Payload) == 0 {
		return effect.Verification{}, fmt.Errorf("effect supervisor evidence source is untrusted")
	}
	var envelope signedEvidence
	if err := json.Unmarshal(observation.Evidence.Payload, &envelope); err != nil {
		return effect.Verification{}, fmt.Errorf("decode effect supervisor evidence: %w", err)
	}
	encoded, _ := json.Marshal(envelope.Assertion)
	if !hmac.Equal([]byte(envelope.MAC), []byte(v.manager.mac(encoded))) {
		return effect.Verification{}, fmt.Errorf("effect supervisor evidence authentication failed")
	}
	assertion := envelope.Assertion
	if assertion.Protocol != ProtocolV1 || assertion.InputDigest != record.Reservation.InputDigest ||
		assertion.SupervisorExecutionID != record.Reservation.SupervisorExecutionID ||
		assertion.RuntimeInstanceID != observation.Identity.RuntimeInstanceID || assertion.Phase != observation.Phase ||
		assertion.EvidenceReference != observation.Evidence.Reference || assertion.ObservedAt.IsZero() {
		return effect.Verification{}, fmt.Errorf("effect supervisor evidence is not bound to the observation")
	}
	decision := effect.VerificationDecision("")
	switch assertion.Phase {
	case effect.SupervisorSucceeded:
		if !assertion.ContainmentProven || assertion.ResultDigest == "" || assertion.ResultReference == "" {
			return effect.Verification{}, fmt.Errorf("successful effect lacks contained result evidence")
		}
		decision = effect.VerificationSucceeded
	case effect.SupervisorNotFound:
		if !assertion.ContainmentProven || strings.TrimSpace(assertion.RuntimeInstanceID) != "" {
			return effect.Verification{}, fmt.Errorf("not-found effect lacks durable tombstone evidence")
		}
		decision = effect.VerificationNeverLaunched
	case effect.SupervisorStopped:
		return effect.Verification{}, fmt.Errorf("stopped build.test is not automatically repeat-safe")
	case effect.SupervisorFailed:
		// A contained command that exited on its own (or was killed by the
		// runner's timeout) has a final outcome and nothing left running. This
		// releases the gate but is not repeat safety: the executor reuses the
		// recorded failure for the same input and never relaunches it.
		if !assertion.ContainmentProven || assertion.ExitCode == nil || assertion.ResultDigest == "" || assertion.ResultReference == "" {
			return effect.Verification{}, fmt.Errorf("failed effect lacks contained final-outcome evidence")
		}
		decision = effect.VerificationFailed
	default:
		return effect.Verification{}, fmt.Errorf("effect supervisor observation is not terminal")
	}
	return effect.Verification{
		Decision: decision, InputDigest: assertion.InputDigest, ResultDigest: assertion.ResultDigest,
		ResultReference: assertion.ResultReference, SupervisorExecutionID: assertion.SupervisorExecutionID,
		RuntimeInstanceID: assertion.RuntimeInstanceID, EvidenceSource: evidenceSource,
		EvidenceReference: assertion.EvidenceReference, ObservedAt: assertion.ObservedAt,
	}, nil
}

var _ effect.EvidenceVerifier = (*Verifier)(nil)
