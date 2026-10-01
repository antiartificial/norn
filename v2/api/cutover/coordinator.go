package cutover

import (
	"context"
	"errors"
	"fmt"

	"norn/v2/api/archive"
)

// ErrActivationBlocked is returned whenever an activation cannot be proven
// safe. Callers must reconcile rather than retrying a possibly applied effect.
var ErrActivationBlocked = errors.New("database cutover activation is blocked pending reconciliation")

// ConsumerGeneration is the target consumer identity that an activation may
// install. CredentialDigest is a reference digest only; it never contains a
// credential value.
type ConsumerGeneration struct {
	Generation       uint64                    `json:"generation"`
	Target           DatabaseBindingGeneration `json:"target"`
	CredentialDigest string                    `json:"credentialDigest"`
}

// ActivationRequest is passed to the effect boundary. It carries the live
// operation claim and authority epoch so implementations can fence stale
// executors at their own database, credential and scheduler boundaries.
type ActivationRequest struct {
	OperationID    string                `json:"operationId"`
	AuthorityEpoch uint64                `json:"authorityEpoch"`
	Claim          OperationClaimBinding `json:"claim"`
	SourceFence    ExternalEffect        `json:"sourceFence"`
	FinalSync      ExternalEffect        `json:"finalSync"`
	Consumer       ConsumerGeneration    `json:"consumer"`
}

type ActivationObservation string

const (
	ActivationAbsent    ActivationObservation = "absent"
	ActivationInstalled ActivationObservation = "installed"
	ActivationConflict  ActivationObservation = "conflict"
)

// ActivationEffect is deliberately split into Apply and Observe. A process
// can die after Apply returns (or after the remote system applied it but
// before it returned); the next coordinator uses Observe and never issues a
// second generation change blindly.
type ActivationEffect interface {
	ObserveActivation(context.Context, ActivationRequest) (ActivationObservation, error)
	ApplyActivation(context.Context, ActivationRequest) error
}

// ActivationJournal is a durable CAS projection of the external authority
// log. AdvanceActivation must atomically persist exactly j.AdvanceWithEvidence
// and reject a stale current value.
type ActivationJournal interface {
	LoadActivationJournal(context.Context, string) (Journal, error)
	AdvanceActivationJournal(context.Context, Journal, PhaseEvidenceReference) (Journal, error)
}

// ActivationCoordinator only owns the FinalSync -> Activate boundary. The
// proof is retained before the effect, signed and verified against current
// journal state, and is bound to a single target consumer generation.
type ActivationCoordinator struct {
	Journal  ActivationJournal
	Archive  archive.Reader
	Verifier PhaseProofVerifier
	Effect   ActivationEffect
}

// ResumeActivation either installs exactly the signed consumer generation or
// records a prior successful installation. It never runs an external effect
// without a retained, verified activation proof and exact source-fence/final-
// sync assertions. A lost response remains blocked until Observe establishes
// the exact generation.
func (c ActivationCoordinator) ResumeActivation(ctx context.Context, operationID string, ref PhaseEvidenceReference) (Journal, error) {
	if c.Journal == nil || c.Archive == nil || c.Effect == nil || operationID == "" {
		return Journal{}, ErrActivationBlocked
	}
	j, err := c.Journal.LoadActivationJournal(ctx, operationID)
	if err != nil {
		return Journal{}, fmt.Errorf("%w: load journal: %v", ErrActivationBlocked, err)
	}
	if j.Intent.OperationID != operationID || j.Validate() != nil {
		return Journal{}, ErrActivationBlocked
	}
	if j.Phase != PhaseFinalSync && j.Phase != PhaseActivate {
		return Journal{}, fmt.Errorf("%w: journal phase is %s", ErrActivationBlocked, j.Phase)
	}
	// A committed activation may only be reported complete after its exact
	// external generation remains observable. This is the restart fence.
	if j.Phase == PhaseActivate {
		if j.EvidenceReferences[PhaseActivate] != ref {
			return Journal{}, ErrActivationBlocked
		}
		raw, err := ReadPhaseEvidence(ctx, c.Archive, j, ref)
		if err != nil {
			return Journal{}, fmt.Errorf("%w: read committed proof: %v", ErrActivationBlocked, err)
		}
		// Expiry does not erase an already committed boundary. Historical proof
		// verification plus the exact Observe below can report recovery, but this
		// branch never invokes ApplyActivation.
		proof, err := c.Verifier.VerifyHistoricalPhaseProof(ctx, journalBeforeActivation(j), ref, raw)
		if err != nil {
			return Journal{}, fmt.Errorf("%w: verify committed proof: %v", ErrActivationBlocked, err)
		}
		req, err := activationRequest(journalBeforeActivation(j), proof)
		if err != nil {
			return Journal{}, err
		}
		state, err := c.Effect.ObserveActivation(ctx, req)
		if err != nil || state != ActivationInstalled {
			return Journal{}, ErrActivationBlocked
		}
		return j, nil
	}
	raw, err := ReadProposedPhaseEvidence(ctx, c.Archive, j, ref)
	if err != nil {
		return Journal{}, fmt.Errorf("%w: read retained proof: %v", ErrActivationBlocked, err)
	}
	proof, err := c.Verifier.VerifyPhaseProof(ctx, j, ref, raw)
	historicalOnly := false
	if err != nil {
		// The remote generation may have committed before the response was
		// lost and before a restart. An expired permit cannot apply anything,
		// but its signed retained bytes can still identify the exact effect
		// that Observe is allowed to reconcile into the journal.
		proof, err = c.Verifier.VerifyHistoricalPhaseProof(ctx, j, ref, raw)
		if err != nil {
			return Journal{}, fmt.Errorf("%w: verify proof: %v", ErrActivationBlocked, err)
		}
		historicalOnly = true
	}
	if !historicalOnly && !proof.AuthorizesEffect() {
		return Journal{}, ErrActivationBlocked
	}
	req, err := activationRequest(j, proof)
	if err != nil {
		return Journal{}, err
	}
	state, err := c.Effect.ObserveActivation(ctx, req)
	if err != nil || state == ActivationConflict {
		return Journal{}, ErrActivationBlocked
	}
	if state == ActivationAbsent {
		if historicalOnly {
			return Journal{}, ErrActivationBlocked
		}
		if err := c.Effect.ApplyActivation(ctx, req); err != nil {
			return Journal{}, fmt.Errorf("%w: apply generation: %v", ErrActivationBlocked, err)
		}
		state, err = c.Effect.ObserveActivation(ctx, req)
		if err != nil || state != ActivationInstalled {
			return Journal{}, ErrActivationBlocked
		}
	}
	next, err := c.Journal.AdvanceActivationJournal(ctx, j, ref)
	if err != nil {
		return Journal{}, fmt.Errorf("%w: commit activation: %v", ErrActivationBlocked, err)
	}
	if next.Phase != PhaseActivate || next.EvidenceReferences[PhaseActivate] != ref {
		return Journal{}, ErrActivationBlocked
	}
	return next, nil
}

func journalBeforeActivation(j Journal) Journal {
	if j.Phase != PhaseActivate {
		return j
	}
	previous := j
	previous.Receipts = cloneReceipts(j.Receipts)
	previous.EvidenceReferences = cloneEvidenceReferences(j.EvidenceReferences)
	delete(previous.Receipts, PhaseActivate)
	delete(previous.EvidenceReferences, PhaseActivate)
	previous.Phase = PhaseFinalSync
	previous.Revision--
	return previous
}

func cloneReceipts(in map[Phase]string) map[Phase]string {
	out := make(map[Phase]string, len(in))
	for phase, digest := range in {
		out[phase] = digest
	}
	return out
}

func cloneEvidenceReferences(in map[Phase]PhaseEvidenceReference) map[Phase]PhaseEvidenceReference {
	out := make(map[Phase]PhaseEvidenceReference, len(in))
	for phase, ref := range in {
		out[phase] = ref
	}
	return out
}

func activationRequest(j Journal, proof *VerifiedPhaseProof) (ActivationRequest, error) {
	if proof == nil || !proof.Valid() || proof.Phase() != PhaseActivate || proof.JournalRevision() != j.Revision {
		return ActivationRequest{}, ErrActivationBlocked
	}
	p := proof.Proof()
	if p.Consumer.Generation == 0 || !validDigest(p.Consumer.CredentialDigest) || !sameBinding(p.Consumer.Target, j.Intent.Target) {
		return ActivationRequest{}, ErrActivationBlocked
	}
	var fence, sync ExternalEffect
	for _, effect := range p.Effects {
		switch effect.Kind {
		case "source-writer-fence":
			if fence.ID != "" {
				return ActivationRequest{}, ErrActivationBlocked
			}
			fence = effect
		case "final-sync-checkpoint":
			if sync.ID != "" {
				return ActivationRequest{}, ErrActivationBlocked
			}
			sync = effect
		}
	}
	if fence.ID == "" || sync.ID == "" {
		return ActivationRequest{}, ErrActivationBlocked
	}
	return ActivationRequest{OperationID: j.Intent.OperationID, AuthorityEpoch: j.Intent.AuthorityGeneration, Claim: p.Claim, SourceFence: fence, FinalSync: sync, Consumer: p.Consumer}, nil
}
