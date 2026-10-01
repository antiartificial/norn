package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
	"norn/v2/api/store"
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
	authority, err := p.ScaleEffects.store.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := scaleResource(scaleRequest{App: jobID, Group: "web", Region: "local"})
	deployment, err := client.LatestDeploymentRegion(jobID, "global")
	if err != nil {
		t.Fatal(err)
	}
	if deployment == nil || (deployment.Status != "running" && deployment.Status != "pending") {
		t.Fatalf("initial deployment must be active to prove preflight: %+v", deployment)
	}
	if result, err := p.ExecuteOperation(ctx, claimed, claim); result != nil || err == nil {
		t.Fatalf("scale during initial deployment = %+v, %v", result, err)
	}
	if record, found, err := p.ScaleEffects.store.UnresolvedForResource(ctx, authority, resource); err != nil || found {
		t.Fatalf("early scale reserved an effect: found=%t record=%+v err=%v", found, record, err)
	}
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

// TestM4ReplicaIntentAcrossThreeDisposableNomadClients exercises the Norn side
// of the M4 capacity gate against real scheduler state. The companion harness
// starts three loopback Nomad clients in the app node pool; this test proves
// durable 2 -> 3 -> redeploy -> 3 -> 2 replica intent and distinct placement.
func TestM4ReplicaIntentAcrossThreeDisposableNomadClients(t *testing.T) {
	addr := os.Getenv("NORN_TEST_M4_NOMAD_ADDR")
	if addr == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("disposable M4 Nomad and PostgreSQL URLs are required")
	}
	parsed, err := url.Parse(addr)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("disposable M4 Nomad address must be a loopback HTTP URL")
	}
	p, db, request := acceptancePipelineFixture(t)
	client, err := nomad.NewClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := client.API().Nodes().List(nil)
	if err != nil {
		t.Fatal(err)
	}
	eligible := 0
	for _, node := range nodes {
		if node != nil && node.Status == "ready" && node.SchedulingEligibility == "eligible" && node.NodePool == "app" {
			eligible++
		}
	}
	if eligible != 3 {
		t.Fatalf("eligible app clients=%d, want exactly 3 disposable clients", eligible)
	}

	image := os.Getenv("NORN_TEST_M4_IMAGE")
	if image == "" {
		t.Fatal("NORN_TEST_M4_IMAGE is required")
	}
	jobID := "norn-m4-capacity-" + uuid.NewString()
	t.Cleanup(func() { stopM4Job(t, client, jobID) })
	p.Nomad = client
	p.ScaleEffects, err = NewNomadScaleEffects(db, client)
	if err != nil {
		t.Fatal(err)
	}
	spec := &model.InfraSpec{App: jobID, Deploy: true, Placement: &model.PlacementSpec{NodePool: "app", DistinctHosts: true},
		Processes: map[string]model.Process{"web": {Command: "sleep 600", Scaling: &model.Scaling{Min: 2}}}}
	submitM4Deployment(t, p, spec, image)

	waitM4Placement(t, client.API(), jobID, 2)
	waitM4Deployment(t, client, jobID)
	runM4Scale(t, p, db, request, jobID, 3)
	waitM4Placement(t, client.API(), jobID, 3)

	// The InfraSpec still declares two replicas. A real Norn submit must load
	// the acknowledged desired count and keep three, rather than resetting it.
	submitM4Deployment(t, p, spec, image)
	waitM4Placement(t, client.API(), jobID, 3)
	waitM4Deployment(t, client, jobID)

	runM4Scale(t, p, db, request, jobID, 2)
	waitM4Placement(t, client.API(), jobID, 2)
}

func stopM4Job(t *testing.T, client *nomad.Client, jobID string) {
	t.Helper()
	if err := client.StopJob(jobID, true); err != nil {
		t.Errorf("purge M4 job: %v", err)
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		allocations, _, err := client.API().Jobs().Allocations(jobID, false, nil)
		running := 0
		for _, allocation := range allocations {
			if allocation.ClientStatus == nomadapi.AllocClientStatusPending || allocation.ClientStatus == nomadapi.AllocClientStatusRunning {
				running++
			}
		}
		if err == nil && running == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("M4 job cleanup retained %d live allocations: %v", running, err)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitM4Deployment(t *testing.T, client *nomad.Client, jobID string) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for {
		deployment, err := client.LatestDeploymentRegion(jobID, "global")
		if err == nil && deployment != nil && deployment.Status == "successful" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Nomad deployment did not settle: deployment=%+v err=%v", deployment, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func submitM4Deployment(t *testing.T, p *Pipeline, spec *model.InfraSpec, image string) {
	t.Helper()
	deploymentID := uuid.NewString()
	st := &state{spec: spec, imageTag: image, deploymentID: deploymentID, regionEvals: map[string]string{},
		operationPayload: map[string]interface{}{"specDigest": "sha256:" + strings.Repeat("a", 64)}}
	log := saga.New(saga.DiscardStore{}, spec.App, "m4-disposable-integration", "deploy")
	if err := p.submit(context.Background(), st, log); err != nil {
		t.Fatalf("submit deployment %s through Norn pipeline: %v", deploymentID, err)
	}
}

func runM4Scale(t *testing.T, p *Pipeline, db *store.DB, request EnqueueRequest, jobID string, count int) {
	t.Helper()
	request.Key = fmt.Sprintf("m4-scale-%d-%s", count, uuid.NewString())
	op := model.Operation{ID: uuid.NewString(), Kind: "app.scale", App: jobID, SagaID: uuid.NewString(), Status: model.OperationQueued,
		Source: "m4-disposable-integration", StartedAt: time.Now().UTC(), MaxAttempts: 3,
		Payload: map[string]interface{}{"group": "web", "region": "local", "nomadRegion": "global", "count": count}}
	accepted, err := p.QueueOperation(context.Background(), op, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "m4-scale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim scale %d = %+v, %v", count, claimed, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		result, executeErr := p.ExecuteOperation(context.Background(), claimed, claim)
		if executeErr == nil && result != nil && result.Status == model.OperationSucceeded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("scale to %d did not complete: result=%+v err=%v", count, result, executeErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitM4Placement(t *testing.T, api *nomadapi.Client, jobID string, count int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		allocations, _, err := api.Jobs().Allocations(jobID, false, nil)
		nodes := map[string]struct{}{}
		running := 0
		for _, allocation := range allocations {
			if allocation.TaskGroup == "web" && allocation.DesiredStatus == nomadapi.AllocDesiredStatusRun && allocation.ClientStatus == nomadapi.AllocClientStatusRunning {
				running++
				nodes[allocation.NodeID] = struct{}{}
			}
		}
		if err == nil && running == count && len(nodes) == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("running allocations=%d distinct placements=%d, want %d (err=%v)", running, len(nodes), count, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
