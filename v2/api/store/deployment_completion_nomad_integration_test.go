package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// TestNomadSuccessWithCompletionFailureRequiresManualRecovery is opt-in and
// owns only its uniquely named job on a disposable loopback Nomad agent.
func TestNomadSuccessWithCompletionFailureRequiresManualRecovery(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_FUNCTION_IMAGE")
	if address == "" || image == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR, NORN_TEST_FUNCTION_IMAGE and NORN_TEST_NOMAD_DOCKER=1")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() == "" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("qualification requires loopback Nomad and a content-addressed image")
	}
	pool := schemaMigrationTestPools(t, 1)[0]
	db := &DB{Pool: pool}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-completion-recovery-%d", time.Now().UnixNano())
	spec := &model.InfraSpec{App: app, Deploy: true, Processes: map[string]model.Process{"web": {Command: "sleep 120"}}}
	region := spec.ResolvedRegions()[0]
	nomadJob := nomad.TranslateForRegion(spec, image, nil, region)
	nomadJob.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Now().Add(-time.Second)
	d := &model.Deployment{ID: uuid.NewString(), App: app, SagaID: uuid.NewString(), Status: model.StatusQueued, ImageTag: image, StartedAt: now}
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: app, SagaID: d.SagaID, Status: model.OperationQueued,
		StartedAt: now, NextAttemptAt: now, MaxAttempts: 2, Payload: map[string]interface{}{"deploymentId": d.ID}, Metadata: map[string]interface{}{}}
	if err := db.InsertDeploymentOperation(ctx, d, []model.ResolvedRegion{region}, op); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "nomad-completion-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := db.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: d.ID, App: d.App, SagaID: d.SagaID,
		Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepRunning, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubmitJob(nomadJob); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.StopJob(app, true) })
	for {
		err = client.VerifyRunningAppImage(ctx, spec, image)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("Nomad image never became provable: %v", err)
		}
		time.Sleep(time.Second)
	}
	if err := db.FinishDeploymentStep(ctx, d.ID, "submit", model.DeploymentStepComplete, 0, "", nil); err != nil {
		t.Fatal(err)
	}
	d.Status = model.StatusDeployed
	if err := db.CompleteDeploymentResult(ctx, claim, d, []DeploymentCompletionRegion{{Region: "missing", ActiveWeight: 100}}, "done", nil); err == nil {
		t.Fatal("completion unexpectedly succeeded for invalid region")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.GetOperation(ctx, op.ID)
	if err != nil || recovered.Status != model.OperationFailed || recovered.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("manual recovery receipt=%+v err=%v", recovered, err)
	}
	stored, err := db.GetDeployment(ctx, d.ID)
	if err != nil || stored.Status != model.StatusFailed || len(stored.Regions) != 1 || stored.Regions[0].Status != model.StatusFailed {
		t.Fatalf("control-plane deployment=%+v err=%v", stored, err)
	}
	var archiveIntents int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1 AND subject_id=$2`, op.ID, d.SagaID).Scan(&archiveIntents); err != nil || archiveIntents != 1 {
		t.Fatalf("manual recovery archive intent count=%d err=%v", archiveIntents, err)
	}
	if err := client.VerifyRunningAppImage(ctx, spec, image); err != nil {
		t.Fatalf("live Nomad image disappeared during manual recovery: %v", err)
	}
	if err := db.CompleteDeploymentResult(ctx, claim, d, []DeploymentCompletionRegion{{Region: region.Name, ActiveWeight: 100}}, "late", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale worker terminalized after recovery: %v", err)
	}
}
