package offline

import (
	"errors"
	"time"
)

const EffectGrantSchema = "norn.fleet-offline-effect-grant/v1"

var ErrEffectProofMissing = errors.New("independent effect-time proof is unavailable")
var ErrEffectProofMismatch = errors.New("independent effect-time proof differs from held attempt")

// EffectObservation is an independent, effect-time reobservation. The runner's
// prechange receipt cannot populate this object. A production observer must
// establish origin, provider inventory, latest backend bytes, every Nomad
// namespace, workload placement, headroom and service health before returning.
type EffectObservation struct {
	PlanID, AttemptID, BindingSHA256, TargetID             string
	SourceCommit, SavedPlanSHA256, EffectSHA256            string
	Backend                                                Backend
	ProviderAccountUUID, NodeName, DropletID               string
	FenceGeneration                                        uint64
	AuthorityEpoch                                         int64
	ObservedAt                                             time.Time
	EvidenceSHA256                                         string
	TrustedOrigin, ProviderExact, LatestBackendExact       bool
	AllNamespacesInspected, App03Empty, SurvivingPlacement bool
	SurvivingHeadroom, ServicePassing                      bool
}

// EffectGrant is a short-lived, one-use authorization record, never a provider
// credential. Its consumed phase only fences a future runner effect; terminal
// proof and target-fence release are separate operations.
type EffectGrant struct {
	SchemaVersion               string     `json:"schemaVersion"`
	ID                          string     `json:"id"`
	PlanID                      string     `json:"planId"`
	AttemptID                   string     `json:"attemptId"`
	BindingSHA256               string     `json:"bindingSHA256"`
	TargetID                    string     `json:"targetId"`
	FenceGeneration             uint64     `json:"fenceGeneration"`
	AuthorityEpoch              int64      `json:"authorityEpoch"`
	SourceCommit                string     `json:"sourceCommit"`
	SavedPlanSHA256             string     `json:"savedPlanSHA256"`
	EffectSHA256                string     `json:"effectSHA256"`
	Backend                     Backend    `json:"backend"`
	ProviderAccountUUID         string     `json:"providerAccountUUID"`
	NodeName                    string     `json:"nodeName"`
	DropletID                   string     `json:"dropletId"`
	PrechangeSHA256             string     `json:"prechangeSHA256"`
	IndependentlyObservedSHA256 string     `json:"independentlyObservedSHA256"`
	Phase                       string     `json:"phase"`
	IssuedAt                    time.Time  `json:"issuedAt"`
	ExpiresAt                   time.Time  `json:"expiresAt"`
	ConsumedAt                  *time.Time `json:"consumedAt,omitempty"`
}

func ValidateEffectObservation(o EffectObservation, b Binding, a Attempt, epoch int64, now time.Time) error {
	if b.SchemaVersion != BindingSchemaV2 || b.Validate() != nil ||
		o.PlanID != b.PlanID || o.AttemptID != a.ID || o.BindingSHA256 != a.BindingSHA256 ||
		o.TargetID != b.TargetID || o.FenceGeneration != b.FenceGeneration || o.AuthorityEpoch != epoch ||
		o.SourceCommit != b.SourceCommit || o.SavedPlanSHA256 != b.SavedPlanSHA256 ||
		o.EffectSHA256 != b.EffectSHA256 || o.Backend != b.Backend ||
		o.ProviderAccountUUID != b.ProviderAccountUUID || o.NodeName != b.NodeName || o.DropletID != b.DropletID ||
		!hex64.MatchString(o.EvidenceSHA256) || o.ObservedAt.IsZero() || now.Before(o.ObservedAt) ||
		now.Sub(o.ObservedAt) > 30*time.Second || !now.Before(b.ExpiresAt) ||
		!o.TrustedOrigin || !o.ProviderExact || !o.LatestBackendExact || !o.AllNamespacesInspected ||
		!o.App03Empty || !o.SurvivingPlacement || !o.SurvivingHeadroom || !o.ServicePassing {
		return ErrEffectProofMismatch
	}
	return nil
}
