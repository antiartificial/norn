package supervisor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"norn/v2/api/effect"
)

// commandTerminator ends a supervised command on timeout. terminate must act
// only on an identity that cannot be reused while the terminator is open;
// runContained guarantees terminate is never called after close begins.
type commandTerminator interface {
	configure(*exec.Cmd) error
	started(pid int)
	terminate() error
	close() error
}

// commandTreeAwaiter is used by migration commands whose shell leader can
// exit while descendants continue writing to the database. The timeout guard
// remains armed until the dedicated command cgroup is empty.
type commandTreeAwaiter interface {
	awaitEmpty(time.Time) error
}

// processGroupTerminator kills the command's process group. A process group
// ID cannot be reused while its leader is unreaped, and runContained reaps the
// leader only after the timeout guard is closed, so the numeric group ID is
// valid for every terminate call. Killing the group is not proof that every
// descendant stopped: descendants may leave the group, which is why terminal
// acceptance still requires an empty execution cgroup.
type processGroupTerminator struct{ pid int }

func (t *processGroupTerminator) configure(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func (t *processGroupTerminator) started(pid int) { t.pid = pid }

func (t *processGroupTerminator) terminate() error {
	if t.pid <= 0 {
		return fmt.Errorf("process group is not started")
	}
	return syscall.Kill(-t.pid, syscall.SIGKILL)
}

func (t *processGroupTerminator) close() error { return nil }

// cgroupTerminator kills a dedicated command cgroup through a directory
// descriptor opened before the command starts. The kill is bound to that
// directory object, never to a path or numeric ID, and covers descendants
// that left the process group.
type cgroupTerminator struct {
	directory *os.File
}

type migrationCgroupTerminator struct{ *cgroupTerminator }

func openCgroupTerminator(path string) (*cgroupTerminator, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("command cgroup path must be absolute")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open command cgroup: %w", err)
	}
	return &cgroupTerminator{directory: os.NewFile(uintptr(fd), path)}, nil
}

func (t *cgroupTerminator) configure(command *exec.Cmd) error {
	return placeInCgroup(command, t.directory)
}

func (t *cgroupTerminator) started(int) {}

func (t *cgroupTerminator) terminate() error {
	fd, err := unix.Openat(int(t.directory.Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open command cgroup.kill: %w", err)
	}
	file := os.NewFile(uintptr(fd), "cgroup.kill")
	defer file.Close()
	_, err = file.Write([]byte("1\n"))
	return err
}

func (t *cgroupTerminator) close() error { return t.directory.Close() }

func (t *migrationCgroupTerminator) awaitEmpty(deadline time.Time) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		fd, err := unix.Openat(int(t.directory.Fd()), "cgroup.events", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open migration command cgroup.events: %w", err)
		}
		file := os.NewFile(uintptr(fd), "cgroup.events")
		data, readErr := io.ReadAll(io.LimitReader(file, maxCgroupEventsBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > maxCgroupEventsBytes {
			return fmt.Errorf("read migration command cgroup.events")
		}
		populated, err := parseCgroupEvents(data)
		if err != nil {
			return err
		}
		if !populated {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("migration command descendants did not stop after timeout")
		}
		<-ticker.C
	}
}

// timeoutGuard owns the only path to terminate. fire and close serialize on
// one mutex and terminate runs while it is held, so close returns only after
// any in-flight terminate completes, and no callback acts afterwards.
type timeoutGuard struct {
	mu        sync.Mutex
	closed    bool
	fired     bool
	err       error
	stop      func() bool
	terminate func() error
}

// scheduleFunc matches time.AfterFunc; tests inject a manual scheduler.
type scheduleFunc func(time.Duration, func()) func() bool

func realSchedule(delay time.Duration, callback func()) func() bool {
	return time.AfterFunc(delay, callback).Stop
}

func startTimeoutGuard(delay time.Duration, terminate func() error, schedule scheduleFunc) *timeoutGuard {
	guard := &timeoutGuard{terminate: terminate}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.stop = schedule(delay, guard.fire)
	return guard
}

func (g *timeoutGuard) fire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.fired {
		return
	}
	g.fired = true
	g.err = g.terminate()
}

// close disarms the guard. After it returns, terminate has either completed
// or will never run.
func (g *timeoutGuard) close() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.stop != nil {
		g.stop()
	}
	return g.fired, g.err
}

// lifecycleTrace records runContained ordering for deterministic tests.
type lifecycleTrace func(string)

// runContained starts a timeout guard after launch. Process-group commands
// disarm it before reaping the leader to prevent PID reuse. Migration cgroup
// commands keep it armed until the entire command cgroup is empty.
func runContained(material effect.LaunchMaterial, output io.Writer, terminator commandTerminator, schedule scheduleFunc, trace lifecycleTrace) (bool, error) {
	if trace == nil {
		trace = func(string) {}
	}
	defer terminator.close()
	if !exitObservationSupported {
		return false, fmt.Errorf("effect runner cannot observe command exit without reaping on this platform")
	}
	command := exec.Command(material.Argv[0], material.Argv[1:]...)
	command.Dir = material.Directory
	command.Env = append([]string(nil), material.Environment...)
	command.Stdin = nil
	command.Stdout = output
	command.Stderr = output
	command.WaitDelay = outputDrainDelay
	if err := terminator.configure(command); err != nil {
		return false, err
	}
	if err := command.Start(); err != nil {
		return false, err
	}
	startedAt := time.Now()
	trace("started")
	terminator.started(command.Process.Pid)
	guard := startTimeoutGuard(material.Timeout, terminator.terminate, schedule)
	exitErr := waitExitWithoutReaping(command.Process.Pid)
	trace("exited")
	if awaiter, ok := terminator.(commandTreeAwaiter); ok {
		// The cgroup directory descriptor remains stable after reaping the
		// leader, so the timeout can still kill descendants without a PID race.
		err := command.Wait()
		trace("reaped")
		containedErr := awaiter.awaitEmpty(startedAt.Add(material.Timeout + 5*time.Second))
		timedOut, terminateErr := guard.close()
		trace("guard-closed")
		if exitErr != nil && err == nil {
			err = fmt.Errorf("observe command exit: %w", exitErr)
		}
		if containedErr != nil {
			return timedOut, containedErr
		}
		if terminateErr != nil {
			return timedOut, terminateErr
		}
		return timedOut, err
	}
	timedOut, _ := guard.close()
	trace("guard-closed")
	err := command.Wait()
	trace("reaped")
	if exitErr != nil && err == nil {
		err = fmt.Errorf("observe command exit: %w", exitErr)
	}
	return timedOut, err
}
