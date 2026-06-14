package model

import "time"

type DeploymentStepStatus string

const (
	DeploymentStepRunning  DeploymentStepStatus = "running"
	DeploymentStepComplete DeploymentStepStatus = "complete"
	DeploymentStepFailed   DeploymentStepStatus = "failed"
)

type DeploymentStepKind string

const (
	DeploymentStepReadOnly DeploymentStepKind = "readonly"
	DeploymentStepMutable  DeploymentStepKind = "mutable"
)

type DeploymentStep struct {
	DeploymentID string                 `json:"deploymentId"`
	App          string                 `json:"app"`
	SagaID       string                 `json:"sagaId"`
	Step         string                 `json:"step"`
	Status       DeploymentStepStatus   `json:"status"`
	Kind         DeploymentStepKind     `json:"kind,omitempty"`
	Attempt      int                    `json:"attempt,omitempty"`
	StartedAt    time.Time              `json:"startedAt"`
	FinishedAt   *time.Time             `json:"finishedAt,omitempty"`
	DurationMs   int64                  `json:"durationMs,omitempty"`
	Message      string                 `json:"message,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

func StepKind(stepName string) DeploymentStepKind {
	switch stepName {
	case "clone", "build", "test":
		return DeploymentStepReadOnly
	default:
		return DeploymentStepMutable
	}
}
