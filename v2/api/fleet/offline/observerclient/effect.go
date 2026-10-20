package observerclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/fleet/offline"
)

// EffectClient is deliberately separate from the terminal observer client.
// A concrete implementation must authenticate exact target receipts and may
// return ErrNotFound only for an authoritative, exact-attempt absence.
type EffectClient interface {
	ReadEffectChallenge(context.Context, string, string, string) (offline.EffectChallengeV1, error)
	ReadAcceptedEffectAttestation(context.Context, string, string, string) (AcceptedEffectAttestation, error)
	SubmitEffectAttestation(context.Context, string, string, offline.SignedEffectAttestationV1) error
}

// AcceptedAt must be the target ledger CAS time, not a proxy response time.
type AcceptedEffectAttestation struct {
	Signed     offline.SignedEffectAttestationV1
	AcceptedAt time.Time
}

// EffectReads are independently fetched raw source, provider, backend and
// workload transcripts. None may be populated from the runner's prechange
// receipt. Source implementations own the read-only clients and validation.
type EffectReads struct {
	Source, Provider, Backend, Workload []byte
	Observation                         offline.EffectObservation
}

type EffectSource interface {
	ObserveEffect(context.Context, offline.EffectChallengeV1, offline.Binding, offline.Attempt) (EffectReads, error)
}

// EffectJournal persists the exact transcripts before an attestation is sent.
// WriteOnce must durably fsync an owner-only record and its parent directory,
// reject a different value for an existing key, and return SHA-256(raw).
type EffectJournal interface {
	WriteOnce(context.Context, string, []byte) (string, error)
	Read(context.Context, string) ([]byte, error)
}

type EffectRunner struct {
	Client          EffectClient
	Source          EffectSource
	Journal         EffectJournal
	ObserverSubject string
	KeyID           string
	TrustGeneration uint64
	PrivateKey      ed25519.PrivateKey
	Clock           func() time.Time
}

// RunEffect signs one purpose-bound observation. A response-loss recovery
// reads the exact accepted signed record; it never submits a second PUT.
// Returning an attestation says nothing about grant issuance or consumption.
func (r EffectRunner) RunEffect(ctx context.Context, b offline.Binding, a offline.Attempt, epoch int64, grant *offline.EffectGrant, purpose string) (offline.SignedEffectAttestationV1, error) {
	var zero offline.SignedEffectAttestationV1
	if r.Client == nil || r.Source == nil || r.Journal == nil || r.Clock == nil ||
		r.ObserverSubject == "" || r.KeyID == "" || r.TrustGeneration == 0 || len(r.PrivateKey) != ed25519.PrivateKeySize ||
		(purpose != offline.EffectIssuePurpose && purpose != offline.EffectConsumePurpose) ||
		(purpose == offline.EffectConsumePurpose) != (grant != nil) {
		return zero, offline.ErrEffectAttestation
	}
	challenge, err := r.Client.ReadEffectChallenge(ctx, b.PlanID, a.ID, purpose)
	if err != nil {
		return zero, fmt.Errorf("read exact effect challenge: %w", err)
	}
	now := r.Clock().UTC()
	public := r.PrivateKey.Public().(ed25519.PublicKey)
	publicDigest := digestEffectBytes(public)
	if challenge.Purpose != purpose || challenge.ObserverSubject != r.ObserverSubject || challenge.ObserverKeyID != r.KeyID ||
		challenge.TrustGeneration != r.TrustGeneration || challenge.ObserverPublicKeySHA256 != publicDigest {
		return zero, offline.ErrEffectAttestation
	}
	if grant != nil {
		raw, e := json.Marshal(grant)
		if e != nil || challenge.GrantID != grant.ID || challenge.GrantSHA256 != digestEffectBytes(raw) {
			return zero, offline.ErrEffectAttestation
		}
	}
	signedRawPrior, signedReadErr := r.Journal.Read(ctx, challenge.ID+".signed")
	if signedReadErr != nil && !errors.Is(signedReadErr, ErrNotFound) {
		return zero, signedReadErr
	}
	accepted, err := r.Client.ReadAcceptedEffectAttestation(ctx, b.PlanID, a.ID, challenge.ID)
	if err == nil {
		if signedReadErr != nil {
			return zero, offline.ErrEffectAttestation
		}
		return r.requireAccepted(ctx, accepted, challenge, b, a, grant, epoch, digestEffectBytes(signedRawPrior))
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, fmt.Errorf("read exact effect attestation: %w", err)
	}
	if signedReadErr == nil {
		return zero, errors.New("signed effect attestation pending target reconciliation")
	}
	if challenge.Validate(b, a, epoch, now) != nil {
		return zero, offline.ErrEffectAttestation
	}
	observedFrom := r.Clock().UTC()
	reads, err := r.Source.ObserveEffect(ctx, challenge, b, a)
	if err != nil {
		return zero, err
	}
	if len(reads.Source) == 0 || len(reads.Provider) == 0 || len(reads.Backend) == 0 || len(reads.Workload) == 0 ||
		len(reads.Source) > 1<<20 || len(reads.Provider) > 1<<20 || len(reads.Backend) > 16<<20 || len(reads.Workload) > 16<<20 {
		return zero, offline.ErrEffectAttestation
	}
	// Recheck after independent reads; a stale or expired challenge is never
	// converted into a signed assertion.
	now = r.Clock().UTC()
	if challenge.Validate(b, a, epoch, now) != nil || offline.ValidateEffectObservation(reads.Observation, b, a, epoch, now) != nil ||
		reads.Observation.Backend != challenge.CurrentBackend {
		return zero, offline.ErrEffectAttestation
	}
	journalRaw, err := json.Marshal(struct {
		Schema      string `json:"schema"`
		ChallengeID string `json:"challengeId"`
		Source      []byte `json:"source"`
		Provider    []byte `json:"provider"`
		Backend     []byte `json:"backend"`
		Workload    []byte `json:"workload"`
	}{"norn.fleet-effect-observer-reads/v1", challenge.ID, reads.Source, reads.Provider, reads.Backend, reads.Workload})
	if err != nil {
		return zero, err
	}
	journalDigest, err := r.Journal.WriteOnce(ctx, challenge.ID, journalRaw)
	if err != nil || journalDigest != digestEffectBytes(journalRaw) {
		return zero, offline.ErrEffectAttestation
	}
	attestation, err := offline.NewEffectAttestationV1(challenge, reads.Observation, observedFrom,
		digestEffectBytes(reads.Source), digestEffectBytes(reads.Provider), digestEffectBytes(reads.Backend), digestEffectBytes(reads.Workload), journalDigest)
	if err != nil {
		return zero, err
	}
	signed, err := offline.SignEffectAttestationV1(attestation, r.KeyID, r.PrivateKey)
	if err != nil {
		return zero, err
	}
	signedRaw, err := json.Marshal(signed)
	if err != nil {
		return zero, err
	}
	expectedDigest := digestEffectBytes(signedRaw)
	if _, err = offline.VerifyEffectAttestationV1(signed, challenge, b, a, grant, epoch, r.ObserverSubject, r.KeyID, public, r.Clock().UTC()); err != nil {
		return zero, err
	}
	if persisted, journalErr := r.Journal.WriteOnce(ctx, challenge.ID+".signed", signedRaw); journalErr != nil || persisted != expectedDigest {
		return zero, offline.ErrEffectAttestation
	}
	if err = r.Client.SubmitEffectAttestation(ctx, b.PlanID, a.ID, signed); err != nil {
		// Ambiguous response is resolved by the target's durable exact receipt.
		accepted, err = r.Client.ReadAcceptedEffectAttestation(ctx, b.PlanID, a.ID, challenge.ID)
		if err != nil {
			return zero, fmt.Errorf("recover exact effect attestation: %w", err)
		}
		return r.requireAccepted(ctx, accepted, challenge, b, a, grant, epoch, expectedDigest)
	}
	accepted, err = r.Client.ReadAcceptedEffectAttestation(ctx, b.PlanID, a.ID, challenge.ID)
	if err != nil {
		return zero, fmt.Errorf("read accepted effect attestation: %w", err)
	}
	return r.requireAccepted(ctx, accepted, challenge, b, a, grant, epoch, expectedDigest)
}

