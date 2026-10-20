package offline

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	EffectChallengeSchemaV1      = "norn.fleet-offline-effect-challenge/v1"
	EffectAttestationSchemaV1    = "norn.fleet-offline-effect-attestation/v1"
	EffectIssuePurpose           = "effect-issue"
	EffectConsumePurpose         = "effect-consume"
	effectIssueSignatureDomain   = "norn.fleet-offline-effect-issue/v1\x00"
	effectConsumeSignatureDomain = "norn.fleet-offline-effect-consume/v1\x00"
)

var ErrEffectAttestation = errors.New("offline effect attestation is invalid, expired, or mismatched")

// EffectChallengeV1 is data only. Its issuer must persist a fresh challenge
// under the held target fence and check the current epoch again at acceptance.
// GrantID and GrantSHA256 are required only for consumption. CurrentBackend is
// the exact latest versioned read to be independently rechecked at acceptance.
type EffectChallengeV1 struct {
	SchemaVersion           string    `json:"schemaVersion"`
	Purpose                 string    `json:"purpose"`
	ID                      string    `json:"id"`
	PlanID                  string    `json:"planId"`
	AttemptID               string    `json:"attemptId"`
	BindingSHA256           string    `json:"bindingSHA256"`
	TargetID                string    `json:"targetId"`
	FenceGeneration         uint64    `json:"fenceGeneration"`
	AuthorityEpoch          int64     `json:"authorityEpoch"`
	SourceCommit            string    `json:"sourceCommit"`
	SavedPlanSHA256         string    `json:"savedPlanSHA256"`
	EffectSHA256            string    `json:"effectSHA256"`
	ProviderAccountUUID     string    `json:"providerAccountUUID"`
	NodeName                string    `json:"nodeName"`
	DropletID               string    `json:"dropletId"`
	RunnerSubject           string    `json:"runnerSubject"`
	ObserverSubject         string    `json:"observerSubject"`
	ObserverKeyID           string    `json:"observerKeyId"`
	ObserverPublicKeySHA256 string    `json:"observerPublicKeySHA256"`
	TrustGeneration         uint64    `json:"trustGeneration"`
	GrantID                 string    `json:"grantId,omitempty"`
	GrantSHA256             string    `json:"grantSHA256,omitempty"`
	CurrentBackend          Backend   `json:"currentBackend"`
	Nonce                   string    `json:"nonce"`
	IssuedAt                time.Time `json:"issuedAt"`
	ExpiresAt               time.Time `json:"expiresAt"`
}

func effectDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func validEffectBackend(b Backend) bool {
	return b.Bucket != "" && b.Key != "" && b.VersionID != "" && b.Lineage != "" && b.Serial > 0 && hex64.MatchString(b.RawSHA256)
}

func validIssuedEffectGrant(g EffectGrant, c EffectChallengeV1, now time.Time) bool {
	return g.SchemaVersion == EffectGrantSchema && g.Phase == "issued" && g.ConsumedAt == nil &&
		g.ID == c.GrantID && uuid.Validate(g.ID) == nil && g.PlanID == c.PlanID && g.AttemptID == c.AttemptID &&
		g.BindingSHA256 == c.BindingSHA256 && g.TargetID == c.TargetID &&
		g.FenceGeneration == c.FenceGeneration && g.AuthorityEpoch == c.AuthorityEpoch &&
		g.SourceCommit == c.SourceCommit && g.SavedPlanSHA256 == c.SavedPlanSHA256 &&
		g.EffectSHA256 == c.EffectSHA256 && g.Backend == c.CurrentBackend &&
		g.ProviderAccountUUID == c.ProviderAccountUUID && g.NodeName == c.NodeName && g.DropletID == c.DropletID &&
		hex64.MatchString(g.PrechangeSHA256) && hex64.MatchString(g.IndependentlyObservedSHA256) &&
		!g.IssuedAt.IsZero() && !g.ExpiresAt.IsZero() && g.ExpiresAt.After(g.IssuedAt) && now.Before(g.ExpiresAt)
}

