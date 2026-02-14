package nomad

import (
	"fmt"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// Translate converts an InfraSpec into a Nomad job specification.
// Each process in the infraspec becomes a TaskGroup within the job.
// Scheduled processes (cron) are translated into separate periodic batch jobs.
func Translate(spec *model.InfraSpec, imageTag string, env map[string]string) *nomadapi.Job {
	jobID := spec.App
	jobType := "service"

	job := nomadapi.NewServiceJob(jobID, jobID, "global", 50)
	job.Datacenters = []string{"dc1"}
	job.Meta = map[string]string{
		"deploy_ts": fmt.Sprintf("%d", time.Now().UnixMilli()),
	}

	// Merge spec.Env with provided env (secrets, etc.)
	mergedEnv := make(map[string]string)
	for k, v := range spec.Env {
		mergedEnv[k] = v
	}
	for k, v := range env {
		mergedEnv[k] = v
	}

	for procName, proc := range spec.Processes {
		if proc.Schedule != "" {
			// Scheduled processes become separate batch jobs — skip here
			continue
		}

		tg := nomadapi.NewTaskGroup(procName, 1)

		// Scaling
		if proc.Scaling != nil && proc.Scaling.Min > 0 {
			count := proc.Scaling.Min
			tg.Count = &count
		}

		// Restart policy
		attempts := 3
		interval := 5 * time.Minute
		delay := 15 * time.Second
		mode := "delay"
		tg.RestartPolicy = &nomadapi.RestartPolicy{
			Attempts: &attempts,
			Interval: &interval,
			Delay:    &delay,
			Mode:     &mode,
		}

		// Update strategy
		maxParallel := 1
		healthy := 30 * time.Second
		autoRevert := true
		tg.Update = &nomadapi.UpdateStrategy{
			MaxParallel:     &maxParallel,
			MinHealthyTime:  &healthy,
			AutoRevert:      &autoRevert,
		}

		// Task
		task := nomadapi.NewTask(procName, "docker")
		task.Config = map[string]interface{}{
			"image": imageTag,
		}
		if proc.Command != "" {
			task.Config["command"] = "/bin/sh"
			task.Config["args"] = []string{"-c", proc.Command}
		}

		// Port mapping
		if proc.Port > 0 {
			portLabel := fmt.Sprintf("%s-http", procName)
			task.Config["ports"] = []string{portLabel}

			net := &nomadapi.NetworkResource{}
			if len(spec.Endpoints) > 0 {
				// Static port for apps with external endpoints (predictable routing)
				net.ReservedPorts = []nomadapi.Port{
					{Label: portLabel, Value: proc.Port},
				}
			} else {
				net.DynamicPorts = []nomadapi.Port{
					{Label: portLabel, To: proc.Port},
				}
			}
			tg.Networks = []*nomadapi.NetworkResource{net}

			// Service registration with Consul
			svc := &nomadapi.Service{
				Name:      fmt.Sprintf("%s-%s", spec.App, procName),
				PortLabel: portLabel,
				Provider:  "consul",
			}

			if proc.Health != nil {
				interval, _ := time.ParseDuration(proc.Health.Interval)
				timeout, _ := time.ParseDuration(proc.Health.Timeout)
				if interval == 0 {
					interval = 10 * time.Second
				}
				if timeout == 0 {
					timeout = 5 * time.Second
				}
				svc.Checks = []nomadapi.ServiceCheck{
					{
						Type:     "http",
						Path:     proc.Health.Path,
						Interval: interval,
						Timeout:  timeout,
					},
				}
			}

			tg.Services = []*nomadapi.Service{svc}
		}

		// Environment
		task.Env = mergedEnv

		// Resources
		cpu := 100
		mem := 128
		if proc.Resources != nil {
			if proc.Resources.CPU > 0 {
				cpu = proc.Resources.CPU
			}
			if proc.Resources.Memory > 0 {
				mem = proc.Resources.Memory
			}
		}
		task.Resources = &nomadapi.Resources{
			CPU:      &cpu,
			MemoryMB: &mem,
		}

		// Kill signal / drain
		if proc.Drain != nil {
			if proc.Drain.Signal != "" {
				task.KillSignal = proc.Drain.Signal
			}
			if proc.Drain.Timeout != "" {
				d, err := time.ParseDuration(proc.Drain.Timeout)
				if err == nil {
					task.KillTimeout = &d
				}
			}
		}

		tg.Tasks = []*nomadapi.Task{task}
		job.TaskGroups = append(job.TaskGroups, tg)
	}

	// Override type if no service processes (all scheduled)
	if len(job.TaskGroups) == 0 {
		job.Type = &jobType
	}

	return job
}

// TranslatePeriodic creates a separate Nomad periodic batch job for a scheduled process.
func TranslatePeriodic(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) *nomadapi.Job {
	jobID := fmt.Sprintf("%s-%s", spec.App, procName)
	job := nomadapi.NewBatchJob(jobID, jobID, "global", 50)
	job.Datacenters = []string{"dc1"}
	job.Periodic = &nomadapi.PeriodicConfig{
		Enabled:  boolPtr(true),
		SpecType: strPtr("cron"),
		Spec:     &proc.Schedule,
	}

	mergedEnv := make(map[string]string)
	for k, v := range spec.Env {
		mergedEnv[k] = v
	}
	for k, v := range env {
		mergedEnv[k] = v
	}

	tg := nomadapi.NewTaskGroup(procName, 1)
	task := nomadapi.NewTask(procName, "docker")
	task.Config = map[string]interface{}{
		"image": imageTag,
	}
	if proc.Command != "" {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-c", proc.Command}
	}
	task.Env = mergedEnv

	cpu := 100
	mem := 128
	if proc.Resources != nil {
		if proc.Resources.CPU > 0 {
			cpu = proc.Resources.CPU
		}
		if proc.Resources.Memory > 0 {
			mem = proc.Resources.Memory
		}
	}
	task.Resources = &nomadapi.Resources{
		CPU:      &cpu,
		MemoryMB: &mem,
	}

	tg.Tasks = []*nomadapi.Task{task}
	job.TaskGroups = []*nomadapi.TaskGroup{tg}

	return job
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }
