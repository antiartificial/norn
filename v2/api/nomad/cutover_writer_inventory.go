package nomad

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

var ErrCutoverWriterInventory = errors.New("cutover writer inventory is incomplete or changed during observation")

// CutoverWriterInventory is a read-only Nomad snapshot. It never claims to
// cover external integrations, database sessions, queues or connection pools.
type CutoverWriterInventory struct {
	App              string             `json:"app"`
	NomadRegion      string             `json:"nomadRegion"`
	Jobs             []CutoverWriterJob `json:"jobs"`
	MissingJobIDs    []string           `json:"missingJobIds,omitempty"`
	UnexpectedJobIDs []string           `json:"unexpectedJobIds,omitempty"`
}

type CutoverWriterJob struct {
	ID              string                    `json:"id"`
	ParentID        string                    `json:"parentId,omitempty"`
	Version         uint64                    `json:"version"`
	JobModifyIndex  uint64                    `json:"jobModifyIndex"`
	Stopped         bool                      `json:"stopped"`
	Status          string                    `json:"status"`
	LiveAllocations []CutoverWriterAllocation `json:"liveAllocations"`
}

type CutoverWriterAllocation struct {
	ID            string `json:"id"`
	JobVersion    uint64 `json:"jobVersion"`
	TaskGroup     string `json:"taskGroup"`
	DesiredStatus string `json:"desiredStatus"`
	ClientStatus  string `json:"clientStatus"`
}

// ObserveCutoverWriterJobs enumerates every Nomad job with the app's ID
// prefix in one region. This deliberately includes unexpected IDs and live
// allocations from old job versions or desired-stop allocations. A second
// list and per-job readback reject a changing view. The caller must inspect
// every region and reconcile all non-Nomad writers before using this as input
// to a signed writer inventory.
func (c *Client) ObserveCutoverWriterJobs(ctx context.Context, spec *model.InfraSpec, nomadRegion string) (CutoverWriterInventory, error) {
	if c == nil || c.api == nil || spec == nil || spec.App == "" || !spec.DeclaresDatabase() || nomadRegion == "" || strings.ContainsAny(spec.App, " /\x00\r\n") {
		return CutoverWriterInventory{}, ErrCutoverWriterInventory
	}
	query := (&nomadapi.QueryOptions{Region: nomadRegion, Prefix: spec.App}).WithContext(ctx)
	first, _, err := c.api.Jobs().List(query)
	if err != nil {
		return CutoverWriterInventory{}, err
	}
	selected, err := cutoverJobStubs(first, spec.App)
	if err != nil {
		return CutoverWriterInventory{}, err
	}
	result := CutoverWriterInventory{App: spec.App, NomadRegion: nomadRegion, Jobs: make([]CutoverWriterJob, 0, len(selected))}
	regions := make([]string, 0)
	for _, region := range spec.ResolvedRegions() {
		if region.NomadRegion == nomadRegion {
			regions = append(regions, region.Name)
		}
	}
	if len(regions) == 0 {
		return CutoverWriterInventory{}, ErrCutoverWriterInventory
	}
	expected := map[string]bool{}
	functions := make([]string, 0)
	for name, process := range spec.Processes {
		placed := false
		for _, region := range regions {
			if spec.ProcessRunsInRegion(process, region) {
				placed = true
				break
			}
		}
		if !placed {
			continue
		}
		if process.Schedule != "" {
			expected[spec.App+"-"+name] = true
		}
		if process.Function != nil {
			functions = append(functions, spec.App+"-"+name+"-")
		}
		if process.Schedule == "" && process.Function == nil {
			expected[spec.App] = true
		}
	}
	seenExpected := map[string]bool{}
	for _, stub := range selected {
		known := expected[stub.ID]
		if known {
			seenExpected[stub.ID] = true
		}
		if !known && stub.ParentID != "" && expected[stub.ParentID] && strings.HasPrefix(stub.ID, stub.ParentID+"/periodic-") {
			known = true
		}
		if !known {
			for _, prefix := range functions {
				if strings.HasPrefix(stub.ID, prefix) {
					known = true
					break
				}
			}
		}
		if !known {
			result.UnexpectedJobIDs = append(result.UnexpectedJobIDs, stub.ID)
		}
		job, _, err := c.api.Jobs().Info(stub.ID, query)
		if err != nil {
			return CutoverWriterInventory{}, err
		}
		if job == nil || job.ID == nil || *job.ID != stub.ID || job.Version == nil || job.JobModifyIndex == nil || job.Stop == nil || *job.JobModifyIndex != stub.JobModifyIndex || job.Region == nil || *job.Region != nomadRegion {
			return CutoverWriterInventory{}, ErrCutoverWriterInventory
		}
		allocations, _, err := c.api.Jobs().Allocations(stub.ID, true, query)
		if err != nil {
			return CutoverWriterInventory{}, err
		}
		live, err := cutoverLiveAllocations(stub.ID, allocations)
		if err != nil {
			return CutoverWriterInventory{}, err
		}
		item := CutoverWriterJob{ID: stub.ID, ParentID: stub.ParentID, Version: *job.Version, JobModifyIndex: *job.JobModifyIndex, Stopped: *job.Stop, Status: stub.Status, LiveAllocations: live}
		current, _, err := c.api.Jobs().Info(stub.ID, query)
		if err != nil || current == nil || current.Version == nil || current.JobModifyIndex == nil || current.Stop == nil || *current.Version != item.Version || *current.JobModifyIndex != item.JobModifyIndex || *current.Stop != item.Stopped {
			return CutoverWriterInventory{}, ErrCutoverWriterInventory
		}
		result.Jobs = append(result.Jobs, item)
	}
	for id := range expected {
		if !seenExpected[id] {
			result.MissingJobIDs = append(result.MissingJobIDs, id)
		}
	}
	sort.Strings(result.MissingJobIDs)
	second, _, err := c.api.Jobs().List(query)
	if err != nil {
		return CutoverWriterInventory{}, err
	}
	selectedAgain, err := cutoverJobStubs(second, spec.App)
	if err != nil || len(selectedAgain) != len(selected) {
		return CutoverWriterInventory{}, ErrCutoverWriterInventory
	}
	for i := range selected {
		if selected[i].ID != selectedAgain[i].ID || selected[i].JobModifyIndex != selectedAgain[i].JobModifyIndex || selected[i].Status != selectedAgain[i].Status || selected[i].Stop != selectedAgain[i].Stop {
			return CutoverWriterInventory{}, ErrCutoverWriterInventory
		}
	}
	// Allocation status can change without changing the job definition index.
	for _, item := range result.Jobs {
		stubs, _, err := c.api.Jobs().Allocations(item.ID, true, query)
		if err != nil {
			return CutoverWriterInventory{}, err
		}
		live, err := cutoverLiveAllocations(item.ID, stubs)
		if err != nil || !reflect.DeepEqual(live, item.LiveAllocations) {
			return CutoverWriterInventory{}, ErrCutoverWriterInventory
		}
	}
	return result, nil
}

