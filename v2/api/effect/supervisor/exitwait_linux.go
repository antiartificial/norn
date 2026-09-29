//go:build linux

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

const exitObservationSupported = true

// waitExitWithoutReaping blocks until pid exits, leaving it a zombie so its
// PID and process group ID stay reserved until the caller reaps it.
func waitExitWithoutReaping(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}

func placeInCgroup(command *exec.Cmd, directory *os.File) error {
	attributes := command.SysProcAttr
	if attributes == nil {
		attributes = &syscall.SysProcAttr{}
	}
	attributes.Setpgid = true
	attributes.UseCgroupFD = true
	attributes.CgroupFD = int(directory.Fd())
	command.SysProcAttr = attributes
	return nil
}
