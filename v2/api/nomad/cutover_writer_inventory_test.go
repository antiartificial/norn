package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

func cutoverInventorySpec() *model.InfraSpec {
	return &model.InfraSpec{App: "fixture", Databases: []model.DatabaseRequirement{{Name: "appdb", Purpose: "application"}}, Processes: map[string]model.Process{
		"web": {}, "worker": {}, "tick": {Schedule: "@hourly"}, "resize": {Function: &model.FunctionSpec{}},
	}}
}

func TestObserveCutoverWriterJobsIncludesOldAndStoppingWriters(t *testing.T) {
	client := newTestNomadClient(t, cutoverInventoryHandler(t, false))
	inventory, err := client.ObserveCutoverWriterJobs(context.Background(), cutoverInventorySpec(), "global")
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Jobs) != 5 || len(inventory.MissingJobIDs) != 0 || len(inventory.UnexpectedJobIDs) != 1 || inventory.UnexpectedJobIDs[0] != "fixture-rogue" {
		t.Fatalf("inventory coverage=%+v", inventory)
	}
	service := inventory.Jobs[0]
	if service.ID != "fixture" || len(service.LiveAllocations) != 2 || service.LiveAllocations[1].ID != "alloc-old" || service.LiveAllocations[1].JobVersion != 6 || service.LiveAllocations[1].DesiredStatus != nomadapi.AllocDesiredStatusStop {
		t.Fatalf("old or stopping writer omitted: %+v", service)
	}
	if inventory.Jobs[4].ID != "fixture-tick/periodic-1" || inventory.Jobs[4].ParentID != "fixture-tick" {
		t.Fatalf("periodic child omitted: %+v", inventory.Jobs)
	}
}

func TestObserveCutoverWriterJobsRejectsChangingListAndMissingParent(t *testing.T) {
	client := newTestNomadClient(t, cutoverInventoryHandler(t, true))
	if _, err := client.ObserveCutoverWriterJobs(context.Background(), cutoverInventorySpec(), "global"); !errors.Is(err, ErrCutoverWriterInventory) {
		t.Fatalf("changing list accepted: %v", err)
	}
	client = newTestNomadClient(t, cutoverInventoryMissingHandler(t))
	inventory, err := client.ObserveCutoverWriterJobs(context.Background(), cutoverInventorySpec(), "global")
	if err != nil || len(inventory.MissingJobIDs) != 1 || inventory.MissingJobIDs[0] != "fixture-tick" {
		t.Fatalf("missing scheduled parent hidden: %+v %v", inventory, err)
	}
}

func TestObserveCutoverWriterJobsRejectsAllocationChangeWithoutJobRevision(t *testing.T) {
	base := cutoverInventoryHandler(t, false)
	reads := 0
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/job/fixture/allocations" {
			reads++
			if reads == 2 {
				_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-new", JobID: "fixture", JobVersion: 7, TaskGroup: "web", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusPending}})
				return
			}
		}
		base.ServeHTTP(w, r)
	}))
	if _, err := client.ObserveCutoverWriterJobs(context.Background(), cutoverInventorySpec(), "global"); !errors.Is(err, ErrCutoverWriterInventory) {
		t.Fatalf("allocation changed without job revision: %v", err)
	}
}

func cutoverInventoryHandler(t *testing.T, change bool) http.Handler {
	t.Helper()
	stubs := []*nomadapi.JobListStub{
		{ID: "fixture", JobModifyIndex: 44, Status: "running"},
		{ID: "fixture-resize-123", JobModifyIndex: 45, Status: "running"},
		{ID: "fixture-rogue", JobModifyIndex: 46, Status: "running"},
		{ID: "fixture-tick", JobModifyIndex: 47, Status: "running"},
		{ID: "fixture-tick/periodic-1", ParentID: "fixture-tick", JobModifyIndex: 48, Status: "running"},
	}
	lists := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("region") != "global" || r.URL.Query().Get("prefix") != "fixture" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		switch {
		case r.URL.Path == "/v1/jobs":
			lists++
			if change && lists == 2 {
				copy := append([]*nomadapi.JobListStub(nil), stubs...)
				changed := *copy[0]
				changed.JobModifyIndex++
				copy[0] = &changed
				_ = json.NewEncoder(w).Encode(copy)
				return
			}
			_ = json.NewEncoder(w).Encode(stubs)
		case strings.HasSuffix(r.URL.Path, "/allocations"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/job/"), "/allocations")
			if r.URL.Query().Get("all") != "true" {
				t.Fatalf("allocations query=%s", r.URL.RawQuery)
			}
			allocs := []*nomadapi.AllocationListStub{}
			if id == "fixture" {
				allocs = []*nomadapi.AllocationListStub{
					{ID: "alloc-new", JobID: id, JobVersion: 7, TaskGroup: "web", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusPending},
					{ID: "alloc-old", JobID: id, JobVersion: 6, TaskGroup: "worker", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusRunning},
					{ID: "alloc-dead", JobID: id, JobVersion: 6, TaskGroup: "worker", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusComplete},
				}
			}
			_ = json.NewEncoder(w).Encode(allocs)
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/job/")
			for _, stub := range stubs {
				if stub.ID == id {
					version, index, stopped, region := uint64(7), stub.JobModifyIndex, false, "global"
					_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &id, Region: &region, Version: &version, JobModifyIndex: &index, Stop: &stopped})
					return
				}
			}
			http.Error(w, "missing", http.StatusNotFound)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
}

func cutoverInventoryMissingHandler(t *testing.T) http.Handler {
	base := cutoverInventoryHandler(t, false)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" {
			base.ServeHTTP(w, r)
			return
		}
		if r.URL.Query().Get("region") != "global" || r.URL.Query().Get("prefix") != "fixture" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]*nomadapi.JobListStub{{ID: "fixture", JobModifyIndex: 44, Status: "running"}})
	})
}