func (c EffectChallengeV1) Validate(b Binding, a Attempt, epoch int64, now time.Time) error {
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(c.Nonce)
	if err != nil || len(nonce) != 32 || b.SchemaVersion != BindingSchemaV2 || b.Validate() != nil ||
		c.SchemaVersion != EffectChallengeSchemaV1 || (c.Purpose != EffectIssuePurpose && c.Purpose != EffectConsumePurpose) ||
		uuid.Validate(c.ID) != nil || c.PlanID != b.PlanID || c.AttemptID != a.ID || a.PlanID != b.PlanID ||
		c.BindingSHA256 != a.BindingSHA256 || c.TargetID != b.TargetID || c.FenceGeneration != b.FenceGeneration ||
		c.AuthorityEpoch != epoch || epoch <= 0 || c.SourceCommit != b.SourceCommit ||
		c.SavedPlanSHA256 != b.SavedPlanSHA256 || c.EffectSHA256 != b.EffectSHA256 ||
		c.ProviderAccountUUID != b.ProviderAccountUUID || c.NodeName != b.NodeName || c.DropletID != b.DropletID ||
		c.RunnerSubject != b.RunnerPrincipalSubject || a.PrincipalSubject != c.RunnerSubject ||
		!validSubject(c.ObserverSubject) || !validSubject(c.ObserverKeyID) || c.TrustGeneration == 0 ||
		!hex64.MatchString(c.ObserverPublicKeySHA256) || !validEffectBackend(c.CurrentBackend) ||
		c.CurrentBackend.Bucket != b.Backend.Bucket || c.CurrentBackend.Key != b.Backend.Key ||
		c.CurrentBackend.Lineage != b.Backend.Lineage || c.CurrentBackend != b.Backend ||
		c.IssuedAt.IsZero() || now.Before(c.IssuedAt) || !now.Before(c.ExpiresAt) ||
		c.ExpiresAt.Sub(c.IssuedAt) <= 0 || c.ExpiresAt.Sub(c.IssuedAt) > 5*time.Minute ||
		!now.Before(b.ExpiresAt) {
		return ErrEffectAttestation
	}
	if c.Purpose == EffectIssuePurpose && (c.GrantID != "" || c.GrantSHA256 != "") {
		return ErrEffectAttestation
	}
	if c.Purpose == EffectConsumePurpose && (uuid.Validate(c.GrantID) != nil || !hex64.MatchString(c.GrantSHA256)) {
		return ErrEffectAttestation
	}
	return nil
}

func NewEffectChallengeV1(purpose string, b Binding, a Attempt, epoch int64, observerSubject, keyID string, trustGeneration uint64, public ed25519.PublicKey, nonce []byte, grant *EffectGrant, now time.Time) (EffectChallengeV1, error) {
	if len(nonce) != 32 || len(public) != ed25519.PublicKeySize || (purpose == EffectConsumePurpose) != (grant != nil) {
		return EffectChallengeV1{}, ErrEffectAttestation
	}
	c := EffectChallengeV1{SchemaVersion: EffectChallengeSchemaV1, Purpose: purpose, ID: uuid.NewString(), PlanID: b.PlanID,
		AttemptID: a.ID, BindingSHA256: a.BindingSHA256, TargetID: b.TargetID, FenceGeneration: b.FenceGeneration,
		AuthorityEpoch: epoch, SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256,
		ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		RunnerSubject: a.PrincipalSubject, ObserverSubject: observerSubject, ObserverKeyID: keyID,
		ObserverPublicKeySHA256: effectDigest(public), TrustGeneration: trustGeneration, CurrentBackend: b.Backend,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), IssuedAt: now.UTC(), ExpiresAt: now.UTC().Add(5 * time.Minute)}
	if grant != nil {
		if grant.SchemaVersion != EffectGrantSchema || grant.Phase != "issued" || grant.ConsumedAt != nil || uuid.Validate(grant.ID) != nil ||
			!hex64.MatchString(grant.PrechangeSHA256) || !hex64.MatchString(grant.IndependentlyObservedSHA256) || grant.PlanID != b.PlanID ||
			grant.AttemptID != a.ID || grant.BindingSHA256 != a.BindingSHA256 || grant.TargetID != b.TargetID ||
			grant.FenceGeneration != b.FenceGeneration || grant.AuthorityEpoch != epoch || grant.Backend != b.Backend ||
			grant.SourceCommit != b.SourceCommit || grant.SavedPlanSHA256 != b.SavedPlanSHA256 || grant.EffectSHA256 != b.EffectSHA256 ||
			grant.ProviderAccountUUID != b.ProviderAccountUUID || grant.NodeName != b.NodeName || grant.DropletID != b.DropletID ||
			!now.Before(grant.ExpiresAt) {
			return EffectChallengeV1{}, ErrEffectAttestation
		}
		raw, err := json.Marshal(grant)
		if err != nil {
			return EffectChallengeV1{}, ErrEffectAttestation
		}
		c.GrantID, c.GrantSHA256 = grant.ID, effectDigest(raw)
		if grant.ExpiresAt.Before(c.ExpiresAt) {
			c.ExpiresAt = grant.ExpiresAt
		}
	}
	if c.Validate(b, a, epoch, now) != nil {
		return EffectChallengeV1{}, ErrEffectAttestation
	}
	return c, nil
}

