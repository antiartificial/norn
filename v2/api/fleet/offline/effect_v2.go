package offline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	EffectGrantSchemaV2        = "norn.fleet-offline-effect-grant/v2"
	DeleteCommitmentSchemaV2   = "norn.fleet-offline-delete-commitment/v2"
	DeleteDispatchSchemaV2     = "norn.fleet-offline-delete-dispatch/v2"
	TerminalEvidenceSchemaV2   = "norn.fleet-offline-terminal-evidence/v2"
	CompletionEvidenceSchemaV2 = "norn.fleet-offline-completion-evidence/v2"
	DeleteRetryPolicyV2        = "never-reissue-after-dispatch-reservation"
)

var (
	ErrEffectProofV2      = errors.New("independent offline effect-time proof is incomplete or mismatched")
	ErrDeleteCommitmentV2 = errors.New("offline delete commitment is incomplete or mismatched")
	ErrTerminalProofV2    = errors.New("independent offline terminal proof is incomplete or mismatched")
	ErrCompletionProofV2  = errors.New("independent offline completion proof is incomplete or mismatched")
)

type WriterFence struct {
	BackendID     string `json:"backendId"`
	Generation    uint64 `json:"generation"`
	Holder        string `json:"holder"`
	SessionSHA256 string `json:"sessionSHA256"`
}

func (w WriterFence) valid(attemptID string) bool {
	return w.BackendID != "" && w.Generation > 0 && w.Holder == attemptID && hex64.MatchString(w.SessionSHA256)
}

// EffectObservationV2 is populated only by an authority-configured evidence
// source. The HTTP caller cannot submit it. FenceGeneration is the Norn target
// fence; WriterFence.Generation is the distinct PostgreSQL all-writer fence.
type EffectObservationV2 struct {
	PlanID, AttemptID, BindingSHA256, TargetID                         string
	SourceCommit, SavedPlanSHA256, EffectSHA256                        string
	Backend                                                            Backend
	ProviderAccountUUID, NodeName, DropletID                           string
	FenceGeneration                                                    uint64
	AuthorityEpoch                                                     int64
	WriterFence                                                        WriterFence
	ObservedAt, ValidUntil                                             time.Time
	EvidenceSHA256                                                     string
	ProviderExact, LatestBackendExact, AllNamespacesInspected          bool
	TargetEmpty, SurvivingPlacement, SurvivingHeadroom, ServicePassing bool
}

type VerifiedEffectObservationV2 struct{ value EffectObservationV2 }

func VerifyEffectObservationV2(o EffectObservationV2, b Binding, a Attempt, epoch int64, now time.Time) (VerifiedEffectObservationV2, error) {
	if b.SchemaVersion != BindingSchemaV2 || b.Validate() != nil || a.ID == "" ||
		o.PlanID != b.PlanID || o.AttemptID != a.ID || o.BindingSHA256 != a.BindingSHA256 ||
		o.TargetID != b.TargetID || o.FenceGeneration != b.FenceGeneration || o.AuthorityEpoch != epoch ||
		o.SourceCommit != b.SourceCommit || o.SavedPlanSHA256 != b.SavedPlanSHA256 || o.EffectSHA256 != b.EffectSHA256 ||
		o.Backend != b.Backend || o.ProviderAccountUUID != b.ProviderAccountUUID || o.NodeName != b.NodeName || o.DropletID != b.DropletID ||
		!o.WriterFence.valid(a.ID) || !hex64.MatchString(o.EvidenceSHA256) || o.ObservedAt.IsZero() ||
		o.ValidUntil.IsZero() || now.Before(o.ObservedAt) || now.Sub(o.ObservedAt) > 30*time.Second ||
		!now.Before(o.ValidUntil) || o.ValidUntil.Sub(o.ObservedAt) > 30*time.Second ||
		!o.ProviderExact || !o.LatestBackendExact || !o.AllNamespacesInspected || !o.TargetEmpty ||
		!o.SurvivingPlacement || !o.SurvivingHeadroom || !o.ServicePassing {
		return VerifiedEffectObservationV2{}, ErrEffectProofV2
	}
	return VerifiedEffectObservationV2{value: o}, nil
}

