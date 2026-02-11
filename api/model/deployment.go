package model

import "time"

type DeployStatus string

const (
	StatusQueued      DeployStatus = "queued"
	StatusBuilding    DeployStatus = "building"
	StatusTesting     DeployStatus = "testing"
	StatusSnapshot    DeployStatus = "snapshotting"
	StatusMigrating   DeployStatus = "migrating"
	StatusDeploying   DeployStatus = "deploying"
	StatusDeployed    DeployStatus = "deployed"
	StatusFailed      DeployStatus = "failed"
	StatusRolledBack  DeployStatus = "rolled_back"
)

type Deployment struct {
	ID         string       `json:"id" db:"id"`
	App        string       `json:"app" db:"app"`
	CommitSHA  string       `json:"commitSha" db:"commit_sha"`
	ImageTag   string       `json:"imageTag" db:"image_tag"`
	Status     DeployStatus `json:"status" db:"status"`
	Steps      []StepLog    `json:"steps" db:"steps"`
	Error      string       `json:"error,omitempty" db:"error"`
	StartedAt  time.Time    `json:"startedAt" db:"started_at"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty" db:"finished_at"`
	WorkDir    string       `json:"-" db:"-"` // temp dir for build, not persisted
}

type StepLog struct {
	Step       string       `json:"step"`
	Status     DeployStatus `json:"status"`
	DurationMs int64        `json:"durationMs,omitempty"`
	Output     string       `json:"output,omitempty"`
}
