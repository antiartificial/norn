// Package offline defines the exact identity binding for a future offline
// destructive Fleet dispatch. It is deliberately data-only: validating this
// record does not verify an owner signature, acquire a target fence, mint a
// credential, or authorize an attempt. Callers must do those atomically at
// server admission before any effect can run.
package offline

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	BindingSchema   = "norn.fleet-offline-dispatch-binding/v1"
	BindingSchemaV2 = "norn.fleet-offline-dispatch-binding/v2"
	WorkResource    = "staging/nyc3"
	WorkNode        = "app-03"
	WorkDropletID   = "610327215"
	maxLifetime     = 15 * time.Minute
)

var (
	hex40              = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64              = regexp.MustCompile(`^[0-9a-f]{64}$`)
	positiveDecimal    = regexp.MustCompile(`^[1-9][0-9]*$`)
	ErrInvalidBinding  = errors.New("offline dispatch binding is incomplete or invalid")
	ErrAttemptMismatch = errors.New("offline attempt differs from its dispatch binding")
)

// Backend pins the immutable versioned object whose bytes were reviewed.
// A later effect gate must independently re-read the latest object version.
type Backend struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"versionId"`
	Lineage   string `json:"lineage"`
	Serial    uint64 `json:"serial"`
	RawSHA256 string `json:"rawSHA256"`
}

type ProviderNode struct {
	Name       string `json:"name"`
	ProviderID string `json:"providerId"`
}

var workNodeNames = []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"}

func validProviderNodes(nodes []ProviderNode, dropletID string) bool {
	if len(nodes) != len(workNodeNames) {
		return false
	}
	ids := map[string]bool{}
	for i, row := range nodes {
		if row.Name != workNodeNames[i] || !positiveDecimal.MatchString(row.ProviderID) || ids[row.ProviderID] ||
			(row.Name == WorkNode && row.ProviderID != dropletID) {
			return false
		}
		ids[row.ProviderID] = true
	}
	return true
}

// Binding carries every identity an offline destructive attempt must inherit.
// OwnerEnvelopeSHA256 is a *digest*, not proof of a valid owner signature.
type Binding struct {
	SchemaVersion          string         `json:"schemaVersion"`
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
	OwnerAuthorizationID   string         `json:"ownerAuthorizationId"`
	OwnerEnvelopeSHA256    string         `json:"ownerEnvelopeSHA256"`
	RunnerPrincipalSubject string         `json:"runnerPrincipalSubject"`
	DispatchNonceSHA256    string         `json:"dispatchNonceSHA256"`
	FenceGeneration        uint64         `json:"fenceGeneration"`
	CreatedAt              time.Time      `json:"createdAt"`
	ExpiresAt              time.Time      `json:"expiresAt"`
}

// Validate checks structural completeness and a bounded lifetime. It never
// asserts that the binding came from Norn or that its claims are true.
func (b Binding) Validate() error {
	if (b.SchemaVersion != BindingSchema && b.SchemaVersion != BindingSchemaV2) ||
		(b.SchemaVersion == BindingSchema && (b.CandidateSHA256 != "" || len(b.ProviderNodes) != 0)) ||
		(b.SchemaVersion == BindingSchemaV2 && (!hex64.MatchString(b.CandidateSHA256) || !validProviderNodes(b.ProviderNodes, b.DropletID))) ||
		uuid.Validate(b.PlanID) != nil || b.TargetID == "" ||
		b.Resource != WorkResource || !hex40.MatchString(b.SourceCommit) ||
		!hex64.MatchString(b.SavedPlanSHA256) || !hex64.MatchString(b.EffectSHA256) ||
		b.Backend.Bucket == "" || b.Backend.Key == "" || b.Backend.VersionID == "" ||
		b.Backend.Lineage == "" || b.Backend.Serial == 0 || !hex64.MatchString(b.Backend.RawSHA256) ||
		b.ProviderAccountUUID == "" || b.NodeName != WorkNode || b.DropletID != WorkDropletID ||
		!hex64.MatchString(b.OwnerAuthorizationID) ||
		!hex64.MatchString(b.OwnerEnvelopeSHA256) || b.RunnerPrincipalSubject == "" ||
		!hex64.MatchString(b.DispatchNonceSHA256) || b.FenceGeneration == 0 ||
		b.CreatedAt.IsZero() || b.ExpiresAt.IsZero() || !b.ExpiresAt.After(b.CreatedAt) ||
		b.ExpiresAt.Sub(b.CreatedAt) > maxLifetime || !positiveDecimal.MatchString(b.DropletID) {
		return ErrInvalidBinding
	}
	return nil
}

// DecodeBinding rejects unknown and duplicate fields. A caller must use this
// on untrusted request bytes.
func DecodeBinding(raw []byte) (Binding, error) {
	var b Binding
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return b, ErrInvalidBinding
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&b) != nil || b.Validate() != nil {
		return Binding{}, ErrInvalidBinding
	}
	return b, nil
}

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrInvalidBinding
			}
			seen[name] = true
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidBinding
	}
	_, err = decoder.Token() // matching delimiter; json.Valid checked structure
	return err
}

// AttemptClaim is presented by a distinct offline principal. The server must
// additionally prove that principal, dispatch, owner signature, and fence.
type AttemptClaim struct {
	PlanID                 string
	TargetID               string
	Resource               string
	SourceCommit           string
	SavedPlanSHA256        string
	EffectSHA256           string
	CandidateSHA256        string
	ProviderNodes          []ProviderNode
	Backend                Backend
	ProviderAccountUUID    string
	NodeName               string
	DropletID              string
	OwnerAuthorizationID   string
	OwnerEnvelopeSHA256    string
	RunnerPrincipalSubject string
	DispatchNonceSHA256    string
	FenceGeneration        uint64
}

// MatchAttempt checks exact dispatch inheritance and expiry, without granting
// authority. A wrong or expired claim fails closed before any credential grant.
func (b Binding) MatchAttempt(a AttemptClaim, now time.Time) error {
	if b.Validate() != nil || now.Before(b.CreatedAt) || !now.Before(b.ExpiresAt) ||
		b.PlanID != a.PlanID || b.TargetID != a.TargetID || b.Resource != a.Resource ||
		b.SourceCommit != a.SourceCommit || b.Backend != a.Backend ||
		b.ProviderAccountUUID != a.ProviderAccountUUID || b.NodeName != a.NodeName ||
		b.DropletID != a.DropletID || b.OwnerAuthorizationID != a.OwnerAuthorizationID ||
		b.RunnerPrincipalSubject != a.RunnerPrincipalSubject ||
		b.FenceGeneration != a.FenceGeneration ||
		!equalDigest(b.SavedPlanSHA256, a.SavedPlanSHA256) ||
		!equalDigest(b.EffectSHA256, a.EffectSHA256) ||
		b.CandidateSHA256 != a.CandidateSHA256 || !sameProviderNodes(b.ProviderNodes, a.ProviderNodes) ||
		!equalDigest(b.OwnerEnvelopeSHA256, a.OwnerEnvelopeSHA256) ||
		!equalDigest(b.DispatchNonceSHA256, a.DispatchNonceSHA256) {
		return ErrAttemptMismatch
	}
	return nil
}

func sameProviderNodes(a, b []ProviderNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDigest(a, b string) bool {
	return len(a) == 64 && len(b) == 64 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
