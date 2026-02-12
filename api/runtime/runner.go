package runtime

import (
	"context"
	"time"
)

type RunResult struct {
	ExitCode int
	Output   string
	Duration time.Duration
}

type Runner interface {
	Run(ctx context.Context, opts RunOpts) (*RunResult, error)
	ImageExists(ctx context.Context, image string) (bool, error)
}

type RunOpts struct {
	Image   string
	Command []string
	Env     map[string]string
	Timeout time.Duration
}
