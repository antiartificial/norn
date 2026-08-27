package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestContainerListEntryParsesAppleOnePointZeroJSON(t *testing.T) {
	raw := []byte(`{
		"id":"norn-sample-web-0",
		"configuration":{"id":"norn-sample-web-0","image":{"reference":"sample:dev"},"creationDate":"2026-08-26T01:02:03Z"},
		"status":{"state":"running","startedDate":"2026-08-26T01:03:04Z","networks":[{"ipv4Address":"192.168.64.8/24"}]}
	}`)
	var entry containerListEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Name != "norn-sample-web-0" || entry.Image != "sample:dev" || entry.Status != "running" || entry.IP != "192.168.64.8" || entry.Created != "2026-08-26T01:03:04Z" {
		t.Fatalf("parsed entry = %#v", entry)
	}
}

func TestContainerRunKeepsEnvironmentOffCommandLine(t *testing.T) {
	original := runContainerCommand
	t.Cleanup(func() { runContainerCommand = original })
	var captured []string
	runContainerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		captured = append([]string(nil), args...)
		index := slices.Index(args, "--env-file")
		if index < 0 || index+1 >= len(args) {
			t.Fatalf("missing --env-file in %v", args)
		}
		info, err := os.Stat(args[index+1])
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("environment file mode = %o", info.Mode().Perm())
		}
		data, err := os.ReadFile(args[index+1])
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "API_TOKEN=super-secret\nPUBLIC=value\n" {
			t.Fatalf("environment = %q", data)
		}
		return nil, nil
	}

	err := containerRun(context.Background(), RunOpts{
		Name: "norn-example-web-0", Image: "example:dev", Port: 8080, HostPort: 18080,
		Env: map[string]string{"PUBLIC": "value", "API_TOKEN": "super-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	if strings.Contains(joined, "super-secret") {
		t.Fatalf("secret leaked into command arguments: %s", joined)
	}
	if !strings.Contains(joined, "--publish 127.0.0.1:18080:8080") {
		t.Fatalf("loopback port publication missing: %s", joined)
	}
}

func TestContainerRunRaisesMemoryToAppleMinimum(t *testing.T) {
	original := runContainerCommand
	t.Cleanup(func() { runContainerCommand = original })
	var captured []string
	runContainerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		captured = append([]string(nil), args...)
		return nil, nil
	}

	if err := containerRun(context.Background(), RunOpts{
		Name: "norn-example-worker-0", Image: "example:dev", MemoryMB: 64,
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--memory 200MB") {
		t.Fatalf("Apple memory floor missing: %s", joined)
	}
}

func TestRestartJobStopsIndependentContainersConcurrently(t *testing.T) {
	original := runContainerCommand
	t.Cleanup(func() { runContainerCommand = original })
	stopEntered := make(chan string, 2)
	releaseStops := make(chan struct{})
	starts := make(chan string, 2)
	runContainerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "stop":
			stopEntered <- args[len(args)-1]
			<-releaseStops
		case "start":
			starts <- args[len(args)-1]
		}
		return nil, nil
	}

	eng := &Engine{instances: map[string]*Instance{
		"norn-sample-worker-0": {ContainerName: "norn-sample-worker-0", App: "sample", Kind: "service", Status: "running"},
		"norn-sample-worker-1": {ContainerName: "norn-sample-worker-1", App: "sample", Kind: "service", Status: "running"},
	}}
	done := make(chan error, 1)
	go func() { done <- eng.RestartJob(context.Background(), "sample") }()

	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-stopEntered:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("restart serialized container stops")
		}
	}
	close(releaseStops)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || len(starts) != 2 {
		t.Fatalf("restart stops=%v starts=%d", seen, len(starts))
	}
}

func TestEnsureRuntimeStartsStoppedSystem(t *testing.T) {
	original := runContainerCommand
	t.Cleanup(func() { runContainerCommand = original })
	statusCalls := 0
	startCalls := 0
	runContainerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "system" && args[1] == "start" {
			startCalls++
			return nil, nil
		}
		if len(args) >= 2 && args[0] == "system" && args[1] == "status" {
			statusCalls++
			if statusCalls > 1 {
				return []byte(`{"status":"running"}`), nil
			}
			return []byte(`{"status":"not running"}`), nil
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := EnsureRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if startCalls != 1 {
		t.Fatalf("system start calls = %d", startCalls)
	}
}
