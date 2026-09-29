package nomad

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// Requires a disposable Nomad agent; it registers only a fixture job there.
func TestObserveCutoverWriterJobsNomadReadback(t *testing.T) {
	addr := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if addr == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to a disposable Nomad agent")
	}
	client, err := NewClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	app := "cutover-inventory-" + uuid.NewString()
	job := nomadapi.NewServiceJob(app, app, "global", 50)
	group := nomadapi.NewTaskGroup("web", 1)
	group.Tasks = []*nomadapi.Task{{Name: "web", Driver: "docker", Config: map[string]interface{}{"image": "docker.io/library/busybox:1.36", "command": "sleep", "args": []string{"60"}}}}
	job.TaskGroups = []*nomadapi.TaskGroup{group}
	if _, _, err := client.API().Jobs().Register(job, &nomadapi.WriteOptions{Region: "global"}); err != nil {
		t.Fatal(err)
	}
	spec := &model.InfraSpec{App: app, Databases: []model.DatabaseRequirement{{Name: "appdb", Purpose: "application"}}, Processes: map[string]model.Process{"web": {}}}
	var inventory CutoverWriterInventory
	for attempt := 0; attempt < 10; attempt++ {
		inventory, err = client.ObserveCutoverWriterJobs(context.Background(), spec, "global")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || len(inventory.Jobs) != 1 || inventory.Jobs[0].ID != app || len(inventory.MissingJobIDs) != 0 || len(inventory.UnexpectedJobIDs) != 0 || inventory.Jobs[0].JobModifyIndex == 0 {
		t.Fatalf("Nomad readback: %+v %v", inventory, err)
	}
}
