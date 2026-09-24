package nomad

import (
	"testing"

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

func TestAbsentDesiredReplicaIntentUsesInfraSpecMinimum(t *testing.T) {
	spec := &model.InfraSpec{App: "widget", Processes: map[string]model.Process{"web": {Scaling: &model.Scaling{Min: 2}}}}
	job := TranslateForRegion(spec, "example.test/widget:1", nil, spec.ResolvedRegions()[0])
	ApplyDesiredReplicaCounts(job, nil)
	if got := *job.TaskGroups[0].Count; got != 2 {
		t.Fatalf("old-data count=%d, want infraspec minimum 2", got)
	}
}
