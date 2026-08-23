package nomad

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestCronRunHealthDetectsOOMRestartsAndFailedAllocation(t *testing.T) {
	t.Parallel()

	restartedAt := time.Date(2026, 8, 22, 10, 6, 23, 0, time.FixedZone("CDT", -5*60*60))
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/allocations") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("all"); got != "true" {
			t.Fatalf("all query = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
			{
				ID:           "running",
				ClientStatus: nomadapi.AllocClientStatusRunning,
				TaskStates: map[string]*nomadapi.TaskState{
					"sync": {
						State:       "running",
						Restarts:    2,
						LastRestart: restartedAt,
						Events: []*nomadapi.TaskEvent{{
							Type:           nomadapi.TaskTerminated,
							Time:           restartedAt.UnixNano(),
							DisplayMessage: `Exit Code: 0, Exit Message: "OOM Killed"`,
							Details:        map[string]string{"oom_killed": "true"},
						}},
					},
				},
			},
			{ID: "failed", ClientStatus: nomadapi.AllocClientStatusFailed},
		})
	}))

	health, err := client.CronRunHealth("field-harbor-sync/periodic-1")
	if err != nil {
		t.Fatal(err)
	}
	if health.RunningAllocations != 1 || health.FailedAllocations != 1 {
		t.Fatalf("allocation counts = running %d failed %d", health.RunningAllocations, health.FailedAllocations)
	}
	if health.Restarts != 2 || !health.OOMKilled {
		t.Fatalf("restart health = restarts %d oom %t", health.Restarts, health.OOMKilled)
	}
	if !health.LastRestart.Equal(restartedAt) {
		t.Fatalf("last restart = %s, want %s", health.LastRestart, restartedAt)
	}
}

func TestRestartJobStopsOnlyActiveDesiredAllocations(t *testing.T) {
	t.Parallel()

	var stopped []string
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/like-trove/allocations":
			if got := r.URL.Query().Get("all"); got != "false" {
				t.Fatalf("all query = %q, want false", got)
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
				{ID: "running", Namespace: "apps", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
				{ID: "pending", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusPending},
				{ID: "unknown", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusUnknown},
				{ID: "complete", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusComplete},
				{ID: "stopping", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusRunning},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/allocation/") && strings.HasSuffix(r.URL.Path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/allocation/"), "/stop")
			if id == "running" && r.URL.Query().Get("namespace") != "apps" {
				t.Fatalf("namespace query = %q, want apps", r.URL.Query().Get("namespace"))
			}
			stopped = append(stopped, id)
			_ = json.NewEncoder(w).Encode(nomadapi.AllocStopResponse{EvalID: "eval-" + id})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))

	if err := client.RestartJob("like-trove"); err != nil {
		t.Fatalf("RestartJob() error = %v", err)
	}
	if want := []string{"running", "pending", "unknown"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stopped allocations = %v, want %v", stopped, want)
	}
}

func TestRestartJobRequiresActiveAllocation(t *testing.T) {
	t.Parallel()

	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/batch/allocations" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
			{ID: "done", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusComplete},
		})
	}))

	err := client.RestartJob("batch")
	if err == nil || !strings.Contains(err.Error(), "no active allocations found") {
		t.Fatalf("RestartJob() error = %v, want no active allocations error", err)
	}
}

func TestRestartJobReportsAllocationStopFailure(t *testing.T) {
	t.Parallel()

	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/api/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
				{ID: "broken", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/allocation/broken/stop":
			http.Error(w, "stop rejected", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))

	err := client.RestartJob("api")
	if err == nil || !strings.Contains(err.Error(), "stop allocation broken") {
		t.Fatalf("RestartJob() error = %v, want allocation stop error", err)
	}
}

func TestIsRestartableAllocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		alloc *nomadapi.AllocationListStub
		want  bool
	}{
		{name: "nil", alloc: nil},
		{name: "missing id", alloc: &nomadapi.AllocationListStub{DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning}},
		{name: "desired stop", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusRunning}},
		{name: "failed", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusFailed}},
		{name: "running", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRestartableAllocation(tt.alloc); got != tt.want {
				t.Fatalf("isRestartableAllocation() = %t, want %t", got, tt.want)
			}
		})
	}
}

func newTestNomadClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := nomadapi.DefaultConfig()
	cfg.Address = server.URL
	apiClient, err := nomadapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("new Nomad client: %v", err)
	}
	return &Client{api: apiClient}
}
