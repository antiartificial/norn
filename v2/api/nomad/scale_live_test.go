package nomad

import (
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
)

// This opt-in test checks the real Nomad scaling-event contract used to
// reconcile a claimed app.scale effect. It creates and purges only its own
// disposable job on an explicitly selected loopback Nomad agent.
func TestScaleEventIdentityAgainstDisposableNomad(t *testing.T) {
	addr := os.Getenv("NORN_TEST_DISPOSABLE_NOMAD_ADDR")
	if addr == "" {
		t.Skip("NORN_TEST_DISPOSABLE_NOMAD_ADDR is not set")
	}
	parsed, err := url.Parse(addr)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("disposable Nomad address must be a loopback HTTP URL")
	}
	client, err := NewClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "norn-scale-probe-" + uuid.NewString()
	job := nomadapi.NewServiceJob(jobID, jobID, "global", 50).AddDatacenter("dc1")
	job.AddTaskGroup(nomadapi.NewTaskGroup("web", 1).AddTask(
		nomadapi.NewTask("sleep", "raw_exec").SetConfig("command", "/bin/sleep").SetConfig("args", []string{"120"}),
	))
	if _, err := client.SubmitJob(job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.StopJob(jobID, true); err != nil {
			t.Errorf("purge disposable Nomad job: %v", err)
		}
	})

	operationID := uuid.NewString()
	executionID := "nomad-scale-" + uuid.NewString()
	evalID, err := client.ScaleJobWithMeta(jobID, "web", "global", 2, map[string]interface{}{
		"norn.operationId": operationID, "norn.claimGeneration": "1", "norn.executionId": executionID,
	})
	if err != nil || evalID == "" {
		t.Fatalf("scale evaluation = %q, %v", evalID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		desired, matched, actualEvalID, err := client.ScaleStatus(jobID, "web", "global", operationID, "1", executionID, 2, evalID)
		if err == nil && desired == 2 && matched && actualEvalID == evalID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exact scale event not observed: desired=%d matched=%t eval=%q err=%v", desired, matched, actualEvalID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, matched, _, err := client.ScaleStatus(jobID, "web", "global", uuid.NewString(), "1", executionID, 2, evalID); err != nil || matched {
		t.Fatalf("foreign operation matched scale event: matched=%t err=%v", matched, err)
	}
}
