package offline

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	TerminalChallengeSchemaV1   = "norn.fleet-offline-terminal-challenge/v1"
	TerminalAttestationSchemaV1 = "norn.fleet-offline-terminal-attestation/v1"
	TerminalChallengePurpose    = "terminal"
	terminalSignatureDomainV1   = "norn.fleet-offline-terminal-attestation/v1\x00"
)

var ErrTerminalAttestation = errors.New("offline terminal attestation is invalid, expired, or mismatched")

// TerminalChallengeV1 is an authority-created, one-use request for independent
// observation. The authority must persist it atomically against the current
// epoch/fence and enforce one terminal result per attempt. This data type does
// not perform that ledger CAS or grant an effect.
type TerminalChallengeV1 struct {
	SchemaVersion           string    `json:"schemaVersion"`
	Purpose                 string    `json:"purpose"`
	ID                      string    `json:"id"`
	PlanID                  string    `json:"planId"`
	AttemptID               string    `json:"attemptId"`
	GrantID                 string    `json:"grantId"`
	GrantSHA256             string    `json:"grantSHA256"`
	BindingSHA256           string    `json:"bindingSHA256"`
	DispatchNonceSHA256     string    `json:"dispatchNonceSHA256"`
	HolderID                string    `json:"holderId"`
	TargetID                string    `json:"targetId"`
	FenceGeneration         uint64    `json:"fenceGeneration"`
	AuthorityEpoch          int64     `json:"authorityEpoch"`
	SourceCommit            string    `json:"sourceCommit"`
	SavedPlanSHA256         string    `json:"savedPlanSHA256"`
	EffectSHA256            string    `json:"effectSHA256"`
	ProviderAccountUUID     string    `json:"providerAccountUUID"`
	NodeName                string    `json:"nodeName"`
	DropletID               string    `json:"dropletId"`
	VerifierSubject         string    `json:"verifierSubject"`
	ObserverSubject         string    `json:"observerSubject"`
	ObserverKeyID           string    `json:"observerKeyId"`
	TrustGeneration         uint64    `json:"trustGeneration"`
	ObserverPublicKeySHA256 string    `json:"observerPublicKeySHA256"`
	Nonce                   string    `json:"nonce"`
	IssuedAt                time.Time `json:"issuedAt"`
	ExpiresAt               time.Time `json:"expiresAt"`
}

// NewTerminalChallengeV1 accepts caller-supplied random nonce bytes so the
// authority can persist an idempotent challenge before returning it. The
// caller must use a cryptographic RNG; GenerateTerminalChallengeNonce helps.
func GenerateTerminalChallengeNonce() ([]byte, error) {
	nonce := make([]byte, 32)
	_, err := rand.Read(nonce)
	return nonce, err
}

func NewTerminalChallengeV1(g EffectGrant, verifierSubject, observerSubject, observerKeyID string, trustGeneration uint64, observerPublicKeySHA256, dispatchNonceSHA256, holderID string, nonce []byte, now time.Time) (TerminalChallengeV1, error) {
	if len(nonce) != 32 || !validTerminalGrant(g) || !validSubject(verifierSubject) || !validSubject(observerSubject) ||
		!validSubject(observerKeyID) || trustGeneration == 0 || !hex64.MatchString(observerPublicKeySHA256) ||
		!hex64.MatchString(dispatchNonceSHA256) || uuid.Validate(holderID) != nil ||
		now.IsZero() || now.Before(*g.ConsumedAt) {
		return TerminalChallengeV1{}, ErrTerminalAttestation
	}
	grantRaw, err := json.Marshal(g)
	if err != nil {
		return TerminalChallengeV1{}, ErrTerminalAttestation
	}
	c := TerminalChallengeV1{
		SchemaVersion: TerminalChallengeSchemaV1, Purpose: TerminalChallengePurpose,
		ID: uuid.NewString(), PlanID: g.PlanID,
		AttemptID: g.AttemptID, GrantID: g.ID, GrantSHA256: digestTerminalBytes(grantRaw),
		BindingSHA256: g.BindingSHA256, DispatchNonceSHA256: dispatchNonceSHA256,
		HolderID: holderID, TargetID: g.TargetID, FenceGeneration: g.FenceGeneration,
		AuthorityEpoch: g.AuthorityEpoch, SourceCommit: g.SourceCommit,
		SavedPlanSHA256: g.SavedPlanSHA256, EffectSHA256: g.EffectSHA256,
		ProviderAccountUUID: g.ProviderAccountUUID, NodeName: g.NodeName, DropletID: g.DropletID,
		VerifierSubject: verifierSubject, ObserverSubject: observerSubject,
		ObserverKeyID: observerKeyID, TrustGeneration: trustGeneration,
		ObserverPublicKeySHA256: observerPublicKeySHA256,
		Nonce:                   base64.RawURLEncoding.EncodeToString(nonce), IssuedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(5 * time.Minute),
	}
	if c.ValidateForGrant(g, now) != nil {
		return TerminalChallengeV1{}, ErrTerminalAttestation
	}
	return c, nil
}

