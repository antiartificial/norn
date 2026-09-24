package pipeline

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type rejectSnapshotAcceptanceStore struct{ store.OperationStore }

func (rejectSnapshotAcceptanceStore) Accept(context.Context, store.OperationAcceptance) (store.AcceptedOperation, error) {
	panic("snapshot acceptance reached the durable store")
}

func TestSnapshotAcceptanceRejectsUnavailableExecutionBeforePersistence(t *testing.T) {
	for _, effects := range []*SnapshotEffects{nil, {}} {
		p := &Pipeline{OperationStore: rejectSnapshotAcceptanceStore{}, SnapshotEffects: effects}
		_, err := p.QueueOperation(context.Background(), model.Operation{Kind: "app.snapshot", App: "demo"}, EnqueueRequest{})
		var unavailable *SnapshotExecutionUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("effects=%+v: got %v, want unavailable execution", effects, err)
		}
	}
}
