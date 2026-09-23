//go:build darwin

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

const exitObservationSupported = true

// waitExitWithoutReaping uses a kqueue NOTE_EXIT watch, which reports exit
// without reaping, so the PID and process group ID stay reserved until the
// caller reaps. ESRCH at registration means the unreaped child already exited.
func waitExitWithoutReaping(pid int) error {
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(queue)
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	for {
		_, err := unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		break
	}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(queue, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 1 && events[0].Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}

func placeInCgroup(*exec.Cmd, *os.File) error {
	return fmt.Errorf("command cgroups are supported only on Linux")
}