func validSubject(subject string) bool {
	return subject != "" && len(subject) <= 256 && !strings.ContainsAny(subject, "\x00\r\n\t ")
}

func validTerminalGrant(g EffectGrant) bool {
	return g.SchemaVersion == EffectGrantSchema && g.Phase == "consumed" && g.ConsumedAt != nil &&
		uuid.Validate(g.PlanID) == nil && uuid.Validate(g.AttemptID) == nil && uuid.Validate(g.ID) == nil &&
		hex64.MatchString(g.BindingSHA256) && g.TargetID != "" && g.FenceGeneration > 0 && g.AuthorityEpoch > 0 &&
		hex40.MatchString(g.SourceCommit) && hex64.MatchString(g.SavedPlanSHA256) && hex64.MatchString(g.EffectSHA256) &&
		g.ProviderAccountUUID != "" && g.NodeName == WorkNode && g.DropletID == WorkDropletID &&
		!g.ConsumedAt.Before(g.IssuedAt) && g.ConsumedAt.Before(g.ExpiresAt)
}

func (c TerminalChallengeV1) ValidateForGrant(g EffectGrant, now time.Time) error {
	grantRaw, err := json.Marshal(g)
	decoded, nonceErr := base64.RawURLEncoding.Strict().DecodeString(c.Nonce)
	if err != nil || nonceErr != nil || len(decoded) != 32 || !validTerminalGrant(g) ||
		c.SchemaVersion != TerminalChallengeSchemaV1 || c.Purpose != TerminalChallengePurpose || uuid.Validate(c.ID) != nil ||
		c.PlanID != g.PlanID || c.AttemptID != g.AttemptID || c.GrantID != g.ID ||
		!equalDigest(c.GrantSHA256, digestTerminalBytes(grantRaw)) ||
		c.BindingSHA256 != g.BindingSHA256 || !hex64.MatchString(c.DispatchNonceSHA256) ||
		uuid.Validate(c.HolderID) != nil || c.TargetID != g.TargetID ||
		c.FenceGeneration != g.FenceGeneration || c.AuthorityEpoch != g.AuthorityEpoch ||
		c.SourceCommit != g.SourceCommit || c.SavedPlanSHA256 != g.SavedPlanSHA256 ||
		c.EffectSHA256 != g.EffectSHA256 || c.ProviderAccountUUID != g.ProviderAccountUUID ||
		c.NodeName != g.NodeName || c.DropletID != g.DropletID ||
		!validSubject(c.VerifierSubject) || !validSubject(c.ObserverSubject) ||
		!validSubject(c.ObserverKeyID) || c.TrustGeneration == 0 || !hex64.MatchString(c.ObserverPublicKeySHA256) ||
		c.IssuedAt.IsZero() || c.IssuedAt.Before(*g.ConsumedAt) ||
		!c.ExpiresAt.After(c.IssuedAt) || c.ExpiresAt.Sub(c.IssuedAt) > 5*time.Minute ||
		now.Before(c.IssuedAt) || !now.Before(c.ExpiresAt) {
		return ErrTerminalAttestation
	}
	return nil
}

// DecodeTerminalChallengeV1 refuses alternate JSON representations before an
// observer interprets a challenge. Its caller must also check the challenge
// against trusted authority state and the exact consumed grant.
func DecodeTerminalChallengeV1(raw []byte) (TerminalChallengeV1, error) {
	var c TerminalChallengeV1
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return c, ErrTerminalAttestation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return TerminalChallengeV1{}, ErrTerminalAttestation
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(raw, canonical) {
		return TerminalChallengeV1{}, ErrTerminalAttestation
	}
	return c, nil
}

