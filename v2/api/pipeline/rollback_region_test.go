package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

func TestCompleteRollbackRegionsRequiresWholeApp(t *testing.T) {
	spec := &model.InfraSpec{Regions: map[string]model.RegionTarget{"east": {}, "west": {}}}
	for _, requested := range [][]string{{"east"}, {"unknown"}, {"east", "west", "unknown"}} {
		if _, err := completeRollbackRegions(spec, requested); err == nil {
			t.Fatalf("accepted incomplete or unknown region selection %v", requested)
		}
	}
	for _, requested := range [][]string{nil, {"west", "east"}} {
		regions, err := completeRollbackRegions(spec, requested)
		if err != nil || len(regions) != 2 || regions[0].Name != "east" || regions[1].Name != "west" {
			t.Fatalf("whole-app rollback regions=%+v err=%v", regions, err)
		}
	}
}

func TestPartialRollbackDoesNotPersistWholeAppIntent(t *testing.T) {
	p, db, request := acceptancePipelineFixture(t)
	ctx := context.Background()
	spec := &model.InfraSpec{App: "regional-rollback", Deploy: true, Regions: map[string]model.RegionTarget{"east": {}, "west": {}}}
	current := model.Deployment{ID: uuid.NewString(), App: spec.App, Environment: "staging", Status: model.StatusDeployed, StartedAt: time.Now()}
	previous := &model.Deployment{ID: uuid.NewString(), App: spec.App, Environment: "staging", Status: model.StatusDeployed, StartedAt: time.Now().Add(-time.Hour)}
	request.Semantics = map[string]interface{}{"currentDeploymentId": current.ID, "sourceDeploymentId": previous.ID}
	if _, err := p.QueueRollback(ctx, spec, current, previous, []string{"east"}, request, nil); err == nil {
		t.Fatal("partial rollback accepted as a whole-app deployment")
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE app=$1`, spec.App).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected rollback persisted deployments=%d err=%v", rows, err)
	}
}
