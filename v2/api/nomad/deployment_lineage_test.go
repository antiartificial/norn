package nomad

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestObserveActiveDeploymentLineageDerivesExactServingProcess(t *testing.T) {
	fixture := newActiveDeploymentLineageFixture(t)
	defer fixture.server.Close()
	client, err := NewClient(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.ObserveActiveDeploymentLineage(context.Background(), fixture.app, fixture.placement,
		fixture.region, fixture.namespace, fixture.deployment, fixture.operation, fixture.specDigest, fixture.jobDigest, fixture.image, "web")
	if err != nil {
		t.Fatal(err)
	}
	if observed.JobID != fixture.jobID || observed.JobDigest != fixture.jobDigest || observed.JobVersion != fixture.version ||
		observed.ModifyIndex != fixture.modifyIndex || observed.Replicas != 2 || observed.Process != "web" ||
		len(observed.AllocationIDs) != 2 || observed.AllocationIDs[0] != "alloc-web-1" || observed.AllocationIDs[1] != "alloc-web-2" {
		t.Fatalf("active deployment lineage=%+v", observed)
	}
	if strings.Contains(strings.Join(observed.AllocationIDs, ","), "worker") {
		t.Fatalf("lineage included an unrelated process allocation: %+v", observed.AllocationIDs)
	}
}

func TestObserveActiveDeploymentLineageRejectsForeignOperationMarker(t *testing.T) {
	fixture := newActiveDeploymentLineageFixture(t)
	defer fixture.server.Close()
	fixture.current.Meta[DeploymentOperationIDMeta] = "foreign-operation"
	client, err := NewClient(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ObserveActiveDeploymentLineage(context.Background(), fixture.app, fixture.placement,
		fixture.region, fixture.namespace, fixture.deployment, fixture.operation, fixture.specDigest, fixture.jobDigest, fixture.image, "web"); err == nil {
		t.Fatal("accepted a Nomad job carrying a foreign operation marker")
	}
}

func TestObserveActiveDeploymentLineageRejectsUnanchoredDigest(t *testing.T) {
	fixture := newActiveDeploymentLineageFixture(t)
	defer fixture.server.Close()
	client, err := NewClient(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ObserveActiveDeploymentLineage(context.Background(), fixture.app, fixture.placement,
		fixture.region, fixture.namespace, fixture.deployment, fixture.operation, fixture.specDigest,
		strings.Repeat("c", 64), fixture.image, "web"); err == nil {
		t.Fatal("accepted a Nomad submission digest that differs from the trusted expected digest")
	}
}

type activeDeploymentLineageFixture struct {
	server                  *httptest.Server
	current                 *nomadapi.Job
	app, placement, region  string
	namespace, deployment   string
	operation, specDigest   string
	image, jobID, jobDigest string
	version, modifyIndex    uint64
}

func newActiveDeploymentLineageFixture(t *testing.T) *activeDeploymentLineageFixture {
	t.Helper()
	fixture := &activeDeploymentLineageFixture{app: "demo", placement: "west", region: "global", namespace: "default",
		deployment: "deploy-old", operation: "operation-old", specDigest: "sha256:" + strings.Repeat("a", 64),
		image: "registry.example.test/demo@sha256:" + strings.Repeat("b", 64), version: 4, modifyIndex: 44}
	var err error
	fixture.jobID, err = ManagedDeploymentJobID(fixture.app, fixture.placement, fixture.deployment)
	if err != nil {
		t.Fatal(err)
	}
	jobName, kind, stopped, namespace := fixture.jobID, "service", false, fixture.namespace
	webName, workerName, webCount, workerCount := "web", "worker", 2, 1
	fixture.current = &nomadapi.Job{ID: &jobName, Name: &jobName, Region: &fixture.region, Namespace: &namespace,
		Type: &kind, Stop: &stopped, Meta: map[string]string{DeploymentIDMeta: fixture.deployment,
			SpecDigestMeta: fixture.specDigest, DeploymentOperationIDMeta: fixture.operation},
		TaskGroups: []*nomadapi.TaskGroup{
			{Name: &webName, Count: &webCount, Tasks: []*nomadapi.Task{{Name: webName, Driver: "docker", Config: map[string]interface{}{"image": fixture.image}}}},
			{Name: &workerName, Count: &workerCount, Tasks: []*nomadapi.Task{{Name: workerName, Driver: "docker", Config: map[string]interface{}{"image": fixture.image}}}},
		}}
	fixture.jobDigest, err = DigestDeploymentJob(fixture.current)
	if err != nil {
		t.Fatal(err)
	}
	fixture.current.Meta[DeploymentJobDigestMeta] = fixture.jobDigest
	fixture.current.Meta[DeploymentExecutionIDMeta] = DeploymentEffectExecutionID(fixture.operation, fixture.placement, fixture.jobDigest)
	source, err := json.Marshal(fixture.current)
	if err != nil {
		t.Fatal(err)
	}
	version, modifyIndex := fixture.version, fixture.modifyIndex
	fixture.current.Version, fixture.current.JobModifyIndex = &version, &modifyIndex
	healthy := true
	stubs := []*nomadapi.AllocationListStub{
		lineageStub("alloc-web-1", fixture.jobID, fixture.version, "web", "node-1", &healthy),
		lineageStub("alloc-web-2", fixture.jobID, fixture.version, "web", "node-2", &healthy),
		lineageStub("alloc-worker-1", fixture.jobID, fixture.version, "worker", "node-3", &healthy),
	}
	allocations := make(map[string]*nomadapi.Allocation, len(stubs))
	for _, stub := range stubs {
		allocations[stub.ID] = &nomadapi.Allocation{ID: stub.ID, NodeID: stub.NodeID, JobID: fixture.jobID,
			Namespace: fixture.namespace, TaskGroup: stub.TaskGroup, Job: fixture.current,
			ClientStatus: stub.ClientStatus, DesiredStatus: stub.DesiredStatus,
			TaskStates: map[string]*nomadapi.TaskState{stub.TaskGroup: {State: "running"}}}
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + fixture.jobID:
			_ = json.NewEncoder(w).Encode(fixture.current)
		case "/v1/job/" + fixture.jobID + "/submission":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobSubmission{Source: string(source), Format: "json"})
		case "/v1/job/" + fixture.jobID + "/plan":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobPlanResponse{JobModifyIndex: fixture.modifyIndex,
				Diff: &nomadapi.JobDiff{Type: "None"}})
		case "/v1/job/" + fixture.jobID + "/allocations":
			_ = json.NewEncoder(w).Encode(stubs)
		default:
			if allocation, ok := allocations[strings.TrimPrefix(r.URL.Path, "/v1/allocation/")]; ok {
				_ = json.NewEncoder(w).Encode(allocation)
				return
			}
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	return fixture
}

func lineageStub(id, jobID string, version uint64, group, node string, healthy *bool) *nomadapi.AllocationListStub {
	return &nomadapi.AllocationListStub{ID: id, NodeID: node, JobID: jobID, JobVersion: version,
		TaskGroup: group, ClientStatus: nomadapi.AllocClientStatusRunning, DesiredStatus: nomadapi.AllocDesiredStatusRun,
		DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: healthy}}
}
