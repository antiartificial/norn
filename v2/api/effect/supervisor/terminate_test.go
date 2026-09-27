package supervisor

import (
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norn/v2/api/effect"
)

// manualSchedule captures the timeout callback so a test decides exactly when
// it fires relative to close.
type manualSchedule struct {
	mu       sync.Mutex
	callback func()
	stopped  bool
}

func (m *manualSchedule) schedule(_ time.Duration, callback func()) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callback = callback
	return func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.stopped = true
		return true
	}
}

func (m *manualSchedule) fire() {
	m.mu.Lock()
	callback := m.callback
	m.mu.Unlock()
	callback()
}

func TestTimeoutGuardFireBeforeCloseTerminatesOnce(t *testing.T) {
	var calls atomic.Int32
	manual := &manualSchedule{}
	guard := startTimeoutGuard(time.Minute, func() error { calls.Add(1); return nil }, manual.schedule)
	manual.fire()
	manual.fire()
	fired, err := guard.close()
	if !fired || err != nil || calls.Load() != 1 || !manual.stopped {
		t.Fatalf("fired=%v err=%v calls=%d stopped=%v", fired, err, calls.Load(), manual.stopped)
	}
	manual.fire()
	if calls.Load() != 1 {
		t.Fatal("callback terminated after close")
	}
}

func TestTimeoutGuardCallbackAfterCloseNeverTerminates(t *testing.T) {
	var calls atomic.Int32
	manual := &manualSchedule{}
	guard := startTimeoutGuard(time.Minute, func() error { calls.Add(1); return nil }, manual.schedule)
	if fired, _ := guard.close(); fired {
		t.Fatal("unfired guard reported a timeout")
	}
	// A timer callback already dispatched when close ran must be inert.
	manual.fire()
	if calls.Load() != 0 {
		t.Fatalf("late callback terminated %d times", calls.Load())
	}
}

func TestTimeoutGuardCloseJoinsInFlightTerminate(t *testing.T) {
	manual := &manualSchedule{}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	guard := startTimeoutGuard(time.Minute, func() error {
		calls.Add(1)
		close(entered)
		<-release
		return errors.New("kill reported")
	}, manual.schedule)
	go manual.fire()
	<-entered
	closed := make(chan struct{})
	var fired bool
	var err error
	go func() { fired, err = guard.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned while terminate was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-closed
	if !fired || err == nil || calls.Load() != 1 {
		t.Fatalf("fired=%v err=%v calls=%d", fired, err, calls.Load())
	}
}

func TestTimeoutGuardRealTimerIsStoppedByClose(t *testing.T) {
	var calls atomic.Int32
	guard := startTimeoutGuard(20*time.Millisecond, func() error { calls.Add(1); return nil }, realSchedule)
	guard.close()
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("stopped timer still terminated")
	}
}

// tracingTerminator records terminate calls against the lifecycle trace.
type tracingTerminator struct {
	inner      *processGroupTerminator
	mu         sync.Mutex
	events     []string
	terminates []int
	closed     bool
}

func (t *tracingTerminator) record(event string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *tracingTerminator) configure(command *exec.Cmd) error { return t.inner.configure(command) }
func (t *tracingTerminator) started(pid int)                   { t.inner.started(pid) }
func (t *tracingTerminator) terminate() error {
	t.mu.Lock()
	t.terminates = append(t.terminates, len(t.events))
	t.events = append(t.events, "terminate")
	t.mu.Unlock()
	return t.inner.terminate()
}
func (t *tracingTerminator) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.events = append(t.events, "terminator-closed")
	return nil
}

// runTraced runs a real command through runContained with a tracing
// process-group terminator.
func runTraced(t *testing.T, command string, timeout time.Duration) (*tracingTerminator, bool, error) {
	t.Helper()
	tracing := &tracingTerminator{inner: &processGroupTerminator{}}
	material := effect.LaunchMaterial{Argv: []string{"sh", "-c", command}, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"}, Subject: "test", Timeout: timeout}
	file, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	capture := &boundedCapture{file: file, hash: sha256.New(), limit: MaxResultBytes}
	timedOut, runErr := runContained(material, capture, tracing, realSchedule, tracing.record)
	return tracing, timedOut, runErr
}

func requireOrder(t *testing.T, events []string, want ...string) {
	t.Helper()
	index := 0
	for _, event := range events {
		if index < len(want) && event == want[index] {
			index++
		}
	}
	if index != len(want) {
		t.Fatalf("lifecycle %v does not contain ordered %v", events, want)
	}
}

func TestRunContainedClosesGuardBeforeReapingOnNormalExit(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		tracing, timedOut, err := runTraced(t, "exit 0", time.Minute)
		if err != nil || timedOut {
			t.Fatalf("normal exit err=%v timedOut=%v", err, timedOut)
		}
		requireOrder(t, tracing.events, "started", "exited", "guard-closed", "reaped", "terminator-closed")
		if len(tracing.terminates) != 0 {
			t.Fatalf("normal exit terminated: %v", tracing.events)
		}
	}
}

