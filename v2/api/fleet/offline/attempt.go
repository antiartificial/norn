package offline

import "time"

const AttemptSchema = "norn.fleet-offline-attempt/v1"

// Checkpoint is a durable, non-effect runner observation. No checkpoint phase
// authorizes provider credentials, a state apply, or fence release.
const CheckpointSchema = "norn.fleet-offline-checkpoint/v1"

type Checkpoint struct {
	SchemaVersion   string            `json:"schemaVersion"`
	PlanID          string            `json:"planId"`
	AttemptID       string            `json:"attemptId"`
	Sequence        uint64            `json:"sequence"`
	Phase           string            `json:"phase"`
	EvidenceSHA256  string            `json:"evidenceSHA256"`
	Prechange       *PrechangeSummary `json:"prechange,omitempty"`
	AuthorityEpoch  int64             `json:"authorityEpoch"`
	FenceGeneration uint64            `json:"fenceGeneration"`
	RecordedAt      time.Time         `json:"recordedAt"`
}

// Attempt is the immutable, non-effect admission record. Its runner can
// record a separately authenticated observation checkpoint, but neither
// record can request a credential or authorize an effect.
type Attempt struct {
	SchemaVersion     string    `json:"schemaVersion"`
	ID                string    `json:"id"`
	PlanID            string    `json:"planId"`
	BindingSHA256     string    `json:"bindingSHA256"`
	PrincipalSubject  string    `json:"principalSubject"`
	PrincipalTokenID  string    `json:"principalTokenId,omitempty"`
	PrincipalDeviceID string    `json:"principalDeviceId,omitempty"`
	TargetID          string    `json:"targetId"`
	FenceGeneration   uint64    `json:"fenceGeneration"`
	Status            string    `json:"status"`
	CurrentPhase      string    `json:"currentPhase"`
	CreatedAt         time.Time `json:"createdAt"`
}
