package pipeline

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
)

// TestLiveDeploymentReconciliation is opt-in and uses an isolated control
// schema and a uniquely named job on a disposable loopback Nomad agent.
func TestLiveDeploymentReconciliation(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_FUNCTION_IMAGE")
	if address == "" || image == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR, NORN_TEST_FUNCTION_IMAGE and NORN_TEST_NOMAD_DOCKER=1")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("qualification requires loopback Nomad and a content-addressed image")
	}
	p, db, request := acceptancePipelineFixture(t)
	p.Nomad, err = nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := fmt.Sprintf("norn-reconcile-%d", time.Now().UnixNano())
	spec := &model.InfraSpec{App: app, Deploy: true, Processes: map[string]model.Process{"web": {Command: "sleep 120"}}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	region := spec.ResolvedRegions()[0]
	now := time.Now().UTC().Add(-time.Second)
	d := &model.Deployment{ID: uuid.NewString(), App: app, SagaID: uuid.NewString(), Status: model.StatusQueued, ImageTag: image,
		SpecDigest: digest, Environment: "staging", StartedAt: now,
		SourceKind: "git_clone", SourceRef: "0123456789abcdef0123456789abcdef01234567", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	source := model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: app, SagaID: d.SagaID, Status: model.OperationQueued,
		StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"deploymentId": d.ID, "specDigest": digest}}
	request.Key = "source-deployment"
	accepted, err := p.acceptOperation(context.Background(), request, source, d, []model.ResolvedRegion{region})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	claimedSource, _, err := db.ClaimNextOperation(ctx, "source-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimedSource == nil || claimedSource.ID != accepted.Operation.ID {
		t.Fatalf("source claim=%+v err=%v", claimedSource, err)
	}
	if err := db.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: d.ID, App: app, SagaID: d.SagaID,
		Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepComplete, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	job := nomad.TranslateForRegion(spec, image, nil, region)
	job.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	if _, err := p.Nomad.SubmitJob(job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Nomad.StopJob(app, true) })
	for {
		if err := p.Nomad.VerifyRunningAppImage(ctx, spec, image); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("Nomad image never became provable: %v", err)
		}
		time.Sleep(time.Second)
	}
	if err := db.UpdateDeploymentRegion(ctx, d.ID, region.Name, model.StatusSubmitting, "eval-live", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	request.Key = "operator-reconciliation"
	repair, err := p.QueueDeploymentReconciliation(ctx, spec, source.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "repair-worker", time.Minute, []string{"app.deployment-reconcile"})
	if err != nil || claimed == nil || claimed.ID != repair.Operation.ID {
		t.Fatalf("repair claim=%+v err=%v", claimed, err)
	}
	result, err := p.executeDeploymentReconciliation(ctx, claimed, claim, spec, saga.NewWithID(p.SagaStore, claimed.SagaID, app, "pipeline", "deploy"))
	if err != nil || result == nil || result.Status != model.OperationSucceeded || !result.Finished() {
		t.Fatalf("reconciliation result=%+v err=%v", result, err)
	}
	original, err := db.GetOperation(ctx, source.ID)
	if err != nil || original.Status != model.OperationFailed || original.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("original failure changed=%+v err=%v", original, err)
	}
	stored, err := db.GetDeployment(ctx, d.ID)
	if err != nil || stored.Status != model.StatusDeployed || stored.ImageTag != image || len(stored.Regions) != 1 || stored.Regions[0].Status != model.StatusDeployed {
		t.Fatalf("reconciled deployment=%+v err=%v", stored, err)
	}
	completed, err := db.GetOperation(ctx, repair.Operation.ID)
	if err != nil || completed.Status != model.OperationSucceeded || completed.Metadata["sourceOperationId"] != source.ID {
		t.Fatalf("operator receipt=%+v err=%v", completed, err)
	}
	var intents int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1 AND subject_id=$2`, repair.Operation.ID, repair.Operation.SagaID).Scan(&intents); err != nil || intents != 1 {
		t.Fatalf("operator archive intent count=%d err=%v", intents, err)
	}
}