func DecodeEffectChallengeV1(raw []byte) (EffectChallengeV1, error) {
	var c EffectChallengeV1
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) || rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return c, ErrEffectAttestation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return EffectChallengeV1{}, ErrEffectAttestation
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(raw, canonical) {
		return EffectChallengeV1{}, ErrEffectAttestation
	}
	return c, nil
}

// The four transcript digests are independent: a digest of the aggregate
// EffectObservation cannot stand in for source/provider/backend/workload reads.
type EffectAttestationV1 struct {
	SchemaVersion          string            `json:"schemaVersion"`
	Purpose                string            `json:"purpose"`
	ChallengeID            string            `json:"challengeId"`
	ChallengeSHA256        string            `json:"challengeSHA256"`
	Nonce                  string            `json:"nonce"`
	ObserverSubject        string            `json:"observerSubject"`
	ObserverKeyID          string            `json:"observerKeyId"`
	TrustGeneration        uint64            `json:"trustGeneration"`
	ObservedFrom           time.Time         `json:"observedFrom"`
	ObservedTo             time.Time         `json:"observedTo"`
	SourceEvidenceSHA256   string            `json:"sourceEvidenceSHA256"`
	ProviderEvidenceSHA256 string            `json:"providerEvidenceSHA256"`
	BackendEvidenceSHA256  string            `json:"backendEvidenceSHA256"`
	WorkloadEvidenceSHA256 string            `json:"workloadEvidenceSHA256"`
	RawJournalSHA256       string            `json:"rawJournalSHA256"`
	Observation            EffectObservation `json:"observation"`
}

type SignedEffectAttestationV1 struct {
	Attestation EffectAttestationV1 `json:"attestation"`
	KeyID       string              `json:"keyId"`
	Signature   string              `json:"signature"`
}

func NewEffectAttestationV1(c EffectChallengeV1, o EffectObservation, from time.Time, source, provider, backend, workload, journal string) (EffectAttestationV1, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return EffectAttestationV1{}, ErrEffectAttestation
	}
	return EffectAttestationV1{SchemaVersion: EffectAttestationSchemaV1, Purpose: c.Purpose, ChallengeID: c.ID,
		ChallengeSHA256: effectDigest(raw), Nonce: c.Nonce, ObserverSubject: c.ObserverSubject, ObserverKeyID: c.ObserverKeyID,
		TrustGeneration: c.TrustGeneration, ObservedFrom: from.UTC(), ObservedTo: o.ObservedAt.UTC(),
		SourceEvidenceSHA256: source, ProviderEvidenceSHA256: provider, BackendEvidenceSHA256: backend,
		WorkloadEvidenceSHA256: workload, RawJournalSHA256: journal, Observation: o}, nil
}

func effectSigningBytes(a EffectAttestationV1, keyID string) ([]byte, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, ErrEffectAttestation
	}
	domain := effectIssueSignatureDomain
	if a.Purpose == EffectConsumePurpose {
		domain = effectConsumeSignatureDomain
	} else if a.Purpose != EffectIssuePurpose {
		return nil, ErrEffectAttestation
	}
	msg := append([]byte(domain), []byte(keyID)...)
	msg = append(msg, 0)
	return append(msg, raw...), nil
}

