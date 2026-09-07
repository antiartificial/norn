package model

import "time"

const FleetRunnerAttemptSchemaVersion = "norn.fleet-runner-attempt/v1"

type FleetRunnerAttemptStatus string

const (
	FleetRunnerAttemptQueued    FleetRunnerAttemptStatus = "queued"
	FleetRunnerAttemptRunning   FleetRunnerAttemptStatus = "running"
	FleetRunnerAttemptSucceeded FleetRunnerAttemptStatus = "succeeded"
	FleetRunnerAttemptFailed    FleetRunnerAttemptStatus = "failed"
	FleetRunnerAttemptCanceled  FleetRunnerAttemptStatus = "canceled"
	FleetRunnerAttemptAbandoned FleetRunnerAttemptStatus = "abandoned"
)

func (s FleetRunnerAttemptStatus) Terminal() bool {
	return s == FleetRunnerAttemptSucceeded || s == FleetRunnerAttemptFailed || s == FleetRunnerAttemptCanceled || s == FleetRunnerAttemptAbandoned
}

// FleetRunnerAttempt is the durable liveness record for one protected
// infrastructure-runner execution. Append-only reconciliation operations remain
// the proof that a phase completed; this record only says which phase a live
// runner is attempting and when it last proved liveness.
type FleetRunnerAttempt struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	PlanID        string `json:"planId"`
	Attempt       int    `json:"attempt"`
	// RootAttemptID is assigned exclusively by durable storage. It ties every
	// retry to the first attempt for a plan and must never be caller-controlled.
	RootAttemptID           string                   `json:"rootAttemptId"`
	RunnerAttemptID         string                   `json:"runnerAttemptId,omitempty"`
	SourceDispatchRunID     int64                    `json:"sourceDispatchRunId"`
	Recovery                bool                     `json:"recovery,omitempty"`
	Status                  FleetRunnerAttemptStatus `json:"status"`
	CurrentPhase            string                   `json:"currentPhase"`
	CommitSHA               string                   `json:"commitSha"`
	PlanSHA256              string                   `json:"planSha256"`
	WorkflowURL             string                   `json:"workflowUrl,omitempty"`
	PrincipalSubject        string                   `json:"principalSubject,omitempty"`
	RetryOf                 string                   `json:"retryOf,omitempty"`
	HeartbeatSequence       int64                    `json:"heartbeatSequence"`
	HeartbeatTimeoutSeconds int                      `json:"heartbeatTimeoutSeconds"`
	Revision                int64                    `json:"revision"`
	StartedAt               time.Time                `json:"startedAt"`
	PhaseStartedAt          time.Time                `json:"phaseStartedAt"`
	HeartbeatAt             time.Time                `json:"heartbeatAt"`
	HeartbeatExpiresAt      time.Time                `json:"heartbeatExpiresAt"`
	UpdatedAt               time.Time                `json:"updatedAt"`
	FinishedAt              *time.Time               `json:"finishedAt,omitempty"`
	LastError               string                   `json:"lastError,omitempty"`
	Metadata                map[string]interface{}   `json:"metadata,omitempty"`
	Timing                  *FleetRunnerTiming       `json:"timing,omitempty"`
}

func (a *FleetRunnerAttempt) SetDerivedFields() {
	if a == nil {
		return
	}
	a.SchemaVersion = FleetRunnerAttemptSchemaVersion
	a.HeartbeatExpiresAt = a.HeartbeatAt.Add(time.Duration(a.HeartbeatTimeoutSeconds) * time.Second)
}
