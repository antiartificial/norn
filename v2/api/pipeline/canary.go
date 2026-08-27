package pipeline

import (
	"context"
	"fmt"
	"time"

	"norn/v2/api/saga"
)

// canary evaluates canary allocations after the healthy step passes.
// It waits for the configured evaluation period, then checks allocation health
// and either promotes or fails the Nomad deployment.
func (p *Pipeline) canary(ctx context.Context, st *state, sg *saga.Saga) error {
	spec := st.spec
	workloads := p.workloadConnector()
	if workloads == nil {
		return fmt.Errorf("workload connector is not configured")
	}

	// Find the evaluate-after duration from the first process with canary config
	evaluateAfter := 2 * time.Minute
	for _, proc := range spec.Processes {
		if proc.Canary != nil && proc.Canary.Count > 0 && proc.Canary.EvaluateAfter != "" {
			d, err := time.ParseDuration(proc.Canary.EvaluateAfter)
			if err == nil {
				evaluateAfter = d
			}
			break
		}
	}

	sg.Log(ctx, "canary.evaluating", fmt.Sprintf("evaluating canary for %s (waiting %s)", spec.App, evaluateAfter), map[string]string{
		"evaluateAfter": evaluateAfter.String(),
	})

	// Wait for the evaluation period or context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(evaluateAfter):
	}

	for _, region := range spec.ResolvedRegions() {
		if regionalServiceProcessCount(spec, region.Name) == 0 {
			continue
		}
		allocs, err := workloads.Poll(ctx, spec.App, region)
		if err != nil {
			_ = workloads.Fail(ctx, spec.App, region)
			return fmt.Errorf("canary poll allocations in %s: %w", region.Name, err)
		}
		for _, alloc := range allocs {
			if alloc.Healthy == nil || !*alloc.Healthy {
				_ = workloads.Fail(ctx, spec.App, region)
				return fmt.Errorf("canary allocation %s unhealthy in %s (status: %s)", alloc.ID, region.Name, alloc.Status)
			}
		}
		if err := workloads.Promote(ctx, spec.App, region); err != nil {
			return fmt.Errorf("canary promote in %s: %w", region.Name, err)
		}
	}

	sg.Log(ctx, "canary.promoted", fmt.Sprintf("canary promoted for %s", spec.App), nil)
	return nil
}
