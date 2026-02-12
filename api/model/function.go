package model

import "time"

type FuncExecStatus string

const (
	FuncRunning   FuncExecStatus = "running"
	FuncSucceeded FuncExecStatus = "succeeded"
	FuncFailed    FuncExecStatus = "failed"
	FuncTimedOut  FuncExecStatus = "timed_out"
)

type FuncExecution struct {
	ID         int64          `json:"id"`
	App        string         `json:"app"`
	ImageTag   string         `json:"imageTag"`
	Status     FuncExecStatus `json:"status"`
	ExitCode   int            `json:"exitCode"`
	Output     string         `json:"output"`
	DurationMs int64          `json:"durationMs"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
}