func (v VerifiedEffectObservationV2) Match(b Binding, a Attempt, epoch int64, now time.Time) (EffectObservationV2, bool) {
	_, err := VerifyEffectObservationV2(v.value, b, a, epoch, now)
	return v.value, err == nil
}

type EffectEvidenceV2 struct {
	SHA256     string    `json:"sha256"`
	ObservedAt time.Time `json:"observedAt"`
	ValidUntil time.Time `json:"validUntil"`
}

type DeleteCommitmentV2 struct {
	SchemaVersion       string    `json:"schemaVersion"`
	SHA256              string    `json:"sha256"`
	DeleteRequestSHA256 string    `json:"deleteRequestSHA256"`
	CommittedAt         time.Time `json:"committedAt"`
}

type DeleteDispatchV2 struct {
	SchemaVersion         string      `json:"schemaVersion"`
	PlanID                string      `json:"planId"`
	AttemptID             string      `json:"attemptId"`
	GrantID               string      `json:"grantId"`
	CommitmentSHA256      string      `json:"commitmentSHA256"`
	TargetID              string      `json:"targetId"`
	TargetFenceGeneration uint64      `json:"targetFenceGeneration"`
	WriterFence           WriterFence `json:"writerFence"`
	AuthorityEpoch        int64       `json:"authorityEpoch"`
	RetryPolicy           string      `json:"retryPolicy"`
	AdmittedAt            time.Time   `json:"admittedAt"`
}

type TerminalObservationV2 struct {
	PlanID, AttemptID, GrantID, BindingSHA256, TargetID                                                 string
	TargetFenceGeneration                                                                               uint64
	AuthorityEpoch                                                                                      int64
	WriterFence                                                                                         WriterFence
	ObservedAt                                                                                          time.Time
	EvidenceSHA256, ProviderEvidenceSHA256, BackendEvidenceSHA256, WorkloadEvidenceSHA256               string
	ProviderAbsent, BackendAdvanced, BackendTargetAbsent                                                bool
	AllNamespacesInspected, WorkloadTargetAbsent, SurvivingPlacement, SurvivingHeadroom, ServicePassing bool
}

type VerifiedTerminalObservationV2 struct{ value TerminalObservationV2 }

func VerifyTerminalObservationV2(o TerminalObservationV2, g EffectGrantV2, now time.Time) (VerifiedTerminalObservationV2, error) {
	if g.SchemaVersion != EffectGrantSchemaV2 || g.Phase != "delete_dispatched" || g.Dispatch == nil ||
		o.PlanID != g.PlanID || o.AttemptID != g.AttemptID || o.GrantID != g.ID ||
		o.BindingSHA256 != g.BindingSHA256 || o.TargetID != g.TargetID ||
		o.TargetFenceGeneration != g.TargetFenceGeneration || o.AuthorityEpoch != g.AuthorityEpoch ||
		o.WriterFence != g.WriterFence || !o.WriterFence.valid(g.AttemptID) ||
		o.ObservedAt.Before(g.Dispatch.AdmittedAt) || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > time.Minute ||
		!hex64.MatchString(o.EvidenceSHA256) || !hex64.MatchString(o.ProviderEvidenceSHA256) ||
		!hex64.MatchString(o.BackendEvidenceSHA256) || !hex64.MatchString(o.WorkloadEvidenceSHA256) ||
		!o.ProviderAbsent || !o.BackendAdvanced || !o.BackendTargetAbsent || !o.AllNamespacesInspected ||
		!o.WorkloadTargetAbsent || !o.SurvivingPlacement || !o.SurvivingHeadroom || !o.ServicePassing {
		return VerifiedTerminalObservationV2{}, ErrTerminalProofV2
	}
	return VerifiedTerminalObservationV2{value: o}, nil
}

func (v VerifiedTerminalObservationV2) Match(g EffectGrantV2, now time.Time) (TerminalObservationV2, bool) {
	_, err := VerifyTerminalObservationV2(v.value, g, now)
	return v.value, err == nil
}

