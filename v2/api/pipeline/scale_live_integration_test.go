package pipeline

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// This opt-in test exercises signed acceptance, a claimed PostgreSQL effect,
// real Nomad scaling metadata, and the atomic durable replica-intent result.
// It creates and purges only its own job on a selected loopback Nomad agent.
func TestClaimedScaleAgainstDisposablePostgresAndNomad(t *testing.T) {
	addr := os.Getenv("NORN_TEST_DISPOSABLE_NOMAD_ADDR")
	if addr == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("disposable Nomad and PostgreSQL URLs are required")
	}
	parsed, err := url.Parse(addr)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("disposable Nomad address must be a loopback HTTP URL")
	}
	p, db, request := acceptancePipelineFixture(t)
	client, err := nomad.NewClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "norn-scale-probe-" + uuid.NewString()
	job := nomadapi.NewServiceJob(jobID, jobID, "global", 50).AddDatacenter("dc1")
	job.AddTaskGroup(nomadapi.NewTaskGroup("web", 1).AddTask(
		nomadapi.NewTask("sleep", "raw_exec").SetConfig("command", "/bin/sleep").SetConfig("args", []string{"120"}),
	))
	if _, err := client.SubmitJob(job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.StopJob(jobID, true); err != nil {
			t.Errorf("purge disposable Nomad job: %v", err)
		}
	})
	deploymentDeadline := time.Now().Add(20 * time.Second)
	for {
		deployment, err := client.LatestDeploymentRegion(jobID, "global")
		if err == nil && deployment != nil && deployment.Status == "successful" {
			break
		}
		if time.Now().After(deploymentDeadline) {
			t.Fatalf("initial Nomad deployment did not settle: deployment=%+v err=%v", deployment, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.ScaleEffects, err = NewNomadScaleEffects(db, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request.Key = "scale-" + uuid.NewString()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.scale", App: jobID, SagaID: uuid.NewString(),
		Status: model.OperationQueued, Source: "disposable-integration", StartedAt: time.Now().UTC(), MaxAttempts: 3,
		Payload: map[string]interface{}{"group": "web", "region": "local", "nomadRegion": "global", "count": 2}}
	accepted, err := p.QueueOperation(ctx, op, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "disposable-scale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := p.ExecuteOperation(ctx, claimed, claim)
		if err == nil && result != nil && result.Status == model.OperationSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("claimed scale did not complete: result=%+v err=%v", result, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	counts, err := db.DesiredReplicaCounts(ctx, jobID, "local")
	if err != nil || counts["web"] != 2 {
		t.Fatalf("accepted replica intent = %+v, %v", counts, err)
	}
	terminal, err := db.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || terminal == nil || terminal.Status != model.OperationSucceeded {
		t.Fatalf("terminal scale operation = %+v, %v", terminal, err)
	}
	if desired, _, _, err := client.ScaleStatus(jobID, "web", "global", claimed.ID, "1", "", 2, ""); err != nil || desired != 2 {
		t.Fatalf("Nomad desired scale = %d, %v", desired, err)
	}
}
