package model

import "time"

type OperationStatus string

const (
	OperationQueued    OperationStatus = "queued"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
	OperationCanceled  OperationStatus = "canceled"
)

type Operation struct {
	ID         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	App        string                 `json:"app,omitempty"`
	SagaID     string                 `json:"sagaId,omitempty"`
	Ref        string                 `json:"ref,omitempty"`
	Status     OperationStatus        `json:"status"`
	Risk       string                 `json:"risk,omitempty"`
	Source     string                 `json:"source,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	StartedAt  time.Time              `json:"startedAt"`
	UpdatedAt  time.Time              `json:"updatedAt"`
	FinishedAt *time.Time             `json:"finishedAt,omitempty"`
}

func (o Operation) Active() bool {
	return o.Status == OperationQueued || o.Status == OperationRunning
}
