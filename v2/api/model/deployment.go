package model

import "time"

type DeployStatus string

const (
	StatusQueued    DeployStatus = "queued"
	StatusBuilding  DeployStatus = "building"
	StatusTesting   DeployStatus = "testing"
	StatusMigrating DeployStatus = "migrating"
	StatusSubmitting DeployStatus = "submitting"
	StatusHealthy   DeployStatus = "healthy"
	StatusDeployed  DeployStatus = "deployed"
	StatusFailed    DeployStatus = "failed"
)

type Deployment struct {
	ID         string       `json:"id"`
	App        string       `json:"app"`
	CommitSHA  string       `json:"commitSha"`
	ImageTag   string       `json:"imageTag"`
	SagaID     string       `json:"sagaId"`
	Status     DeployStatus `json:"status"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty"`
}
