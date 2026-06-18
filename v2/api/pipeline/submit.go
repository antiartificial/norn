package pipeline

import (
	"context"
	"fmt"
	"os"

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
			if used, err := p.Engine.UsedPorts(); err == nil {
				for _, pa := range used {
					if pa.Port == proc.Port && pa.App != st.spec.App {
						suggested, _ := p.Engine.SuggestPort(proc.Port)
						sg.Log(ctx, "port.conflict",
							fmt.Sprintf("port %d is used by %s — suggest %d", proc.Port, pa.App, suggested),
							map[string]string{"step": "submit"})
					}
				}
			}
			break
		}
	}

	if err := p.Engine.SubmitJob(ctx, st.spec, st.imageTag, env); err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	sg.Log(ctx, "engine.submitted", fmt.Sprintf("job submitted for %s", st.spec.App), map[string]string{
		"step": "submit",
	})

	// Register cron jobs for scheduled processes
	for procName, proc := range st.spec.Processes {
		if proc.Schedule == "" {
			continue
		}
		if err := p.Engine.RegisterCron(st.spec, procName, proc, st.imageTag, env); err != nil {
			return fmt.Errorf("register cron %s: %w", procName, err)
		}
		sg.Log(ctx, "engine.cron_registered", fmt.Sprintf("cron job %s registered", procName), map[string]string{
			"step": "submit",
		})
	}

	return nil
}
