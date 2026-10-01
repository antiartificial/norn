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
	AdvanceActivationJournal(context.Context, Journal, *VerifiedActivationCommit) (Journal, error)
}

// VerifiedActivationCommit is an in-process capability minted only after an
// exact installed-generation readback. It is the only value a future claimed
// adapter may accept for the Activate journal CAS; callers cannot assemble one
// from decoded proof bytes or an evidence reference.
type VerifiedActivationCommit struct {
	intentSHA      string
	journalRev     uint64
	reference      PhaseEvidenceReference
	authorityEpoch uint64
	claim          OperationClaimBinding
	consumer       ConsumerGeneration
	sourceFence    ExternalEffect
	finalSync      ExternalEffect
	marker         *struct{}
}

// ValidFor proves this exact pre-activation journal and evidence edge produced
// the capability. Adapters must still CAS their durable journal row/value.
func (c *VerifiedActivationCommit) ValidFor(j Journal) bool {
	if c == nil || c.marker == nil || j.Phase != PhaseFinalSync || j.Revision != c.journalRev || c.reference.NextPhase != PhaseActivate {
		return false
	}
	digest, err := IntentSHA256(j.Intent)
	if err != nil || digest != c.intentSHA || c.authorityEpoch != j.Intent.AuthorityGeneration || c.claim.OperationID != j.Intent.OperationID || c.claim.OwnerID == "" || c.claim.Generation == 0 || c.consumer.Generation == 0 || !validDigest(c.consumer.CredentialDigest) || !sameBinding(c.consumer.Target, j.Intent.Target) || c.sourceFence.ID == "" || !validDigest(c.sourceFence.SHA256) || c.finalSync.ID == "" || !validDigest(c.finalSync.SHA256) {
		return false
	}
	want, err := NewPhaseEvidenceReference(j, PhaseActivate, c.reference.EvidenceSHA256)
	return err == nil && want == c.reference
}

// EvidenceReference returns the bound immutable proof reference only from a
// valid capability. It is safe to copy, but cannot authorize a CAS by itself.
func (c *VerifiedActivationCommit) EvidenceReference() (PhaseEvidenceReference, bool) {
	if c == nil || c.marker == nil {
		return PhaseEvidenceReference{}, false
	}
	return c.reference, true
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
	commit, err := newVerifiedActivationCommit(j, ref, proof, req)
	if err != nil {
		return Journal{}, err
	}
	next, err := c.Journal.AdvanceActivationJournal(ctx, j, commit)
	if err != nil {
		return Journal{}, fmt.Errorf("%w: commit activation: %v", ErrActivationBlocked, err)
	}
	if next.Phase != PhaseActivate || next.EvidenceReferences[PhaseActivate] != ref {
		return Journal{}, ErrActivationBlocked
	}
	return next, nil
}

func newVerifiedActivationCommit(j Journal, ref PhaseEvidenceReference, proof *VerifiedPhaseProof, request ActivationRequest) (*VerifiedActivationCommit, error) {
	if proof == nil || !proof.Valid() || !proofMatchesActivationRequest(j, proof, request) {
		return nil, ErrActivationBlocked
	}
	intentSHA, err := IntentSHA256(j.Intent)
	if err != nil {
		return nil, ErrActivationBlocked
	}
	return &VerifiedActivationCommit{intentSHA: intentSHA, journalRev: j.Revision, reference: ref, authorityEpoch: request.AuthorityEpoch, claim: request.Claim, consumer: request.Consumer, sourceFence: request.SourceFence, finalSync: request.FinalSync, marker: &struct{}{}}, nil
}

func proofMatchesActivationRequest(j Journal, proof *VerifiedPhaseProof, request ActivationRequest) bool {
	if proof.Phase() != PhaseActivate || proof.JournalRevision() != j.Revision || request.OperationID != j.Intent.OperationID || request.AuthorityEpoch != j.Intent.AuthorityGeneration {
		return false
	}
	want, err := activationRequest(j, proof)
	return err == nil && want == request
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