type TerminalEvidenceV2 struct {
	SchemaVersion          string      `json:"schemaVersion"`
	PlanID                 string      `json:"planId"`
	AttemptID              string      `json:"attemptId"`
	GrantID                string      `json:"grantId"`
	BindingSHA256          string      `json:"bindingSHA256"`
	TargetID               string      `json:"targetId"`
	TargetFenceGeneration  uint64      `json:"targetFenceGeneration"`
	WriterFence            WriterFence `json:"writerFence"`
	AuthorityEpoch         int64       `json:"authorityEpoch"`
	ObservedAt             time.Time   `json:"observedAt"`
	RecordedAt             time.Time   `json:"recordedAt"`
	EvidenceSHA256         string      `json:"evidenceSHA256"`
	ProviderEvidenceSHA256 string      `json:"providerEvidenceSHA256"`
	BackendEvidenceSHA256  string      `json:"backendEvidenceSHA256"`
	WorkloadEvidenceSHA256 string      `json:"workloadEvidenceSHA256"`
}

// CompletionObservationV2 is populated only by the independent evidence
// source after PostgreSQL has durably released the exact all-writer fence.
// ReceiptSHA256 identifies that PostgreSQL release receipt.
type CompletionObservationV2 struct {
	PlanID, AttemptID, GrantID, BindingSHA256, TargetID string
	TargetFenceGeneration                               uint64
	AuthorityEpoch                                      int64
	WriterFence                                         WriterFence
	TerminalEvidenceSHA256, ReceiptSHA256               string
	ReleasedAt, ObservedAt                              time.Time
}

type CompletionEvidenceV2 struct {
	SchemaVersion          string      `json:"schemaVersion"`
	PlanID                 string      `json:"planId"`
	AttemptID              string      `json:"attemptId"`
	GrantID                string      `json:"grantId"`
	BindingSHA256          string      `json:"bindingSHA256"`
	TargetID               string      `json:"targetId"`
	TargetFenceGeneration  uint64      `json:"targetFenceGeneration"`
	AuthorityEpoch         int64       `json:"authorityEpoch"`
	WriterFence            WriterFence `json:"writerFence"`
	TerminalEvidenceSHA256 string      `json:"terminalEvidenceSHA256"`
	ReceiptSHA256          string      `json:"receiptSHA256"`
	ReleasedAt             time.Time   `json:"releasedAt"`
	ObservedAt             time.Time   `json:"observedAt"`
	RecordedAt             time.Time   `json:"recordedAt"`
}

func (c CompletionEvidenceV2) Validate(a Attempt) error {
	if c.SchemaVersion != CompletionEvidenceSchemaV2 || c.PlanID != a.PlanID || c.AttemptID != a.ID ||
		uuid.Validate(c.GrantID) != nil || c.BindingSHA256 != a.BindingSHA256 || c.TargetID != a.TargetID ||
		c.TargetFenceGeneration != a.FenceGeneration || c.AuthorityEpoch <= 0 || !c.WriterFence.valid(a.ID) ||
		!hex64.MatchString(c.TerminalEvidenceSHA256) || !hex64.MatchString(c.ReceiptSHA256) ||
		c.ReleasedAt.IsZero() || c.ObservedAt.Before(c.ReleasedAt) || c.RecordedAt.Before(c.ObservedAt) {
		return ErrCompletionProofV2
	}
	return nil
}