// TerminalAttestationV1 distinguishes source-read digests from the original
// source commit and plan/effect digests. RawJournalSHA256 identifies the
// immutable observer-owned transcript; the receipt must retain that transcript.
type TerminalAttestationV1 struct {
	SchemaVersion           string              `json:"schemaVersion"`
	Purpose                 string              `json:"purpose"`
	ChallengeID             string              `json:"challengeId"`
	ChallengeSHA256         string              `json:"challengeSHA256"`
	Nonce                   string              `json:"nonce"`
	PlanID                  string              `json:"planId"`
	AttemptID               string              `json:"attemptId"`
	GrantID                 string              `json:"grantId"`
	GrantSHA256             string              `json:"grantSHA256"`
	BindingSHA256           string              `json:"bindingSHA256"`
	DispatchNonceSHA256     string              `json:"dispatchNonceSHA256"`
	HolderID                string              `json:"holderId"`
	TargetID                string              `json:"targetId"`
	FenceGeneration         uint64              `json:"fenceGeneration"`
	AuthorityEpoch          int64               `json:"authorityEpoch"`
	SourceCommit            string              `json:"sourceCommit"`
	SavedPlanSHA256         string              `json:"savedPlanSHA256"`
	EffectSHA256            string              `json:"effectSHA256"`
	ProviderAccountUUID     string              `json:"providerAccountUUID"`
	NodeName                string              `json:"nodeName"`
	DropletID               string              `json:"dropletId"`
	ObserverSubject         string              `json:"observerSubject"`
	ObserverKeyID           string              `json:"observerKeyId"`
	TrustGeneration         uint64              `json:"trustGeneration"`
	ObserverPublicKeySHA256 string              `json:"observerPublicKeySHA256"`
	ObservedFrom            time.Time           `json:"observedFrom"`
	ObservedTo              time.Time           `json:"observedTo"`
	ProviderEvidenceSHA256  string              `json:"providerEvidenceSHA256"`
	BackendEvidenceSHA256   string              `json:"backendEvidenceSHA256"`
	WorkloadEvidenceSHA256  string              `json:"workloadEvidenceSHA256"`
	RawJournalSHA256        string              `json:"rawJournalSHA256"`
	Observation             TerminalObservation `json:"observation"`
}

func NewTerminalAttestationV1(c TerminalChallengeV1, observation TerminalObservation, observedFrom time.Time, rawJournalSHA256 string) (TerminalAttestationV1, error) {
	challengeRaw, err := json.Marshal(c)
	if err != nil {
		return TerminalAttestationV1{}, ErrTerminalAttestation
	}
	a := TerminalAttestationV1{
		SchemaVersion: TerminalAttestationSchemaV1, Purpose: TerminalChallengePurpose, ChallengeID: c.ID,
		ChallengeSHA256: digestTerminalBytes(challengeRaw), Nonce: c.Nonce,
		PlanID: c.PlanID, AttemptID: c.AttemptID, GrantID: c.GrantID, GrantSHA256: c.GrantSHA256,
		BindingSHA256: c.BindingSHA256, DispatchNonceSHA256: c.DispatchNonceSHA256,
		HolderID: c.HolderID, TargetID: c.TargetID, FenceGeneration: c.FenceGeneration,
		AuthorityEpoch: c.AuthorityEpoch, SourceCommit: c.SourceCommit,
		SavedPlanSHA256: c.SavedPlanSHA256, EffectSHA256: c.EffectSHA256,
		ProviderAccountUUID: c.ProviderAccountUUID, NodeName: c.NodeName, DropletID: c.DropletID,
		ObserverSubject: c.ObserverSubject, ObserverKeyID: c.ObserverKeyID,
		TrustGeneration: c.TrustGeneration, ObserverPublicKeySHA256: c.ObserverPublicKeySHA256,
		ObservedFrom: observedFrom.UTC(),
		ObservedTo:   observation.ObservedAt.UTC(), ProviderEvidenceSHA256: observation.ProviderEvidenceSHA256,
		BackendEvidenceSHA256:  observation.BackendEvidenceSHA256,
		WorkloadEvidenceSHA256: observation.WorkloadEvidenceSHA256,
		RawJournalSHA256:       rawJournalSHA256, Observation: observation,
	}
	return a, nil
}

type SignedTerminalAttestationV1 struct {
	Attestation TerminalAttestationV1 `json:"attestation"`
	KeyID       string                `json:"keyId"`
	Signature   string                `json:"signature"`
}

type VerifiedTerminalAttestationV1 struct {
	attestationSHA256 string
	challengeID       string
}

func (v VerifiedTerminalAttestationV1) AttestationSHA256() string { return v.attestationSHA256 }
func (v VerifiedTerminalAttestationV1) ChallengeID() string       { return v.challengeID }

func digestTerminalBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func terminalSigningBytes(a TerminalAttestationV1, keyID string) ([]byte, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, ErrTerminalAttestation
	}
	message := append([]byte(terminalSignatureDomainV1), []byte(keyID)...)
	message = append(message, 0)
	return append(message, raw...), nil
}

