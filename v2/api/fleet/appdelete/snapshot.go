package appdelete

// Snapshot is an observation of Norn-owned authority at one etcd revision.
// It does not attest PostgreSQL, provider state, or workload placement.
const SnapshotSchema = "norn.fleet-app-delete-snapshot/v1"

type Snapshot struct {
	SchemaVersion             string          `json:"schemaVersion"`
	PlanID                    string          `json:"planId"`
	AttemptID                 string          `json:"attemptId"`
	EtcdRevision              int64           `json:"etcdRevision"`
	SourceCommit              string          `json:"sourceCommit"`
	PlanSHA256                string          `json:"planSha256"`
	AttemptStatus             string          `json:"attemptStatus"`
	AttemptPhase              string          `json:"attemptPhase"`
	AttemptRevision           int64           `json:"attemptRevision"`
	HeartbeatExpiresAt        string          `json:"heartbeatExpiresAt"`
	ExecutorOperationClass    string          `json:"executorOperationClass"`
	ExecutorRecovery          bool            `json:"executorRecovery"`
	CreatedNodeCount          int             `json:"createdNodeCount"`
	TargetID                  string          `json:"targetId"`
	TargetFenceHeld           bool            `json:"targetFenceHeld"`
	TargetFenceHolderPlanID   string          `json:"targetFenceHolderPlanId"`
	TargetFenceGeneration     int64           `json:"targetFenceGeneration"`
	TargetFenceAuthorityEpoch int64           `json:"targetFenceAuthorityEpoch"`
	CurrentAuthorityEpoch     int64           `json:"currentAuthorityEpoch"`
	App04Admission            *App04Admission `json:"app04Admission,omitempty"`
	ConsumedPermit            *Request        `json:"consumedPermit,omitempty"`
}