func VerifyCompletionObservationV2(o CompletionObservationV2, g EffectGrantV2, now time.Time) (CompletionEvidenceV2, error) {
	if g.Validate() != nil || g.Phase != "terminal_verified" || g.Terminal == nil ||
		o.PlanID != g.PlanID || o.AttemptID != g.AttemptID || o.GrantID != g.ID ||
		o.BindingSHA256 != g.BindingSHA256 || o.TargetID != g.TargetID ||
		o.TargetFenceGeneration != g.TargetFenceGeneration || o.AuthorityEpoch != g.AuthorityEpoch ||
		o.WriterFence != g.WriterFence || o.TerminalEvidenceSHA256 != g.Terminal.EvidenceSHA256 ||
		!hex64.MatchString(o.ReceiptSHA256) || o.ReleasedAt.Before(g.Terminal.RecordedAt) ||
		o.ObservedAt.Before(o.ReleasedAt) || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > 30*time.Second {
		return CompletionEvidenceV2{}, ErrCompletionProofV2
	}
	receipt := CompletionEvidenceV2{SchemaVersion: CompletionEvidenceSchemaV2, PlanID: o.PlanID, AttemptID: o.AttemptID,
		GrantID: o.GrantID, BindingSHA256: o.BindingSHA256, TargetID: o.TargetID,
		TargetFenceGeneration: o.TargetFenceGeneration, AuthorityEpoch: o.AuthorityEpoch, WriterFence: o.WriterFence,
		TerminalEvidenceSHA256: o.TerminalEvidenceSHA256, ReceiptSHA256: o.ReceiptSHA256,
		ReleasedAt: o.ReleasedAt.UTC(), ObservedAt: o.ObservedAt.UTC(), RecordedAt: now.UTC()}
	if receipt.Validate(Attempt{ID: g.AttemptID, PlanID: g.PlanID, BindingSHA256: g.BindingSHA256,
		TargetID: g.TargetID, FenceGeneration: g.TargetFenceGeneration}) != nil {
		return CompletionEvidenceV2{}, ErrCompletionProofV2
	}
	return receipt, nil
}

type EffectGrantV2 struct {
	SchemaVersion         string              `json:"schemaVersion"`
	ID                    string              `json:"id"`
	PlanID                string              `json:"planId"`
	AttemptID             string              `json:"attemptId"`
	BindingSHA256         string              `json:"bindingSHA256"`
	TargetID              string              `json:"targetId"`
	TargetFenceGeneration uint64              `json:"targetFenceGeneration"`
	AuthorityEpoch        int64               `json:"authorityEpoch"`
	OwnerAuthorizationID  string              `json:"ownerAuthorizationId"`
	SourceCommit          string              `json:"sourceCommit"`
	SavedPlanSHA256       string              `json:"savedPlanSHA256"`
	EffectSHA256          string              `json:"effectSHA256"`
	Backend               Backend             `json:"backend"`
	ProviderAccountUUID   string              `json:"providerAccountUUID"`
	NodeName              string              `json:"nodeName"`
	DropletID             string              `json:"dropletId"`
	RunnerSubject         string              `json:"runnerSubject"`
	RunnerTokenID         string              `json:"runnerTokenId"`
	RunnerDeviceID        string              `json:"runnerDeviceId"`
	WriterFence           WriterFence         `json:"writerFence"`
	EffectEvidence        EffectEvidenceV2    `json:"effectEvidence"`
	DeleteRequestSHA256   string              `json:"deleteRequestSHA256"`
	Phase                 string              `json:"phase"`
	IssuedAt              time.Time           `json:"issuedAt"`
	ExpiresAt             time.Time           `json:"expiresAt"`
	Commitment            *DeleteCommitmentV2 `json:"commitment,omitempty"`
	Dispatch              *DeleteDispatchV2   `json:"dispatch,omitempty"`
	Terminal              *TerminalEvidenceV2 `json:"terminal,omitempty"`
}

func (g EffectGrantV2) Validate() error {
	if g.SchemaVersion != EffectGrantSchemaV2 || uuid.Validate(g.ID) != nil || uuid.Validate(g.PlanID) != nil ||
		uuid.Validate(g.AttemptID) != nil || !hex64.MatchString(g.BindingSHA256) || g.TargetID == "" ||
		g.TargetFenceGeneration == 0 || g.AuthorityEpoch <= 0 || !hex64.MatchString(g.OwnerAuthorizationID) ||
		!hex40.MatchString(g.SourceCommit) || !hex64.MatchString(g.SavedPlanSHA256) || !hex64.MatchString(g.EffectSHA256) ||
		g.Backend.Bucket == "" || g.Backend.Key == "" || g.Backend.VersionID == "" || g.Backend.Lineage == "" ||
		g.Backend.Serial == 0 || !hex64.MatchString(g.Backend.RawSHA256) || g.ProviderAccountUUID == "" ||
		g.NodeName != WorkNode || g.DropletID != WorkDropletID ||
		g.RunnerSubject == "" || g.RunnerTokenID == "" || g.RunnerDeviceID == "" || !g.WriterFence.valid(g.AttemptID) ||
		!hex64.MatchString(g.EffectEvidence.SHA256) || g.EffectEvidence.ObservedAt.IsZero() ||
		!g.EffectEvidence.ValidUntil.After(g.EffectEvidence.ObservedAt) || !hex64.MatchString(g.DeleteRequestSHA256) ||
		g.IssuedAt.IsZero() || !g.ExpiresAt.After(g.IssuedAt) || g.ExpiresAt.Sub(g.IssuedAt) > 30*time.Second {
		return ErrEffectProofV2
	}
	switch g.Phase {
	case "issued":
		return nilIf(g.Commitment == nil && g.Dispatch == nil && g.Terminal == nil)
	case "delete_committed":
		return nilIf(g.validCommitment() && g.Dispatch == nil && g.Terminal == nil)
	case "delete_dispatched":
		return nilIf(g.validCommitment() && g.validDispatch() && g.Terminal == nil)
	case "terminal_verified":
		return nilIf(g.validCommitment() && g.validDispatch() && g.validTerminal())
	default:
		return ErrEffectProofV2
	}
}