// Signing is deliberately separate from validation against live authority
// state. The observer must first obtain and inspect a persisted challenge.
func SignTerminalAttestationV1(a TerminalAttestationV1, keyID string, private ed25519.PrivateKey) (SignedTerminalAttestationV1, error) {
	if a.SchemaVersion != TerminalAttestationSchemaV1 || a.Purpose != TerminalChallengePurpose ||
		!validSubject(keyID) || len(private) != ed25519.PrivateKeySize {
		return SignedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	if a.ObserverKeyID != keyID {
		return SignedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	message, err := terminalSigningBytes(a, keyID)
	if err != nil {
		return SignedTerminalAttestationV1{}, err
	}
	return SignedTerminalAttestationV1{Attestation: a, KeyID: keyID,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, message))}, nil
}

func VerifyTerminalAttestationV1(envelope SignedTerminalAttestationV1, c TerminalChallengeV1, g EffectGrant,
	pinnedObserverSubject, pinnedKeyID string, public ed25519.PublicKey, now time.Time) (VerifiedTerminalAttestationV1, error) {
	a := envelope.Attestation
	challengeRaw, err := json.Marshal(c)
	if err != nil || c.ValidateForGrant(g, now) != nil || len(public) != ed25519.PublicKeySize ||
		!validSubject(pinnedObserverSubject) || !validSubject(pinnedKeyID) ||
		c.ObserverSubject != pinnedObserverSubject || envelope.KeyID != pinnedKeyID || c.ObserverKeyID != pinnedKeyID ||
		!equalDigest(c.ObserverPublicKeySHA256, digestTerminalBytes(public)) ||
		a.SchemaVersion != TerminalAttestationSchemaV1 || a.Purpose != TerminalChallengePurpose || a.ChallengeID != c.ID ||
		!equalDigest(a.ChallengeSHA256, digestTerminalBytes(challengeRaw)) || a.Nonce != c.Nonce ||
		a.PlanID != c.PlanID || a.AttemptID != c.AttemptID || a.GrantID != c.GrantID ||
		!equalDigest(a.GrantSHA256, c.GrantSHA256) || !equalDigest(a.BindingSHA256, c.BindingSHA256) ||
		!equalDigest(a.DispatchNonceSHA256, c.DispatchNonceSHA256) || a.HolderID != c.HolderID ||
		a.TargetID != c.TargetID || a.FenceGeneration != c.FenceGeneration ||
		a.AuthorityEpoch != c.AuthorityEpoch || a.SourceCommit != c.SourceCommit ||
		!equalDigest(a.SavedPlanSHA256, c.SavedPlanSHA256) || !equalDigest(a.EffectSHA256, c.EffectSHA256) ||
		a.ProviderAccountUUID != c.ProviderAccountUUID || a.NodeName != c.NodeName ||
		a.DropletID != c.DropletID || a.ObserverSubject != c.ObserverSubject ||
		a.ObserverKeyID != c.ObserverKeyID || a.TrustGeneration != c.TrustGeneration ||
		!equalDigest(a.ObserverPublicKeySHA256, c.ObserverPublicKeySHA256) ||
		a.ObservedFrom.Before(c.IssuedAt) || a.ObservedTo.Before(a.ObservedFrom) ||
		a.ObservedTo.Sub(a.ObservedFrom) > 30*time.Second ||
		a.ObservedTo.After(now) || now.Sub(a.ObservedTo) > time.Minute ||
		!a.ObservedTo.Equal(a.Observation.ObservedAt) ||
		!equalDigest(a.ProviderEvidenceSHA256, a.Observation.ProviderEvidenceSHA256) ||
		!equalDigest(a.BackendEvidenceSHA256, a.Observation.BackendEvidenceSHA256) ||
		!equalDigest(a.WorkloadEvidenceSHA256, a.Observation.WorkloadEvidenceSHA256) ||
		!hex64.MatchString(a.RawJournalSHA256) ||
		ValidateTerminalObservation(a.Observation, g, now) != nil {
		return VerifiedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	signature, err := base64.RawStdEncoding.Strict().DecodeString(envelope.Signature)
	message, messageErr := terminalSigningBytes(a, envelope.KeyID)
	if err != nil || messageErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(public, message, signature) {
		return VerifiedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return VerifiedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	return VerifiedTerminalAttestationV1{attestationSHA256: digestTerminalBytes(raw), challengeID: c.ID}, nil
}

// DecodeSignedTerminalAttestationV1 admits only canonical JSON, rejecting
// unknown, duplicate and alternate spellings before signature verification.
func DecodeSignedTerminalAttestationV1(raw []byte) (SignedTerminalAttestationV1, error) {
	var envelope SignedTerminalAttestationV1
	if len(raw) == 0 || len(raw) > 32*1024 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return envelope, ErrTerminalAttestation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil {
		return SignedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(raw, canonical) {
		return SignedTerminalAttestationV1{}, ErrTerminalAttestation
	}
	return envelope, nil
}
