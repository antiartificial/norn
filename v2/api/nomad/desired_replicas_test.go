package nomad

import (
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

func TestDesiredReplicaIntentSurvivesRedeployTranslation(t *testing.T) {
	spec := &model.InfraSpec{App: "widget", Processes: map[string]model.Process{"web": {Scaling: &model.Scaling{Min: 1}}}}
	job := TranslateForRegion(spec, "example.test/widget:1", nil, spec.ResolvedRegions()[0])
	ApplyDesiredReplicaCounts(job, map[string]int{"web": 3})
	if got := *job.TaskGroups[0].Count; got != 3 {
		t.Fatalf("redeploy count=%d, want durable scale intent 3", got)
	}
}

func TestDesiredReplicaOverlayPreservesRequiredHostPlacement(t *testing.T) {
	spec := &model.InfraSpec{App: "widget", Processes: map[string]model.Process{
		"web": {Scaling: &model.Scaling{Min: 1}, Placement: &model.ProcessPlacement{DistinctHosts: true}},
	}}
	job := TranslateForRegion(spec, "example.test/widget:1", nil, spec.ResolvedRegions()[0])
	ApplyDesiredReplicaCounts(job, map[string]int{"web": 3})
	if len(job.TaskGroups) != 1 {
		t.Fatalf("task groups=%d, want one web group", len(job.TaskGroups))
	}
	group := job.TaskGroups[0]
	if group.Count == nil || *group.Count != 3 {
		t.Fatalf("redeploy count=%v, want durable scale intent 3", group.Count)
	}
	if len(group.Constraints) != 1 || group.Constraints[0].Operand != nomadapi.ConstraintDistinctHosts {
		t.Fatalf("redeploy placement constraints=%#v, want required distinct hosts", group.Constraints)
	}
}

func TestAbsentDesiredReplicaIntentUsesInfraSpecMinimum(t *testing.T) {
	spec := &model.InfraSpec{App: "widget", Processes: map[string]model.Process{"web": {Scaling: &model.Scaling{Min: 2}}}}
	job := TranslateForRegion(spec, "example.test/widget:1", nil, spec.ResolvedRegions()[0])
	ApplyDesiredReplicaCounts(job, nil)
	if got := *job.TaskGroups[0].Count; got != 2 {
		t.Fatalf("old-data count=%d, want infraspec minimum 2", got)
	}
}
