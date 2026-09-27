package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"norn/v2/api/effect"
)

// These tests exercise the cgroup-v2 backend's observation, revocation and
// result logic against a fake cgroupfs directory. They do not prove kernel
// containment; that requires a scoped Linux qualification environment.

type fakeCgroup struct {
	backend   *cgroupBackend
	execution BackendExecution
	cgroup    string
}

func newFakeCgroup(t *testing.T) *fakeCgroup {
	t.Helper()
	backend := newCgroupBackend(t.TempDir(), "/nonexistent/norn-effect-runner", testSigningKey)
	backend.pollInterval = 5 * time.Millisecond
	execution := BackendExecution{SupervisorExecutionID: "execution-cgroup", RuntimeInstanceID: "runtime-cgroup", StateDirectory: t.TempDir()}
	return &fakeCgroup{backend: backend, execution: execution, cgroup: backend.cgroupPath(execution.RuntimeInstanceID)}
}

func (f *fakeCgroup) events(t *testing.T, contents string) {
	t.Helper()
	if err := os.MkdirAll(f.cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeEventsAtomically(f.cgroup, contents); err != nil {
		t.Fatal(err)
	}
}

// writeEventsAtomically mirrors cgroupfs, where a reader never observes a
// half-written cgroup.events.
func writeEventsAtomically(cgroup, contents string) error {
	staged := filepath.Join(cgroup, ".cgroup.events.tmp")
	if err := os.WriteFile(staged, []byte(contents), 0o644); err != nil {
		return err
	}
	return os.Rename(staged, filepath.Join(cgroup, "cgroup.events"))
}

// run produces a genuine runner status/result pair with the backend's key.
func (f *fakeCgroup) run(t *testing.T, command string, timeout time.Duration) {
	t.Helper()
	request := runnerRequest{
		Protocol: ProtocolV1, Execution: f.execution,
		Material:  effect.LaunchMaterial{Argv: []string{"sh", "-c", command}, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"}, Subject: "git:" + strings.Repeat("c", 40), Timeout: timeout},
		StatusKey: hex.EncodeToString(runnerStatusKey(f.backend.key, f.execution.RuntimeInstanceID)),
	}
	encoded, _ := json.Marshal(request)
	if err := RunHelper(strings.NewReader(string(encoded))); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupEventsParserIsStrict(t *testing.T) {
	for name, test := range map[string]struct {
		contents  string
		populated bool
		valid     bool
	}{
		"empty group":      {"populated 0\nfrozen 0\n", false, true},
		"populated":        {"frozen 0\npopulated 1\n", true, true},
		"no newline":       {"populated 1", true, true},
		"missing field":    {"frozen 0\n", false, false},
		"duplicate field":  {"populated 0\npopulated 1\n", false, false},
		"duplicate same":   {"populated 0\npopulated 0\n", false, false},
		"invalid value":    {"populated 2\n", false, false},
		"malformed line":   {"populated\n", false, false},
		"three fields":     {"populated 0 extra\n", false, false},
		"empty file":       {"", false, false},
		"garbage key line": {"populated 0\nnoise\n", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeCgroup(t)
			fake.events(t, test.contents)
			populated, err := cgroupPopulated(fake.cgroup)
			if (err == nil) != test.valid || (test.valid && populated != test.populated) {
				t.Fatalf("populated=%v err=%v, want populated=%v valid=%v", populated, err, test.populated, test.valid)
			}
		})
	}
	if _, err := cgroupPopulated(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cgroup error = %v", err)
	}
}

func TestCgroupObservationIsFailClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("missing cgroup is unknown, not not-found", func(t *testing.T) {
		fake := newFakeCgroup(t)
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorUnknown || state.ContainmentProven {
			t.Fatalf("state=%+v err=%v", state, err)
		}
	})
	t.Run("populated without status is running", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.events(t, "populated 1\n")
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorRunning {
			t.Fatalf("state=%+v err=%v", state, err)
		}
	})
	t.Run("empty without status is unknown", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.events(t, "populated 0\n")
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorUnknown {
			t.Fatalf("state=%+v err=%v", state, err)
		}
	})
	t.Run("runner died after running status is unknown", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.events(t, "populated 0\n")
		if err := writeRunnerStatus(fake.execution.StateDirectory, runnerStatusKey(fake.backend.key, fake.execution.RuntimeInstanceID), runnerStatus{
			Protocol: ProtocolV1, RuntimeInstanceID: fake.execution.RuntimeInstanceID, Phase: effect.SupervisorRunning, UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorUnknown {
			t.Fatalf("state=%+v err=%v", state, err)
		}
	})
	t.Run("terminal status with live descendants is running", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.run(t, "printf done", time.Minute)
		fake.events(t, "populated 1\n")
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorRunning || state.ContainmentProven {
			t.Fatalf("state=%+v err=%v", state, err)
		}
	})
	t.Run("terminal status in empty group is contained with authenticated output", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.run(t, "printf done; exit 4", time.Minute)
		fake.events(t, "populated 0\n")
		state, err := fake.backend.Observe(ctx, fake.execution)
		if err != nil || state.Phase != effect.SupervisorFailed || !state.ContainmentProven || string(state.Output) != "done" || state.ExitCode == nil || *state.ExitCode != 4 {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		output, err := fake.backend.RetrieveResult(ctx, fake.execution, "result/"+fake.execution.SupervisorExecutionID)
		if err != nil || string(output) != "done" {
			t.Fatalf("retrieve=%q err=%v", output, err)
		}
		if _, err := fake.backend.RetrieveResult(ctx, fake.execution, "result/other"); err == nil {
			t.Fatal("result served for another reference")
		}
	})
	t.Run("replaced output after terminal status is rejected", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.run(t, "printf done", time.Minute)
		fake.events(t, "populated 0\n")
		if err := os.WriteFile(filepath.Join(fake.execution.StateDirectory, "result.bin"), []byte("evil"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fake.backend.Observe(ctx, fake.execution); err == nil {
			t.Fatal("observation accepted output that differs from the signed status")
		}
		if _, err := fake.backend.RetrieveResult(ctx, fake.execution, "result/"+fake.execution.SupervisorExecutionID); err == nil {
			t.Fatal("retrieval accepted output that differs from the signed status")
		}
	})
	t.Run("malformed events fail observation", func(t *testing.T) {
		fake := newFakeCgroup(t)
		fake.events(t, "populated 0\npopulated 0\n")
		if _, err := fake.backend.Observe(ctx, fake.execution); err == nil {
			t.Fatal("duplicate populated field accepted")
		}
	})
}

