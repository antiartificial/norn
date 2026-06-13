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

	if st.spec.Infrastructure != nil && st.spec.Infrastructure.ObjectStorage != nil {
		if p.Storage == nil {
			return fmt.Errorf("object storage declared but NORN_S3_ENDPOINT is not configured")
		}
		storageEnv, err := p.Storage.ProvisionAppStorage(ctx, st.spec.App, st.spec.Infrastructure.ObjectStorage, env)
		if err != nil {
			return fmt.Errorf("provision object storage: %w", err)
		}
		if len(storageEnv.Secrets) > 0 && p.Secrets != nil {
			if err := p.Secrets.Set(st.spec.App, storageEnv.Secrets); err != nil {
				_ = sg.Log(ctx, "object_storage.secrets", fmt.Sprintf("object storage secrets were generated but not persisted: %v", err), map[string]string{
					"step": "submit",
				})
			}
		}
		for k, v := range storageEnv.Env {
			env[k] = v
		}
		for _, bucket := range storageEnv.Buckets {
			_ = sg.Log(ctx, "object_storage.bucket", fmt.Sprintf("object storage bucket ready: %s", bucket.Name), map[string]string{
				"step":     "submit",
				"provider": bucket.Provider,
				"bucket":   bucket.Name,
				"access":   bucket.Access,
				"mode":     bucket.Mode,
			})
		}
	}

	if st.spec.Infrastructure != nil && st.spec.Infrastructure.Kafka != nil {
		if p.Redpanda == nil {
			return fmt.Errorf("kafka declared but NORN_REDPANDA_BROKERS is not configured")
		}
		kafkaEnv, err := p.Redpanda.ProvisionAppKafka(ctx, st.spec.App, st.spec.Infrastructure.Kafka)
		if err != nil {
			return fmt.Errorf("provision kafka topics: %w", err)
		}
		for k, v := range kafkaEnv.Env {
			env[k] = v
		}
		for _, topic := range kafkaEnv.Topics {
			_ = sg.Log(ctx, "kafka.topic", fmt.Sprintf("kafka topic ready: %s", topic.Name), map[string]string{
				"step":  "submit",
				"topic": topic.Name,
			})
		}
	}

	// Check for port conflicts before submitting
	for _, proc := range st.spec.Processes {
		if proc.Port > 0 && len(st.spec.Endpoints) > 0 {
			if used, err := p.Nomad.UsedPorts(); err == nil {
				for _, pa := range used {
					if pa.Port == proc.Port && pa.JobID != st.spec.App {
						suggested, _ := p.Nomad.SuggestPort(proc.Port)
						sg.Log(ctx, "port.conflict",
							fmt.Sprintf("port %d is used by %s — suggest %d", proc.Port, pa.JobID, suggested),
							map[string]string{"step": "submit"})
					}
				}
			}
			break
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
