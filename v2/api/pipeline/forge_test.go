package pipeline

import (
	"context"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func TestForgeExternalIngressDoesNotRequireLocalCloudflared(t *testing.T) {
	p := &Pipeline{ExternalIngress: true}
	st := &state{spec: &model.InfraSpec{
		App:       "orders",
		Endpoints: []model.Endpoint{{URL: "https://orders.example.test"}},
	}}
	if err := p.forge(context.Background(), st, saga.New(discardSagaStore{}, "orders", "pipeline", "deploy")); err != nil {
		t.Fatalf("external ingress forge: %v", err)
	}
}

type discardSagaStore struct{}

func (discardSagaStore) Append(context.Context, *saga.Event) error { return nil }
func (discardSagaStore) ListBySaga(context.Context, string) ([]saga.Event, error) {
	return nil, nil
}
func (discardSagaStore) ListByApp(context.Context, string, int) ([]saga.Event, error) {
	return nil, nil
}
func (discardSagaStore) ListRecent(context.Context, int) ([]saga.Event, error) { return nil, nil }
