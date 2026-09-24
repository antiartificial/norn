package handler

import (
	"context"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type canaryReplayStore struct{ accepted store.AcceptedOperation }

func (s canaryReplayStore) Authority(context.Context) (string, error) { return "authority", nil }
func (s canaryReplayStore) Accept(context.Context, store.OperationAcceptance) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, nil
}
func (s canaryReplayStore) Resolve(context.Context, store.OperationRequestIdentity, store.RequestFingerprint) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, nil
}
func (s canaryReplayStore) ResolveIdentity(context.Context, store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	return s.accepted, nil
}

func TestCanaryPromotionReplayDoesNotRequireLiveCanary(t *testing.T) {
	accepted := store.AcceptedOperation{Replayed: true, Operation: model.Operation{ID: "operation-1", Kind: "app.canary-promote", App: "widgets", Payload: map[string]interface{}{"region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-promoted"}}}
	p := &pipeline.Pipeline{}
	p.SetOperationStore(canaryReplayStore{accepted: accepted})
	h := &Handler{pipeline: p}
	result, replayed, err := h.resolveCanaryPromotionReplay(context.Background(), pipeline.EnqueueRequest{Authority: "authority", Actor: store.OperationActor{Issuer: "issuer", Subject: "subject"}, Key: "same-key"}, "widgets", "us-central", "global")
	if err != nil || !replayed || result.Operation.ID != "operation-1" {
		t.Fatalf("replay = %#v, %t, %v", result, replayed, err)
	}
}