func TestCgroupRevokeWaitsForEmptyGroup(t *testing.T) {
	fake := newFakeCgroup(t)
	if state, err := fake.backend.Revoke(context.Background(), fake.execution); err != nil || state.Phase != effect.SupervisorUnknown {
		t.Fatalf("revoke of missing cgroup = %+v, %v", state, err)
	}
	fake.events(t, "populated 1\n")
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(fake.cgroup, "cgroup.kill")); err == nil {
				_ = writeEventsAtomically(fake.cgroup, "populated 0\n")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	state, err := fake.backend.Revoke(context.Background(), fake.execution)
	if err != nil || state.Phase != effect.SupervisorStopped || !state.ContainmentProven {
		t.Fatalf("revoke state=%+v err=%v", state, err)
	}
	fake.events(t, "populated 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := fake.backend.Revoke(ctx, fake.execution); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("revoke of a group that never empties = %v", err)
	}
}

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }

func TestObserveSnapshotScrubsDeadRunningHelper(t *testing.T) {
	fake := newFakeCgroup(t)
	fake.events(t, "populated 0\n")
	key := runnerStatusKey(fake.backend.key, fake.execution.RuntimeInstanceID)
	if err := writeSnapshotStatus(fake.execution.StateDirectory, key, snapshotRunnerStatus{Protocol: SnapshotProtocolV1, RuntimeInstanceID: fake.execution.RuntimeInstanceID, DescriptorSHA256: strings.Repeat("a", 64), Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(fake.execution.StateDirectory, ".snapshot-crashed")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"service.conf", "passfile"} {
		if err := os.WriteFile(filepath.Join(private, name), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state, err := fake.backend.ObserveSnapshot(context.Background(), fake.execution, SnapshotDescriptor{})
	if err != nil || state.Phase != effect.SupervisorUnknown {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	for _, name := range []string{"service.conf", "passfile"} {
		if _, err := os.Stat(filepath.Join(private, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
}