func TestRunContainedTimeoutTerminatesOnlyBeforeGuardCloses(t *testing.T) {
	tracing, timedOut, err := runTraced(t, "sleep 30", 100*time.Millisecond)
	if !timedOut || err == nil {
		t.Fatalf("timeout timedOut=%v err=%v", timedOut, err)
	}
	requireOrder(t, tracing.events, "started", "terminate", "exited", "guard-closed", "reaped", "terminator-closed")
	if len(tracing.terminates) != 1 {
		t.Fatalf("terminate calls = %v", tracing.events)
	}
}

func TestRunContainedNeverTerminatesAfterGuardCloses(t *testing.T) {
	// Commands finishing right at the deadline race the timer; whichever wins,
	// no terminate may follow the guard closing (the leader is unreaped until
	// then, so the process-group ID is still reserved when a kill is sent).
	for iteration := 0; iteration < 30; iteration++ {
		tracing, _, _ := runTraced(t, "sleep 0.02", 20*time.Millisecond)
		guardClosed := -1
		for index, event := range tracing.events {
			if event == "guard-closed" {
				guardClosed = index
			}
		}
		if guardClosed < 0 {
			t.Fatalf("guard never closed: %v", tracing.events)
		}
		for _, at := range tracing.terminates {
			if at > guardClosed {
				t.Fatalf("terminate after guard closed: %v", tracing.events)
			}
		}
		requireOrder(t, tracing.events, "exited", "guard-closed", "reaped")
	}
}

func TestProcessGroupTerminatorRequiresStartedProcess(t *testing.T) {
	if err := (&processGroupTerminator{}).terminate(); err == nil {
		t.Fatal("terminate without a started process group was accepted")
	}
}

func TestCgroupTerminatorKillsTheOpenedDirectoryNotThePath(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "command")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{original} {
		if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	terminator, err := openCgroupTerminator(original)
	if err != nil {
		t.Fatal(err)
	}
	defer terminator.close()
	// Replace the path with a different directory; the kill must still reach
	// the directory object opened before the command started.
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "cgroup.kill"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := terminator.terminate(); err != nil {
		t.Fatal(err)
	}
	killed, _ := os.ReadFile(filepath.Join(moved, "cgroup.kill"))
	replacement, _ := os.ReadFile(filepath.Join(original, "cgroup.kill"))
	if string(killed) != "1\n" || len(replacement) != 0 {
		t.Fatalf("kill reached opened=%q replacement=%q", killed, replacement)
	}
	if _, err := openCgroupTerminator("relative/command"); err == nil {
		t.Fatal("relative command cgroup accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(moved, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openCgroupTerminator(link); err == nil {
		t.Fatal("symlinked command cgroup accepted")
	}
	if runtime.GOOS != "linux" {
		if err := terminator.configure(exec.Command("true")); err == nil {
			t.Fatal("command cgroup placement claimed support off Linux")
		}
	}
}

func TestRunHelperWithholdsTerminalStatusUntilResultIsDurable(t *testing.T) {
	directory := t.TempDir()
	request, key := helperRequest(t, directory, "printf durable", time.Minute)
	original := syncResult
	t.Cleanup(func() { syncResult = original })
	var observedAtSync runnerStatus
	syncResult = func(file *os.File) error {
		status, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
		if err != nil {
			return err
		}
		observedAtSync = status
		return file.Sync()
	}
	if err := runHelperRequest(t, request); err != nil {
		t.Fatal(err)
	}
	if observedAtSync.Phase != effect.SupervisorRunning {
		t.Fatalf("terminal status published before result sync: %+v", observedAtSync)
	}
	final, err := readRunnerStatus(directory, key, request.Execution.RuntimeInstanceID)
	if err != nil || final.Phase != effect.SupervisorSucceeded {
		t.Fatalf("final status = %+v, %v", final, err)
	}

	failed := t.TempDir()
	request, key = helperRequest(t, failed, "printf lost", time.Minute)
	syncResult = func(*os.File) error { return errors.New("fsync failed") }
	if err := runHelperRequest(t, request); err == nil || !strings.Contains(err.Error(), "durably store") {
		t.Fatalf("sync failure = %v", err)
	}
	status, err := readRunnerStatus(failed, key, request.Execution.RuntimeInstanceID)
	if err != nil || status.Phase != effect.SupervisorRunning {
		t.Fatalf("sync failure published %+v, %v", status, err)
	}
}

func TestRunHelperRejectsRelativeCommandCgroup(t *testing.T) {
	directory := t.TempDir()
	request, _ := helperRequest(t, directory, "true", time.Minute)
	request.CommandCgroup = "relative/command"
	if err := runHelperRequest(t, request); err == nil {
		t.Fatal("relative command cgroup accepted")
	}
}
