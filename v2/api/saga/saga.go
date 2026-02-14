package saga

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ID        string            `json:"id"`
	SagaID    string            `json:"sagaId"`
	Timestamp time.Time         `json:"timestamp"`
	Source    string            `json:"source"`
	App       string            `json:"app"`
	Category  string            `json:"category"` // deploy, restart, scale, system
	Action    string            `json:"action"`   // step.start, step.complete, step.failed, etc.
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Store interface {
	Append(ctx context.Context, evt *Event) error
	ListBySaga(ctx context.Context, sagaID string) ([]Event, error)
	ListByApp(ctx context.Context, app string, limit int) ([]Event, error)
	ListRecent(ctx context.Context, limit int) ([]Event, error)
}

// Saga is a helper for logging structured events in a deployment or operation.
type Saga struct {
	ID       string
	App      string
	Source   string
	Category string
	store    Store
}

func New(store Store, app, source, category string) *Saga {
	return &Saga{
		ID:       uuid.New().String(),
		App:      app,
		Source:   source,
		Category: category,
		store:    store,
	}
}

func (s *Saga) Log(ctx context.Context, action, message string, metadata map[string]string) error {
	evt := &Event{
		ID:        uuid.New().String(),
		SagaID:    s.ID,
		Timestamp: time.Now(),
		Source:    s.Source,
		App:       s.App,
		Category:  s.Category,
		Action:    action,
		Message:   message,
		Metadata:  metadata,
	}
	return s.store.Append(ctx, evt)
}

func (s *Saga) StepStart(ctx context.Context, step string) error {
	return s.Log(ctx, "step.start", step+" started", map[string]string{"step": step})
}

func (s *Saga) StepComplete(ctx context.Context, step string, durationMs int64) error {
	return s.Log(ctx, "step.complete", step+" completed", map[string]string{
		"step":       step,
		"durationMs": formatInt64(durationMs),
	})
}

func (s *Saga) StepFailed(ctx context.Context, step string, err error) error {
	return s.Log(ctx, "step.failed", step+" failed: "+err.Error(), map[string]string{
		"step":  step,
		"error": err.Error(),
	})
}

func formatInt64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