func (r EffectRunner) requireAccepted(ctx context.Context, receipt AcceptedEffectAttestation, c offline.EffectChallengeV1, b offline.Binding, a offline.Attempt, grant *offline.EffectGrant, epoch int64, expected string) (offline.SignedEffectAttestationV1, error) {
	s := receipt.Signed
	raw, err := json.Marshal(s)
	if err != nil || expected == "" || !bytes.Equal([]byte(digestEffectBytes(raw)), []byte(expected)) ||
		receipt.AcceptedAt.Before(s.Attestation.ObservedTo) || !receipt.AcceptedAt.Before(c.ExpiresAt) {
		return offline.SignedEffectAttestationV1{}, offline.ErrEffectAttestation
	}
	public := r.PrivateKey.Public().(ed25519.PublicKey)
	if _, err := offline.VerifyEffectAttestationV1(s, c, b, a, grant, epoch, r.ObserverSubject, r.KeyID, public, receipt.AcceptedAt); err != nil {
		return offline.SignedEffectAttestationV1{}, err
	}
	rawJournal, err := r.Journal.Read(ctx, c.ID)
	if err != nil || digestEffectBytes(rawJournal) != s.Attestation.RawJournalSHA256 {
		return offline.SignedEffectAttestationV1{}, offline.ErrEffectAttestation
	}
	var evidence struct {
		Schema      string `json:"schema"`
		ChallengeID string `json:"challengeId"`
		Source      []byte `json:"source"`
		Provider    []byte `json:"provider"`
		Backend     []byte `json:"backend"`
		Workload    []byte `json:"workload"`
	}
	if json.Unmarshal(rawJournal, &evidence) != nil || evidence.Schema != "norn.fleet-effect-observer-reads/v1" || evidence.ChallengeID != c.ID ||
		digestEffectBytes(evidence.Source) != s.Attestation.SourceEvidenceSHA256 ||
		digestEffectBytes(evidence.Provider) != s.Attestation.ProviderEvidenceSHA256 ||
		digestEffectBytes(evidence.Backend) != s.Attestation.BackendEvidenceSHA256 ||
		digestEffectBytes(evidence.Workload) != s.Attestation.WorkloadEvidenceSHA256 {
		return offline.SignedEffectAttestationV1{}, offline.ErrEffectAttestation
	}
	return s, nil
}

func digestEffectBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
