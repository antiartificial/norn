package nomad

import (
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestExecTargetMustBelongToAuthorizedApp(t *testing.T) {
	groupName := "web"
	alloc := &nomadapi.Allocation{
		ID: "alloc-1", JobID: "mail-mcp", TaskGroup: groupName, ClientStatus: "running",
		Job: &nomadapi.Job{TaskGroups: []*nomadapi.TaskGroup{{
			Name: &groupName, Tasks: []*nomadapi.Task{{Name: "server"}},
		}}},
	}
	if _, _, err := execTargetFromAllocation("atlas", "", alloc); err == nil {
		t.Fatal("allocation from another app must be rejected")
	}
	allocID, task, err := execTargetFromAllocation("mail-mcp", "web", alloc)
	if err != nil {
		t.Fatal(err)
	}
	if allocID != "alloc-1" || task != "server" {
		t.Fatalf("resolved %q/%q", allocID, task)
	}
	if _, _, err := execTargetFromAllocation("mail-mcp", "worker", alloc); err == nil {
		t.Fatal("allocation from another process must be rejected")
	}
}
