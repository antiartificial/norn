package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type unavailableCheckpointStore struct{}

func (unavailableCheckpointStore) RecordOperationCheckpoint(context.Context, store.OperationClaim, string, json.RawMessage) (store.OperationCheckpoint, error) {
	panic("build/test preflight must be rejected before checkpoint access")
}

func (unavailableCheckpointStore) LoadOperationCheckpoint(context.Context, string, string) (*store.OperationCheckpoint, error) {
	panic("build/test preflight must be rejected before checkpoint access")
}

func TestBackendNeutralPreflightRejectsBuildBeforeAcceptance(t *testing.T) {
	p := &Pipeline{CheckpointStore: unavailableCheckpointStore{}}
	_, err := p.Preflight(context.Background(), &model.InfraSpec{
		App:       "source-only",
		Processes: map[string]model.Process{"worker": {Command: "sleep 1"}},
		Build:     &model.BuildSpec{Test: "false"},
	}, "HEAD", EnqueueRequest{})
	if err == nil || !strings.Contains(err.Error(), "source validation only") {
		t.Fatalf("build/test preflight acceptance error=%v", err)
	}
}

func TestPostgresPreflightRequiresSagaStore(t *testing.T) {
	p := &Pipeline{DB: &store.DB{}}
	spec := &model.InfraSpec{App: "history-required", Processes: map[string]model.Process{"worker": {Command: "sleep 1"}}}
	if _, err := p.Preflight(context.Background(), spec, "HEAD", EnqueueRequest{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("postgres preflight without saga store error=%v", err)
	}
	if _, err := p.QueueReleasePreflight(context.Background(), spec, "head", "", "test", nil, EnqueueRequest{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("postgres release preflight without saga store error=%v", err)
	}
}
