package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func TestRunClaimedStepStopsAfterCancellationEvenWhenStepReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	secondRan := false
	steps := []func() error{
		func() error {
			cancel()
			return nil
		},
		func() error {
			secondRan = true
			return nil
		},
	}
	for _, step := range steps {
		if err := runClaimedStep(ctx, step); err != nil {
			break
		}
	}
	if secondRan {
		t.Fatal("pipeline continued to a later step after ownership cancellation")
	}
}

func TestDeploymentStepStartFailureIsPropagatedBeforeEffect(t *testing.T) {
	want := errors.New("step store unavailable")
	startCalls := 0
	p := &Pipeline{AppsDir: t.TempDir(), StartDeploymentStep: func(context.Context, model.DeploymentStep) error {
		startCalls++
		return want
	}}
	deploy := &model.Deployment{ID: "deployment", App: "atlas", CommitSHA: "source", StartedAt: time.Now()}
	spec := &model.InfraSpec{App: "atlas"}
	claim, err := store.NewOperationClaim("operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	result := p.run(context.Background(), spec, deploy, saga.New(discardSagaStore{}, "atlas", "pipeline", "deploy"), claim, 1)
	if startCalls != 1 {
		t.Fatalf("step start calls=%d, want 1", startCalls)
	}
	if result == nil || result.Status != model.OperationFailed || !strings.Contains(result.Message, want.Error()) || !strings.Contains(result.Message, "before execution") {
		t.Fatalf("pipeline result=%+v", result)
	}
	if deploy.Status == model.StatusDeployed {
		t.Fatal("deployment was marked deployed after step-start persistence failure")
	}
}
