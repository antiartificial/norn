package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func TestRollbackProvenanceRejectsChangedSpecBeforeAcceptanceAndExecution(t *testing.T) {
	p, db, request := acceptancePipelineFixture(t)
	ctx := context.Background()
	spec := &model.InfraSpec{App: "rollback-provenance", Deploy: true, Processes: map[string]model.Process{"web": {Command: "serve"}}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	current := model.Deployment{ID: uuid.NewString(), App: spec.App, Environment: "staging", Status: model.StatusDeployed, StartedAt: time.Now()}
	previous := &model.Deployment{ID: uuid.NewString(), App: spec.App, SagaID: uuid.NewString(), Environment: "staging", Status: model.StatusDeployed, StartedAt: time.Now().Add(-time.Hour), ImageTag: "registry.example/rollback@sha256:" + strings.Repeat("a", 64), SpecDigest: digest}
	request.Key = "rollback-provenance"
	request.Semantics = map[string]interface{}{"currentDeploymentId": current.ID, "sourceDeploymentId": previous.ID}
	changed := *spec
	changed.Processes = map[string]model.Process{"web": {Command: "changed"}}
	if _, err := p.QueueRollback(ctx, &changed, current, previous, nil, request, nil); err == nil {
		t.Fatal("changed spec was accepted for a proven historical image")
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE app=$1`, spec.App).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected rollback persisted deployments=%d err=%v", rows, err)
	}
	accepted, err := p.QueueRollback(ctx, spec, current, previous, nil, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := p.QueueRollback(ctx, spec, current, previous, nil, request, nil)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID || replayed.Intent.DeploymentID != accepted.Intent.DeploymentID {
		t.Fatalf("proven rollback replay=%+v err=%v", replayed, err)
	}
	queued, err := db.GetDeployment(ctx, accepted.Intent.DeploymentID)
	if err != nil || queued.SpecDigest != digest || queued.ImageTag != previous.ImageTag {
		t.Fatalf("queued provenance=%+v err=%v", queued, err)
	}
	claim, err := store.NewOperationClaim(accepted.Operation.ID, "test-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	result := p.runRollback(ctx, &accepted.Operation, &changed, queued, saga.New(p.SagaStore, spec.App, "pipeline", "rollback"), queued.ImageTag, claim, 1, nil)
	if result.Status != model.OperationFailed || !strings.Contains(result.Message, "provenance changed") {
		t.Fatalf("changed spec reached rollback execution: %+v", result)
	}
	downgraded := *queued
	downgraded.SpecDigest = ""
	result = p.runRollback(ctx, &accepted.Operation, spec, &downgraded, saga.New(p.SagaStore, spec.App, "pipeline", "rollback"), downgraded.ImageTag, claim, 1, nil)
	if result.Status != model.OperationFailed || !strings.Contains(result.Message, "signed intent") {
		t.Fatalf("deployment provenance downgrade reached rollback execution: %+v", result)
	}
}
