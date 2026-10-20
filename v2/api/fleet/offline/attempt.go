package offline

import "time"

const AttemptSchema = "norn.fleet-offline-attempt/v1"

// Attempt is the immutable, non-effect admission record. A queued attempt
// cannot request a credential or advance phases until a later, separately
// authenticated offline runner protocol has been implemented.
type Attempt struct {
	SchemaVersion     string    `json:"schemaVersion"`
	ID                string    `json:"id"`
	PlanID            string    `json:"planId"`
	BindingSHA256     string    `json:"bindingSHA256"`
	PrincipalSubject  string    `json:"principalSubject"`
	PrincipalTokenID  string    `json:"principalTokenId"`
	PrincipalDeviceID string    `json:"principalDeviceId"`
	TargetID          string    `json:"targetId"`
	FenceGeneration   uint64    `json:"fenceGeneration"`
	Status            string    `json:"status"`
	CurrentPhase      string    `json:"currentPhase"`
	CreatedAt         time.Time `json:"createdAt"`
}
