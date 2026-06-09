package dispatch

import (
	"context"

	"norn/api/runtime"
)

type DispatchRunner struct {
	registry   *Registry
	dispatcher *Dispatcher
	fallback   runtime.Runner
}

func NewDispatchRunner(registry *Registry, dispatcher *Dispatcher, fallback runtime.Runner) *DispatchRunner {
	return &DispatchRunner{
		registry:   registry,
		dispatcher: dispatcher,
		fallback:   fallback,
	}
}

func (r *DispatchRunner) Run(ctx context.Context, opts runtime.RunOpts) (*runtime.RunResult, error) {
	return r.fallback.Run(ctx, opts)
}

func (r *DispatchRunner) ImageExists(ctx context.Context, image string) (bool, error) {
	return r.fallback.ImageExists(ctx, image)
}
