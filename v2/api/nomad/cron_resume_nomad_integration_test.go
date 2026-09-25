package nomad

import (
	"errors"
	"os"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// Run only against a disposable Nomad agent. The test owns and purges its job.
func TestPeriodicPauseResumeCASInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to a disposable Nomad agent")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-cron-cas-qual-" + time.Now().UTC().Format("20060102150405")
	name, kind, group, task, driver, schedule, timezone, command := id, "batch", "work", "noop", "raw_exec", "0 0 * * *", "UTC", "/bin/true"
	count := 1
	job := &nomadapi.Job{ID: &id, Name: &name, Type: &kind, Datacenters: []string{"dc1"},
		Periodic: &nomadapi.PeriodicConfig{Spec: &schedule, TimeZone: &timezone},
		TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count,
			Tasks: []*nomadapi.Task{{Name: task, Driver: driver, Config: map[string]interface{}{"command": command}}}}},
	}
	t.Cleanup(func() { _, _, _ = client.api.Jobs().Deregister(id, true, nil) })
	if _, _, err := client.api.Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	initial, err := client.PeriodicJobSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PausePeriodicJob(id, initial.ModifyIndex, "pause-effect"); err != nil {
		t.Fatal(err)
	}
	paused, err := client.PeriodicJobSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.Paused || paused.CronPauseEffectID != "pause-effect" {
		t.Fatalf("pause not observed: %+v", paused)
	}
	if err := client.ResumePeriodicJob(id, initial.ModifyIndex, "stale-effect"); !errors.Is(err, ErrJobRevisionChanged) {
		t.Fatalf("stale resume = %v, want revision conflict", err)
	}
	if err := client.ResumePeriodicJob(id, paused.ModifyIndex, "resume-effect"); err != nil {
		t.Fatal(err)
	}
	resumed, err := client.PeriodicJobSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Paused || resumed.CronResumeEffectID != "resume-effect" || resumed.Schedule != schedule {
		t.Fatalf("resume not observed: %+v", resumed)
	}
}
