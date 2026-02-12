package runtime

import (
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"time"
)

const maxOutputBytes = 64 * 1024 // 64KB

type DockerRunner struct{}

func NewDockerRunner() *DockerRunner {
	return &DockerRunner{}
}

func (d *DockerRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := fmt.Sprintf("norn-cron-%s", randomSuffix())

	args := []string{
		"run", "--rm", "--name", name,
		"--memory=512m", "--cpus=1", "--pids-limit=256",
		"--network=none",
	}
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, opts.Image)
	args = append(args, opts.Command...)

	start := time.Now()
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	duration := time.Since(start)

	output := string(out)
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... (output truncated at 64KB)"
	}

	result := &RunResult{
		Output:   output,
		Duration: duration,
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// Kill the container on timeout
			exec.Command("docker", "kill", name).Run()
			result.ExitCode = -1
			return result, fmt.Errorf("execution timed out after %s", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil // non-zero exit is not a runner error
		}
		return result, err
	}

	result.ExitCode = 0
	return result, nil
}

func (d *DockerRunner) ImageExists(ctx context.Context, image string) (bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func randomSuffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
