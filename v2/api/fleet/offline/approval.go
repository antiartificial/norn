package offline

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const ApprovalSchema = "norn.fleet-offline-owner-approval/v1"
const ApprovalSchemaV2 = "norn.fleet-offline-owner-approval/v2"

var ErrInvalidApproval = errors.New("offline owner approval is invalid or does not match dispatch")

// ApprovalIntent is the owner's exact destructive intent. It deliberately
// omits Binding.OwnerEnvelopeSHA256 so the signed envelope can be hashed and
// that hash can be placed in the binding without a circular digest.
type ApprovalIntent struct {
	SchemaVersion          string         `json:"schemaVersion"`
	AuthorizationID        string         `json:"authorizationId"`
	AllowDestructive       bool           `json:"allowDestructive"`
	PlanID                 string         `json:"planId"`
	TargetID               string         `json:"targetId"`
	Resource               string         `json:"resource"`
	SourceCommit           string         `json:"sourceCommit"`
	SavedPlanSHA256        string         `json:"savedPlanSHA256"`
	EffectSHA256           string         `json:"effectSHA256"`
	CandidateSHA256        string         `json:"candidateSHA256,omitempty"`
	ProviderNodes          []ProviderNode `json:"providerNodes,omitempty"`
	Backend                Backend        `json:"backend"`
	ProviderAccountUUID    string         `json:"providerAccountUUID"`
	NodeName               string         `json:"nodeName"`
	DropletID              string         `json:"dropletId"`
	RunnerPrincipalSubject string         `json:"runnerPrincipalSubject"`
	DispatchNonceSHA256    string         `json:"dispatchNonceSHA256"`
	FenceGeneration        uint64         `json:"fenceGeneration"`
	ExpiresAt              time.Time      `json:"expiresAt"`
}

type SignedApproval struct {
	Intent    ApprovalIntent `json:"intent"`
	KeyID     string         `json:"keyId"`
	Signature string         `json:"signature"`
}

// VerifiedApproval cannot be constructed with valid private fields outside
// this package. A zero value never validates. It is valid only for one exact
// Binding and one authorizer ID, which the store consumes atomically.
type VerifiedApproval struct {
	authorizationID string
	envelopeSHA256  string
	bindingSHA256   string
}

func (v VerifiedApproval) AuthorizationID() string { return v.authorizationID }

func (v VerifiedApproval) Match(b Binding) bool {
	encoded, err := json.Marshal(b)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(encoded)
	return hex64.MatchString(v.authorizationID) && v.authorizationID == b.OwnerAuthorizationID &&
		equalDigest(v.envelopeSHA256, b.OwnerEnvelopeSHA256) &&
		equalDigest(v.bindingSHA256, hex.EncodeToString(digest[:]))
}

func DecodeSignedApproval(raw []byte) (SignedApproval, error) {
	var a SignedApproval
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return a, ErrInvalidApproval
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil {
		return SignedApproval{}, ErrInvalidApproval
	}
	return a, nil
}

// VerifySignedApproval requires a separately pinned Ed25519 public key. The
// caller supplies that key from trusted server configuration, never from the
// request. This proves signature and exact binding, but not live state.
func VerifySignedApproval(a SignedApproval, pinnedKeyID string, pinnedPublicKey ed25519.PublicKey,
	b Binding, now time.Time) (VerifiedApproval, error) {
	if b.Validate() != nil || len(pinnedPublicKey) != ed25519.PublicKeySize ||
		pinnedKeyID == "" || a.KeyID != pinnedKeyID ||
		((b.SchemaVersion == BindingSchema && a.Intent.SchemaVersion != ApprovalSchema) ||
			(b.SchemaVersion == BindingSchemaV2 && a.Intent.SchemaVersion != ApprovalSchemaV2)) ||
		!hex64.MatchString(a.Intent.AuthorizationID) ||
		a.Intent.AuthorizationID != b.OwnerAuthorizationID ||
		!a.Intent.AllowDestructive || a.Intent.PlanID != b.PlanID || a.Intent.TargetID != b.TargetID ||
		a.Intent.Resource != b.Resource || a.Intent.SourceCommit != b.SourceCommit ||
		!equalDigest(a.Intent.SavedPlanSHA256, b.SavedPlanSHA256) ||
		!equalDigest(a.Intent.EffectSHA256, b.EffectSHA256) ||
		a.Intent.CandidateSHA256 != b.CandidateSHA256 ||
		!sameProviderNodes(a.Intent.ProviderNodes, b.ProviderNodes) ||
		a.Intent.Backend != b.Backend || a.Intent.ProviderAccountUUID != b.ProviderAccountUUID ||
		a.Intent.NodeName != b.NodeName || a.Intent.DropletID != b.DropletID ||
		a.Intent.RunnerPrincipalSubject != b.RunnerPrincipalSubject ||
		!equalDigest(a.Intent.DispatchNonceSHA256, b.DispatchNonceSHA256) ||
		a.Intent.FenceGeneration != b.FenceGeneration ||
		!a.Intent.ExpiresAt.Equal(b.ExpiresAt) || now.Before(b.CreatedAt) || !now.Before(b.ExpiresAt) {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	intentBytes, err := json.Marshal(a.Intent)
	if err != nil {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(a.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(pinnedPublicKey, intentBytes, signature) {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	envelopeBytes, err := json.Marshal(a)
	if err != nil {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	digest := sha256.Sum256(envelopeBytes)
	value := hex.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(value), []byte(b.OwnerEnvelopeSHA256)) != 1 {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	bindingBytes, err := json.Marshal(b)
	if err != nil {
		return VerifiedApproval{}, ErrInvalidApproval
	}
	bindingDigest := sha256.Sum256(bindingBytes)
	return VerifiedApproval{authorizationID: a.Intent.AuthorizationID, envelopeSHA256: value,
		bindingSHA256: hex.EncodeToString(bindingDigest[:])}, nil
}
