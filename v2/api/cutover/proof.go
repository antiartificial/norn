package cutover

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"norn/v2/api/database"
)

// PhaseProofSchema is the signed, secret-free external-effect statement used
// by a future coordinator. It is deliberately separate from a journal receipt:
// a receipt names retained bytes, while this document states what was observed.
const PhaseProofSchema = "norn.database-cutover-phase-proof/v1"

var ErrPhaseProofInvalid = errors.New("database cutover phase proof is invalid")

type ProofSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

// SignedAcceptedIntent binds the proof to the exact acceptance bytes. The
// verifier is injected because cutover cannot import the control-store package
// that owns acceptance-key retention.
type SignedAcceptedIntent struct {
	ID               string         `json:"id"`
	CanonicalBytes   []byte         `json:"canonicalBytes"`
	CanonicalSHA256  string         `json:"canonicalSha256"`
	Signature        ProofSignature `json:"signature"`
	OperationID      string         `json:"operationId"`
	OperationKind    string         `json:"operationKind"`
	App              string         `json:"app"`
	CandidateRelease string         `json:"candidateRelease"`
	IntentSHA256     string         `json:"intentSha256"`
}

type OperationClaimBinding struct {
	OperationID string `json:"operationId"`
	OwnerID     string `json:"ownerId"`
	Generation  uint64 `json:"generation"`
}

type ExternalEffect struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// PhaseProof has no credential or endpoint material. Effects identify exact
// provider/readback artifacts by stable IDs and digests.
type PhaseProof struct {
	SchemaVersion         string                    `json:"schemaVersion"`
	NextPhase             Phase                     `json:"nextPhase"`
	Accepted              SignedAcceptedIntent      `json:"accepted"`
	Claim                 OperationClaimBinding     `json:"claim"`
	AuthorityEpoch        uint64                    `json:"authorityEpoch"`
	JournalRevision       uint64                    `json:"journalRevision"`
	JournalReceiptSHA256  string                    `json:"journalReceiptSha256,omitempty"`
	CatalogRevision       int64                     `json:"catalogRevision"`
	CatalogDigest         string                    `json:"catalogDigest"`
	Source                DatabaseBindingGeneration `json:"source"`
	Target                DatabaseBindingGeneration `json:"target"`
	WriterInventorySHA256 string                    `json:"writerInventorySha256"`
	Effects               []ExternalEffect          `json:"effects"`
	ObserverID            string                    `json:"observerId"`
	ObservedAt            time.Time                 `json:"observedAt"`
	ExpiresAt             time.Time                 `json:"expiresAt"`
	Signature             ProofSignature            `json:"signature"`
}

// DatabaseBindingGeneration purposefully mirrors only the existing immutable
// target identity fields that establish a binding generation.
type DatabaseBindingGeneration struct {
	ServiceID         string `json:"serviceId"`
	ServiceGeneration uint64 `json:"serviceGeneration"`
	BindingID         string `json:"bindingId"`
	BindingGeneration uint64 `json:"bindingGeneration"`
}

type AcceptanceProofVerifier interface {
	VerifyAcceptedIntent(context.Context, SignedAcceptedIntent) error
}
type EffectReadbackVerifier interface {
	VerifyEffect(context.Context, ExternalEffect) error
}

type PhaseProofVerifier struct {
	TrustedObserverKeyID string
	TrustedObserverKey   ed25519.PublicKey
	Acceptance           AcceptanceProofVerifier
	Readback             EffectReadbackVerifier
	Now                  func() time.Time
}

// VerifiedPhaseProof is an in-process capability. Its marker cannot be set by
// packages outside cutover, so callers cannot substitute decoded JSON for a
// verifier result.
type VerifiedPhaseProof struct {
	proof  PhaseProof
	marker *struct{}
}

func (p *VerifiedPhaseProof) Valid() bool { return p != nil && p.marker != nil }
func (p *VerifiedPhaseProof) Phase() Phase {
	if !p.Valid() {
		return ""
	}
	return p.proof.NextPhase
}
func (p *VerifiedPhaseProof) JournalRevision() uint64 {
	if !p.Valid() {
		return 0
	}
	return p.proof.JournalRevision
}
func (p *VerifiedPhaseProof) Proof() PhaseProof {
	if !p.Valid() {
		return PhaseProof{}
	}
	return cloneProof(p.proof)
}

