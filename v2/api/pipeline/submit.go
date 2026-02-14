package pipeline

import (
	"context"
	"fmt"
	"os"

	"norn/v2/api/nomad"
	"norn/v2/api/saga"
)

func (p *Pipeline) submit(ctx context.Context, st *state, sg *saga.Saga) error {
	// Resolve secrets for env injection
	env := make(map[string]string)
	if p.Secrets != nil {
		secretEnv, err := p.Secrets.EnvMap(st.spec.App)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("resolve secrets: %w", err)
		}
		for k, v := range secretEnv {
			env[k] = v
		}
	}

	// Translate infraspec → Nomad job
	job := nomad.Translate(st.spec, st.imageTag, env)

	evalID, err := p.Nomad.SubmitJob(job)
	if err != nil {
		return fmt.Errorf("submit nomad job: %w", err)
	}
	sg.Log(ctx, "nomad.submitted", fmt.Sprintf("nomad job submitted (eval: %s)", evalID), map[string]string{
		"step":   "submit",
		"evalId": evalID,
	})

	// Submit periodic jobs for scheduled processes
	for procName, proc := range st.spec.Processes {
		if proc.Schedule == "" {
			continue
		}
		periodicJob := nomad.TranslatePeriodic(st.spec, procName, proc, st.imageTag, env)
		periodicEvalID, err := p.Nomad.SubmitJob(periodicJob)
		if err != nil {
			return fmt.Errorf("submit periodic job %s: %w", procName, err)
		}
		sg.Log(ctx, "nomad.submitted", fmt.Sprintf("periodic job %s submitted (eval: %s)", procName, periodicEvalID), map[string]string{
			"step": "submit",
		})
	}

	return nil
}
