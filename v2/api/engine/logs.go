package engine

import (
	"fmt"
	"io"
	"os/exec"
)

// StreamLogs returns an io.ReadCloser that streams stdout+stderr from a
// running container. If follow is true, the stream stays open (like tail -f).
func (e *Engine) StreamLogs(appID string, follow bool) (io.ReadCloser, error) {
	containerName, err := e.FindRunningInstance(appID, "")
	if err != nil {
		return nil, err
	}
	return streamContainerLogs(containerName, follow)
}

// StreamProcessLogs streams logs for a specific process of an app.
func (e *Engine) StreamProcessLogs(appID, process string, follow bool) (io.ReadCloser, error) {
	containerName, err := e.FindRunningInstance(appID, process)
	if err != nil {
		return nil, err
	}
	return streamContainerLogs(containerName, follow)
}

func streamContainerLogs(containerName string, follow bool) (io.ReadCloser, error) {
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, containerName)

	cmd := exec.Command(containerBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("logs stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("logs stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("logs start: %w", err)
	}

	merged := io.MultiReader(stdout, stderr)
	return &cmdReadCloser{cmd: cmd, reader: merged}, nil
}

type cmdReadCloser struct {
	cmd    *exec.Cmd
	reader io.Reader
}

func (c *cmdReadCloser) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *cmdReadCloser) Close() error {
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

// FindRunningInstance finds the first running instance for an app, optionally
// filtered by process name. Returns the container name.
func (e *Engine) FindRunningInstance(appID, process string) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, inst := range e.instances {
		if inst.App != appID || !inst.IsRunning() {
			continue
		}
		if inst.Kind == "cron" || inst.Kind == "batch" {
			continue
		}
		if process != "" && inst.Process != process {
			continue
		}
		return inst.ContainerName, nil
	}
	return "", fmt.Errorf("no running instance for %s", appID)
}
