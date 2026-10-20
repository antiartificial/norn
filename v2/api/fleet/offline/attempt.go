package offline

import "time"

const AttemptSchema = "norn.fleet-offline-attempt/v1"

// Attempt is the durable offline execution record. Admission creates the
// queued prechange_verified state. Effect v2 may move only that exact attempt
// to succeeded/complete after independently verified terminal evidence.
type Attempt struct {
	SchemaVersion     string                `json:"schemaVersion"`
	ID                string                `json:"id"`
	PlanID            string                `json:"planId"`
	BindingSHA256     string                `json:"bindingSHA256"`
	PrincipalSubject  string                `json:"principalSubject"`
	PrincipalTokenID  string                `json:"principalTokenId"`
	PrincipalDeviceID string                `json:"principalDeviceId"`
	TargetID          string                `json:"targetId"`
	FenceGeneration   uint64                `json:"fenceGeneration"`
	Status            string                `json:"status"`
	CurrentPhase      string                `json:"currentPhase"`
	CreatedAt         time.Time             `json:"createdAt"`
	FinishedAt        *time.Time            `json:"finishedAt,omitempty"`
	Completion        *CompletionEvidenceV2 `json:"completion,omitempty"`
}