func cutoverLiveAllocations(jobID string, allocations []*nomadapi.AllocationListStub) ([]CutoverWriterAllocation, error) {
	live := []CutoverWriterAllocation{}
	seen := map[string]bool{}
	for _, allocation := range allocations {
		if allocation == nil || allocation.ID == "" || allocation.JobID != jobID || seen[allocation.ID] {
			return nil, ErrCutoverWriterInventory
		}
		seen[allocation.ID] = true
		if allocation.ClientStatus == nomadapi.AllocClientStatusPending || allocation.ClientStatus == nomadapi.AllocClientStatusRunning {
			live = append(live, CutoverWriterAllocation{ID: allocation.ID, JobVersion: allocation.JobVersion, TaskGroup: allocation.TaskGroup, DesiredStatus: allocation.DesiredStatus, ClientStatus: allocation.ClientStatus})
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })
	return live, nil
}

func cutoverJobStubs(all []*nomadapi.JobListStub, app string) ([]*nomadapi.JobListStub, error) {
	selected := make([]*nomadapi.JobListStub, 0)
	seen := map[string]bool{}
	for _, item := range all {
		if item == nil || item.ID == "" {
			return nil, ErrCutoverWriterInventory
		}
		if item.ID != app && !strings.HasPrefix(item.ID, app+"-") {
			continue
		}
		if seen[item.ID] {
			return nil, ErrCutoverWriterInventory
		}
		seen[item.ID] = true
		selected = append(selected, item)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	return selected, nil
}