func SignEffectAttestationV1(a EffectAttestationV1, keyID string, private ed25519.PrivateKey) (SignedEffectAttestationV1, error) {
	if a.SchemaVersion != EffectAttestationSchemaV1 || a.ObserverKeyID != keyID || !validSubject(keyID) || len(private) != ed25519.PrivateKeySize {
		return SignedEffectAttestationV1{}, ErrEffectAttestation
	}
	msg, err := effectSigningBytes(a, keyID)
	if err != nil {
		return SignedEffectAttestationV1{}, err
	}
	return SignedEffectAttestationV1{Attestation: a, KeyID: keyID, Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, msg))}, nil
}

// VerifyEffectAttestationV1 is deliberately independent of a ledger CAS. The
// caller must match the persisted challenge, held fence and current epoch.
func VerifyEffectAttestationV1(s SignedEffectAttestationV1, c EffectChallengeV1, b Binding, attempt Attempt, grant *EffectGrant,
	epoch int64, pinnedSubject, keyID string, public ed25519.PublicKey, now time.Time) (string, error) {
	a := s.Attestation
	challengeRaw, err := json.Marshal(c)
	if err != nil || c.Validate(b, attempt, epoch, now) != nil || len(public) != ed25519.PublicKeySize ||
		pinnedSubject != c.ObserverSubject || keyID != c.ObserverKeyID || s.KeyID != keyID ||
		!equalDigest(c.ObserverPublicKeySHA256, effectDigest(public)) ||
		a.SchemaVersion != EffectAttestationSchemaV1 || a.Purpose != c.Purpose || a.ChallengeID != c.ID ||
		!equalDigest(a.ChallengeSHA256, effectDigest(challengeRaw)) || a.Nonce != c.Nonce ||
		a.ObserverSubject != c.ObserverSubject || a.ObserverKeyID != keyID || a.TrustGeneration != c.TrustGeneration ||
		a.ObservedFrom.Before(c.IssuedAt) || a.ObservedTo.Before(a.ObservedFrom) ||
		a.ObservedTo.Sub(a.ObservedFrom) > 30*time.Second || a.ObservedTo.After(now) ||
		!hex64.MatchString(a.SourceEvidenceSHA256) || !hex64.MatchString(a.ProviderEvidenceSHA256) ||
		!hex64.MatchString(a.BackendEvidenceSHA256) || !hex64.MatchString(a.WorkloadEvidenceSHA256) ||
		!hex64.MatchString(a.RawJournalSHA256) || !a.ObservedTo.Equal(a.Observation.ObservedAt) ||
		a.Observation.Backend != c.CurrentBackend || ValidateEffectObservation(a.Observation, b, attempt, epoch, now) != nil {
		return "", ErrEffectAttestation
	}
	if c.Purpose == EffectIssuePurpose {
		if grant != nil || now.Sub(a.ObservedTo) > 30*time.Second {
			return "", ErrEffectAttestation
		}
	} else {
		if grant == nil || !validIssuedEffectGrant(*grant, c, now) || now.Sub(a.ObservedTo) > 10*time.Second {
			return "", ErrEffectAttestation
		}
		grantRaw, e := json.Marshal(grant)
		if e != nil || !equalDigest(c.GrantSHA256, effectDigest(grantRaw)) || grant.Backend != c.CurrentBackend {
			return "", ErrEffectAttestation
		}
	}
	msg, e := effectSigningBytes(a, keyID)
	sig, sigErr := base64.RawStdEncoding.Strict().DecodeString(s.Signature)
	if e != nil || sigErr != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(public, msg, sig) {
		return "", ErrEffectAttestation
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return "", ErrEffectAttestation
	}
	return effectDigest(raw), nil
}

func DecodeSignedEffectAttestationV1(raw []byte) (SignedEffectAttestationV1, error) {
	var s SignedEffectAttestationV1
	if len(raw) == 0 || len(raw) > 32*1024 || !json.Valid(raw) || rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return s, ErrEffectAttestation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil {
		return SignedEffectAttestationV1{}, ErrEffectAttestation
	}
	canonical, err := json.Marshal(s)
	if err != nil || !bytes.Equal(raw, canonical) {
		return SignedEffectAttestationV1{}, ErrEffectAttestation
	}
	return s, nil
}
