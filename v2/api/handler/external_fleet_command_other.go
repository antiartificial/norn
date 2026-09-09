//go:build !unix

package handler

import "os/exec"

// Windows and other non-Unix targets have no portable process-group primitive
// in os/exec. CommandContext still cancels the direct verifier process and
// WaitDelay bounds inherited pipes.
func externalFleetConfigureCommandCancellation(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return command.Process.Kill()
	}
}
