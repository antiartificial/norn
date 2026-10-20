// Package observerclient coordinates an outbound, management-host terminal
// observation. It contains no provider credentials or server route mounting.
package observerclient

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/fleet/offline"
)

// ErrNotFound is the only response that permits creating a challenge or
// observing an effect. Transport failures and ambiguous reads fail closed.
var ErrNotFound = errors.New("exact terminal resource not found")

// Client is an authenticated, TLS-pinned adapter for the target Fleet Norn.
// Implementations must return ErrNotFound only for an authoritative 404 from
// the exact plan/attempt endpoint, not for network or authentication failures.
type Client interface {
	ReadConsumedGrant(context.Context, string, string) (offline.EffectGrant, error)
	ReadTerminal(context.Context, string, string) (offline.TerminalEvidence, error)
	ReadChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error)
	CreateChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error)
	SubmitAttestation(context.Context, string, string, offline.SignedTerminalAttestationV1) (offline.TerminalEvidence, error)
}

// Source owns independent read-only provider, backend and workload clients.
// Observe must durably journal raw source responses before returning an
// attestation; the executor's claims must never be used as source evidence.
type Source interface {
	Observe(context.Context, offline.TerminalChallengeV1, offline.EffectGrant) (offline.TerminalAttestationV1, error)
}

type Runner struct {
	Client          Client
	Source          Source
	ObserverSubject string
	KeyID           string
	PrivateKey      ed25519.PrivateKey
	Clock           func() time.Time
}

// Run records one exact terminal attestation. It never retries observations
// after an ambiguous submission: it first reads the durable target receipt.
// A later invocation likewise recovers that receipt before doing new work.
func (r Runner) Run(ctx context.Context, planID, attemptID string) (offline.TerminalEvidence, error) {
	var zero offline.TerminalEvidence
	if r.Client == nil || r.Source == nil || r.Clock == nil || r.ObserverSubject == "" || r.KeyID == "" || len(r.PrivateKey) != ed25519.PrivateKeySize || planID == "" || attemptID == "" {
		return zero, fmt.Errorf("terminal observer is not configured")
	}
	grant, err := r.Client.ReadConsumedGrant(ctx, planID, attemptID)
	if err != nil || grant.PlanID != planID || grant.AttemptID != attemptID || grant.Phase != "consumed" || grant.ConsumedAt == nil {
		return zero, fmt.Errorf("read exact consumed grant: %w", failure(err))
	}
	terminal, err := r.Client.ReadTerminal(ctx, planID, attemptID)
	if err == nil {
		return requireReceipt(terminal, grant, "")
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, fmt.Errorf("read exact terminal receipt: %w", err)
	}
	challenge, err := r.Client.ReadChallenge(ctx, planID, attemptID)
	if errors.Is(err, ErrNotFound) {
		challenge, err = r.Client.CreateChallenge(ctx, planID, attemptID)
		if err != nil {
			// The create may have committed before its response was lost.
			challenge, err = r.Client.ReadChallenge(ctx, planID, attemptID)
		}
	}
	if err != nil {
		return zero, fmt.Errorf("read exact terminal challenge: %w", err)
	}
	if challenge.PlanID != planID || challenge.AttemptID != attemptID || challenge.ObserverSubject != r.ObserverSubject || challenge.ValidateForGrant(grant, r.Clock().UTC()) != nil {
		return zero, offline.ErrTerminalProof
	}
	attestation, err := r.Source.Observe(ctx, challenge, grant)
	if err != nil {
		return zero, err
	}
	if attestation.ChallengeID != challenge.ID || attestation.PlanID != planID || attestation.AttemptID != attemptID || attestation.GrantID != grant.ID || attestation.ObserverSubject != r.ObserverSubject {
		return zero, offline.ErrTerminalProof
	}
	signed, err := offline.SignTerminalAttestationV1(attestation, r.KeyID, r.PrivateKey)
	if err != nil {
		return zero, err
	}
	// Reject malformed or stale source output locally before sending anything.
	// The target repeats verification against its durable current epoch/fence.
	public, ok := r.PrivateKey.Public().(ed25519.PublicKey)
	if !ok {
		return zero, offline.ErrTerminalProof
	}
	if _, err := offline.VerifyTerminalAttestationV1(signed, challenge, grant, r.ObserverSubject, r.KeyID, public, r.Clock().UTC()); err != nil {
		return zero, err
	}
	terminal, err = r.Client.SubmitAttestation(ctx, planID, attemptID, signed)
	if err != nil {
		// Never issue a second PUT on an ambiguous response. The target's
		// committed receipt decides whether this attempt succeeded.
		terminal, err = r.Client.ReadTerminal(ctx, planID, attemptID)
	}
	if err != nil {
		return zero, fmt.Errorf("recover exact terminal receipt: %w", err)
	}
	return requireReceipt(terminal, grant, attestation.Observation.EvidenceSHA256)
}

func requireReceipt(receipt offline.TerminalEvidence, grant offline.EffectGrant, evidenceSHA string) (offline.TerminalEvidence, error) {
	if receipt.SchemaVersion != offline.TerminalSchema || receipt.PlanID != grant.PlanID || receipt.AttemptID != grant.AttemptID || receipt.GrantID != grant.ID ||
		receipt.BindingSHA256 != grant.BindingSHA256 || receipt.TargetID != grant.TargetID || receipt.FenceGeneration != grant.FenceGeneration || receipt.AuthorityEpoch != grant.AuthorityEpoch ||
		receipt.RecordedAt.IsZero() || receipt.ObservedAt.IsZero() || (evidenceSHA != "" && receipt.EvidenceSHA256 != evidenceSHA) {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	return receipt, nil
}

func failure(err error) error {
	if err == nil {
		return offline.ErrTerminalProof
	}
	return err
}
