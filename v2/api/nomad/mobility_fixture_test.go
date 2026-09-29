package nomad

import (
	"path/filepath"
	"testing"

	"norn/v2/api/model"
)

func TestMobilityFixtureProcessesUseEntrypointAndMountFiles(t *testing.T) {
	spec, err := model.LoadInfraSpec(filepath.Join("..", "..", "infra", "mobility-fixture", "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Migrations != "" {
		t.Fatal("fixture migration command would run on the control host")
	}
	image := "registry.example.test/norn/mobility:test"
	service := mustTranslate(t, spec, image, nil)
	if len(service.TaskGroups) != 1 {
		t.Fatalf("service task groups = %d", len(service.TaskGroups))
	}
	assertTask := func(name, mode string, jobTaskGroups int) {
		t.Helper()
		var taskFound bool
		var groups = service.TaskGroups
		if name != "web" {
			job := mustTranslatePeriodic(t, spec, name, spec.Processes[name], image, nil)
			groups = job.TaskGroups
		}
		if len(groups) != jobTaskGroups {
			t.Fatalf("%s task groups = %d", name, len(groups))
		}
		for _, group := range groups {
			for _, task := range group.Tasks {
				if task.Name != name {
					continue
				}
				taskFound = true
				if _, ok := task.Config["command"]; ok || task.Env["MOBILITY_MODE"] != mode || len(task.VolumeMounts) != 1 || *task.VolumeMounts[0].Destination != "/data" {
					t.Fatalf("%s does not use shell-free entrypoint with its persistent file mount: %+v", name, task)
				}
			}
		}
		if !taskFound {
			t.Fatalf("%s task missing", name)
		}
	}
	assertTask("web", "serve", 1)
	assertTask("worker", "worker", 1)
	assertTask("tick", "tick", 1)
}
