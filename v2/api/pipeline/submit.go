package pipeline

import (
	"context"
	"fmt"
	"os"

	"norn/v2/api/model"
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

	for _, region := range st.spec.ResolvedRegions() {
		if regionalServiceProcessCount(st.spec, region.Name) > 0 {
			job := nomad.TranslateForRegion(st.spec, st.imageTag, env, region)
			evalID, err := p.Nomad.SubmitJobRegion(job, region.NomadRegion)
			if err != nil {
				_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusFailed, "", err.Error(), 0)
				return fmt.Errorf("submit nomad job in region %s: %w", region.Name, err)
			}
			st.regionEvals[region.Name] = evalID
			_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusSubmitting, evalID, "", 0)
			sg.Log(ctx, "nomad.submitted", fmt.Sprintf("nomad job submitted in %s (eval: %s)", region.Name, evalID), map[string]string{
				"step": "submit", "evalId": evalID, "region": region.Name,
			})
		}

		for procName, proc := range st.spec.Processes {
			if proc.Schedule == "" || !st.spec.ProcessRunsInRegion(proc, region.Name) {
				continue
			}
			periodicJob := nomad.TranslatePeriodicForRegion(st.spec, procName, proc, st.imageTag, env, region)
			periodicEvalID, err := p.Nomad.SubmitJobRegion(periodicJob, region.NomadRegion)
			if err != nil {
				_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusFailed, "", err.Error(), 0)
				return fmt.Errorf("submit periodic job %s in region %s: %w", procName, region.Name, err)
			}
			sg.Log(ctx, "nomad.submitted", fmt.Sprintf("periodic job %s submitted in %s (eval: %s)", procName, region.Name, periodicEvalID), map[string]string{
				"step": "submit", "region": region.Name,
			})
		}
		if regionalServiceProcessCount(st.spec, region.Name) == 0 {
			_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusHealthy, "", "", 0)
		}
	}

	return nil
}

func regionalServiceProcessCount(spec *model.InfraSpec, region string) int {
	count := 0
	for _, proc := range spec.Processes {
		if proc.Schedule == "" && spec.ProcessRunsInRegion(proc, region) {
			count++
		}
	}
	return count
}
