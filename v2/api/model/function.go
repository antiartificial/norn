package model

import "time"

// FuncExecution represents a function invocation result.
type FuncExecution struct {
	ID         string     `json:"id"`
	App        string     `json:"app"`
	Process    string     `json:"process"`
	Status     string     `json:"status"`
	ExitCode   int        `json:"exitCode,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
}