func (g EffectGrantV2) validCommitment() bool {
	return g.Commitment != nil && g.Commitment.SchemaVersion == DeleteCommitmentSchemaV2 &&
		hex64.MatchString(g.Commitment.SHA256) && g.Commitment.DeleteRequestSHA256 == g.DeleteRequestSHA256 &&
		!g.Commitment.CommittedAt.Before(g.IssuedAt) && g.Commitment.CommittedAt.Before(g.ExpiresAt)
}

func (g EffectGrantV2) validDispatch() bool {
	return g.Dispatch != nil && g.Commitment != nil && g.Dispatch.SchemaVersion == DeleteDispatchSchemaV2 &&
		g.Dispatch.PlanID == g.PlanID && g.Dispatch.AttemptID == g.AttemptID && g.Dispatch.GrantID == g.ID &&
		g.Dispatch.CommitmentSHA256 == g.Commitment.SHA256 && g.Dispatch.TargetID == g.TargetID &&
		g.Dispatch.TargetFenceGeneration == g.TargetFenceGeneration && g.Dispatch.WriterFence == g.WriterFence &&
		g.Dispatch.AuthorityEpoch == g.AuthorityEpoch && g.Dispatch.RetryPolicy == DeleteRetryPolicyV2 &&
		!g.Dispatch.AdmittedAt.Before(g.Commitment.CommittedAt) && g.Dispatch.AdmittedAt.Before(g.ExpiresAt)
}

func (g EffectGrantV2) validTerminal() bool {
	return g.Terminal != nil && g.Dispatch != nil && g.Terminal.SchemaVersion == TerminalEvidenceSchemaV2 &&
		g.Terminal.PlanID == g.PlanID && g.Terminal.AttemptID == g.AttemptID && g.Terminal.GrantID == g.ID &&
		g.Terminal.BindingSHA256 == g.BindingSHA256 && g.Terminal.TargetID == g.TargetID &&
		g.Terminal.TargetFenceGeneration == g.TargetFenceGeneration && g.Terminal.WriterFence == g.WriterFence &&
		g.Terminal.AuthorityEpoch == g.AuthorityEpoch && !g.Terminal.ObservedAt.Before(g.Dispatch.AdmittedAt) &&
		!g.Terminal.RecordedAt.Before(g.Terminal.ObservedAt) && hex64.MatchString(g.Terminal.EvidenceSHA256) &&
		hex64.MatchString(g.Terminal.ProviderEvidenceSHA256) && hex64.MatchString(g.Terminal.BackendEvidenceSHA256) &&
		hex64.MatchString(g.Terminal.WorkloadEvidenceSHA256)
}

func nilIf(ok bool) error {
	if ok {
		return nil
	}
	return ErrEffectProofV2
}

func ExactDeleteRequestSHA256V2() string {
	empty := sha256.Sum256(nil)
	raw := []byte(`{"apiVersion":"v2","bodySHA256":"` + hex.EncodeToString(empty[:]) + `","method":"DELETE","origin":"https://api.digitalocean.com","path":"/v2/droplets/` + WorkDropletID + `"}` + "\n")
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
