package model

import "time"

type CronExecStatus string

const (
	CronRunning   CronExecStatus = "running"
	CronSucceeded CronExecStatus = "succeeded"
	CronFailed    CronExecStatus = "failed"
	CronTimedOut  CronExecStatus = "timed_out"
)

type CronExecution struct {
	ID         int64          `json:"id"`
	App        string         `json:"app"`
	ImageTag   string         `json:"imageTag"`
	Status     CronExecStatus `json:"status"`
	ExitCode   int            `json:"exitCode"`
	Output     string         `json:"output"`
	DurationMs int64          `json:"durationMs"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
}

type CronState struct {
	App       string     `json:"app"`
	Schedule  string     `json:"schedule"`
	Paused    bool       `json:"paused"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
}
