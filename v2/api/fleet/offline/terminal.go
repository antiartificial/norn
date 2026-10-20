package offline

import (
	"errors"
	"time"
)

const TerminalSchema = "norn.fleet-offline-terminal/v1"

var ErrTerminalProof = errors.New("independent offline terminal proof is incomplete or mismatched")

// TerminalObservation must be assembled by authority-owned read-only clients
// after an effect, never from the executor's completion receipt. A consumed
// grant alone says nothing about whether the provider effect succeeded.
type TerminalObservation struct {
	PlanID, AttemptID, GrantID, BindingSHA256, TargetID            string
	FenceGeneration                                                uint64
	AuthorityEpoch                                                 int64
	ObservedAt                                                     time.Time
	EvidenceSHA256                                                 string
	ProviderEvidenceSHA256                                         string
	BackendEvidenceSHA256                                          string
	WorkloadEvidenceSHA256                                         string
	ProviderAccountUUID, NodeName, DropletID                       string
	Backend                                                        Backend
	ProviderNodeAbsent, BackendLatestExact, BackendNodeAbsent      bool
	AllNamespacesInspected, WorkloadNodeAbsent, SurvivingPlacement bool
	SurvivingHeadroom, ServicePassing                              bool
}

// TerminalEvidence is a durable receipt, not a target-fence release. Recovery
// must restore it together with the consumed grant, attempt, binding and fence.
type TerminalEvidence struct {
	SchemaVersion          string    `json:"schemaVersion"`
	PlanID                 string    `json:"planId"`
	AttemptID              string    `json:"attemptId"`
	GrantID                string    `json:"grantId"`
	BindingSHA256          string    `json:"bindingSHA256"`
	TargetID               string    `json:"targetId"`
	FenceGeneration        uint64    `json:"fenceGeneration"`
	AuthorityEpoch         int64     `json:"authorityEpoch"`
	ObservedAt             time.Time `json:"observedAt"`
	RecordedAt             time.Time `json:"recordedAt"`
	EvidenceSHA256         string    `json:"evidenceSHA256"`
	ProviderEvidenceSHA256 string    `json:"providerEvidenceSHA256"`
	BackendEvidenceSHA256  string    `json:"backendEvidenceSHA256"`
	WorkloadEvidenceSHA256 string    `json:"workloadEvidenceSHA256"`
	ChallengeID            string    `json:"challengeId,omitempty"`
	AttestationSHA256      string    `json:"attestationSHA256,omitempty"`
	ObserverSubject        string    `json:"observerSubject,omitempty"`
	ObserverKeyID          string    `json:"observerKeyId,omitempty"`
	RawJournalSHA256       string    `json:"rawJournalSHA256,omitempty"`
	Backend                Backend   `json:"backend"`
}

func ValidateTerminalObservation(o TerminalObservation, g EffectGrant, now time.Time) error {
	if g.SchemaVersion != EffectGrantSchema || g.Phase != "consumed" || g.ConsumedAt == nil ||
		o.PlanID != g.PlanID || o.AttemptID != g.AttemptID || o.GrantID != g.ID ||
		o.BindingSHA256 != g.BindingSHA256 || o.TargetID != g.TargetID ||
		o.FenceGeneration != g.FenceGeneration || o.AuthorityEpoch != g.AuthorityEpoch ||
		o.ProviderAccountUUID != g.ProviderAccountUUID || o.NodeName != g.NodeName || o.DropletID != g.DropletID ||
		o.Backend.Bucket != g.Backend.Bucket || o.Backend.Key != g.Backend.Key ||
		o.Backend.Lineage != g.Backend.Lineage || g.Backend.Serial == ^uint64(0) || o.Backend.Serial != g.Backend.Serial+1 ||
		o.Backend.VersionID == g.Backend.VersionID || o.Backend.VersionID == "" || o.Backend.VersionID == "null" ||
		!hex64.MatchString(o.Backend.RawSHA256) || !hex64.MatchString(o.EvidenceSHA256) ||
		!hex64.MatchString(o.ProviderEvidenceSHA256) || !hex64.MatchString(o.BackendEvidenceSHA256) ||
		!hex64.MatchString(o.WorkloadEvidenceSHA256) || !o.ObservedAt.After(*g.ConsumedAt) ||
		o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > 30*time.Second ||
		!o.ProviderNodeAbsent || !o.BackendLatestExact || !o.BackendNodeAbsent ||
		!o.AllNamespacesInspected || !o.WorkloadNodeAbsent || !o.SurvivingPlacement ||
		!o.SurvivingHeadroom || !o.ServicePassing {
		return ErrTerminalProof
	}
	return nil
}
