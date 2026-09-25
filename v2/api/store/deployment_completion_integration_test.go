package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestCompleteDeploymentResultIsClaimFencedAndAtomicAcrossRegions(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	db := &DB{Pool: pool}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claim, err := NewOperationClaim("deployment-completion-op", "worker-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,app,saga_id,status,payload,locked_by,lock_generation,locked_until)
		VALUES($1,'app.deploy','completion-app','completion-saga','running','{"deploymentId":"deployment-completion"}'::jsonb,$2,$3,now()+interval '1 minute')`, claim.OperationID(), claim.OwnerID(), claim.Generation()); err != nil {
		t.Fatal(err)
	}
	d := &model.Deployment{ID: "deployment-completion", App: "completion-app", SagaID: "completion-saga", Status: model.StatusQueued,
		ImageTag:   "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpecDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", StartedAt: time.Now()}
	if err := db.InsertDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	regions := []model.ResolvedRegion{{Name: "east", NomadRegion: "east", TrafficWeight: 60}, {Name: "west", NomadRegion: "west", TrafficWeight: 40}}
	if err := db.InsertDeploymentRegions(ctx, d.ID, regions); err != nil {
		t.Fatal(err)
	}
	d.Status = model.StatusDeployed
	if _, err := pool.Exec(ctx, `UPDATE operations SET payload='{"deploymentId":"other-deployment"}'::jsonb WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteDeploymentResult(ctx, claim, d, []DeploymentCompletionRegion{{Region: "east", ActiveWeight: 60}, {Region: "west", ActiveWeight: 40}}, "done", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("wrong accepted deployment completion err=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE operations SET payload='{"deploymentId":"deployment-completion"}'::jsonb WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteDeploymentResult(ctx, claim, d, []DeploymentCompletionRegion{{Region: "east", ActiveWeight: 60}, {Region: "missing", ActiveWeight: 40}}, "done", nil); err == nil {
		t.Fatal("partial region completion committed")
	}
	var status model.DeployStatus
	var deployedRegions int
	if err := pool.QueryRow(ctx, `SELECT status FROM deployments WHERE id=$1`, d.ID).Scan(&status); err != nil || status != model.StatusQueued {
		t.Fatalf("deployment after rolled-back completion status=%s err=%v", status, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM deployment_regions WHERE deployment_id=$1 AND status='deployed'`, d.ID).Scan(&deployedRegions); err != nil || deployedRegions != 0 {
		t.Fatalf("regions after rolled-back completion count=%d err=%v", deployedRegions, err)
	}
	var operationStatus model.OperationStatus
	var archiveIntents int
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, claim.OperationID()).Scan(&operationStatus); err != nil || operationStatus != model.OperationRunning {
		t.Fatalf("operation after rolled-back completion status=%s err=%v", operationStatus, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&archiveIntents); err != nil || archiveIntents != 0 {
		t.Fatalf("archive intent after rolled-back completion count=%d err=%v", archiveIntents, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	want := []DeploymentCompletionRegion{{Region: "east", ActiveWeight: 60}, {Region: "west", ActiveWeight: 40}}
	if err := db.CompleteDeploymentResult(ctx, claim, d, want, "done", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("expired claim completion err=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE operations SET locked_until=now()+interval '1 minute' WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteDeploymentResult(ctx, claim, d, want, "done", map[string]interface{}{"imageTag": d.ImageTag}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetDeployment(ctx, d.ID)
	if err != nil || stored.Status != model.StatusDeployed || stored.SpecDigest != d.SpecDigest || len(stored.Regions) != 2 || stored.Regions[0].Status != model.StatusDeployed || stored.Regions[1].Status != model.StatusDeployed {
		t.Fatalf("completed deployment=%+v err=%v", stored, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id=$1`, claim.OperationID()).Scan(&operationStatus); err != nil || operationStatus != model.OperationSucceeded {
		t.Fatalf("terminal operation status=%s err=%v", operationStatus, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1 AND subject_id='completion-saga'`, claim.OperationID()).Scan(&archiveIntents); err != nil || archiveIntents != 1 {
		t.Fatalf("terminal archive intent count=%d err=%v", archiveIntents, err)
	}
}
