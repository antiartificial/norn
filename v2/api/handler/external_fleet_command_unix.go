//go:build unix

package handler

import (
	"os/exec"
	"syscall"
)

// A dedicated group prevents a timed-out gh helper from retaining children
// after its parent exits. Direct exec means no shell-created intermediary.
func externalFleetConfigureCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