// VerifyPhaseProof checks the archive edge, the pinned observer signature,
// accepted-intent signature, expiry, exact journal/catalog binding, and every
// external-effect readback. It does not advance a journal.
func (v PhaseProofVerifier) VerifyPhaseProof(ctx context.Context, j Journal, ref PhaseEvidenceReference, raw []byte) (*VerifiedPhaseProof, error) {
	if v.Acceptance == nil || v.Readback == nil || len(v.TrustedObserverKey) != ed25519.PublicKeySize || v.TrustedObserverKeyID == "" || len(raw) == 0 {
		return nil, ErrPhaseProofInvalid
	}
	if err := j.Validate(); err != nil {
		return nil, fmt.Errorf("%w: journal: %v", ErrPhaseProofInvalid, err)
	}
	if err := validPhaseProofReference(j, ref, raw); err != nil {
		return nil, err
	}
	var proof PhaseProof
	if err := json.Unmarshal(raw, &proof); err != nil {
		return nil, fmt.Errorf("%w: decode", ErrPhaseProofInvalid)
	}
	if err := v.verify(ctx, j, ref, proof); err != nil {
		return nil, err
	}
	return &VerifiedPhaseProof{proof: cloneProof(proof), marker: &struct{}{}}, nil
}

func (v PhaseProofVerifier) verify(ctx context.Context, j Journal, ref PhaseEvidenceReference, p PhaseProof) error {
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	if p.SchemaVersion != PhaseProofSchema || p.NextPhase != ref.NextPhase || p.JournalRevision != j.Revision || p.JournalReceiptSHA256 != j.Receipts[j.Phase] || p.AuthorityEpoch != j.Intent.AuthorityGeneration || p.CatalogRevision != j.Intent.CatalogRevision || p.CatalogDigest != j.Intent.CatalogDigest || p.WriterInventorySHA256 != j.Intent.WriterInventorySHA256 || p.Accepted.OperationID != j.Intent.OperationID || p.Accepted.OperationKind != "database.cutover" || p.Accepted.App != j.Intent.App || p.Accepted.CandidateRelease != j.Intent.CandidateRelease || p.Accepted.IntentSHA256 != ref.IntentSHA256 || p.Claim.OperationID != j.Intent.OperationID || p.Claim.OwnerID == "" || p.Claim.Generation == 0 || p.ObserverID == "" || !p.ObservedAt.Before(p.ExpiresAt) || now.Before(p.ObservedAt) || !now.Before(p.ExpiresAt) || p.Signature.Algorithm != "ed25519" || p.Signature.KeyID != v.TrustedObserverKeyID {
		return ErrPhaseProofInvalid
	}
	if !sameBinding(p.Source, j.Intent.Source) || !sameBinding(p.Target, j.Intent.Target) || !validDigest(p.Accepted.CanonicalSHA256) || digestBytes(p.Accepted.CanonicalBytes) != p.Accepted.CanonicalSHA256 {
		return ErrPhaseProofInvalid
	}
	if err := v.Acceptance.VerifyAcceptedIntent(ctx, p.Accepted); err != nil {
		return fmt.Errorf("%w: accepted intent: %v", ErrPhaseProofInvalid, err)
	}
	canonical, err := CanonicalPhaseProof(p)
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(p.Signature.Value)
	if err != nil || !ed25519.Verify(v.TrustedObserverKey, canonical, sig) {
		return ErrPhaseProofInvalid
	}
	if len(p.Effects) == 0 {
		return ErrPhaseProofInvalid
	}
	seen := map[string]bool{}
	for _, effect := range p.Effects {
		if effect.ID == "" || effect.Kind == "" || !validDigest(effect.SHA256) || seen[effect.ID] {
			return ErrPhaseProofInvalid
		}
		seen[effect.ID] = true
		if err := v.Readback.VerifyEffect(ctx, effect); err != nil {
			return fmt.Errorf("%w: effect %s: %v", ErrPhaseProofInvalid, effect.ID, err)
		}
	}
	return nil
}

func validPhaseProofReference(j Journal, ref PhaseEvidenceReference, raw []byte) error {
	want, err := NewPhaseEvidenceReference(j, ref.NextPhase, digestBytes(raw))
	if err != nil || want != ref || ref.NextPhase == PhasePrepare {
		return ErrPhaseProofInvalid
	}
	return nil
}
func sameBinding(p DatabaseBindingGeneration, t database.TargetIdentity) bool {
	return p.ServiceID == t.ServiceID && p.ServiceGeneration == t.ServiceGeneration && p.BindingID == t.BindingID && p.BindingGeneration == t.BindingGeneration
}
func digestBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// CanonicalPhaseProof returns the exact observer-signing bytes. It clears the
// observer signature and orders effects by their stable ID before encoding.
func CanonicalPhaseProof(p PhaseProof) ([]byte, error) {
	p.Signature = ProofSignature{}
	sort.Slice(p.Effects, func(i, j int) bool { return p.Effects[i].ID < p.Effects[j].ID })
	return json.Marshal(p)
}
func cloneProof(p PhaseProof) PhaseProof {
	p.Accepted.CanonicalBytes = append([]byte(nil), p.Accepted.CanonicalBytes...)
	p.Effects = append([]ExternalEffect(nil), p.Effects...)
	return p
}
