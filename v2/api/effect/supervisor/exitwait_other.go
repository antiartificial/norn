//go:build !linux && !darwin

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
)

// Without a non-reaping exit observation the timeout guard cannot be closed
// before reaping, so the runner refuses to execute.
const exitObservationSupported = false

func waitExitWithoutReaping(int) error {
	return fmt.Errorf("non-reaping exit observation is unsupported on this platform")
}

func placeInCgroup(*exec.Cmd, *os.File) error {
	return fmt.Errorf("command cgroups are supported only on Linux")
}
