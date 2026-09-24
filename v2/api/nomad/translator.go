package nomad

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// Translate converts an InfraSpec into a Nomad job specification.
// Each process in the infraspec becomes a TaskGroup within the job.
// Scheduled processes (cron) are translated into separate periodic batch jobs.
func Translate(spec *model.InfraSpec, imageTag string, env map[string]string) *nomadapi.Job {
	return TranslateForRegion(spec, imageTag, env, spec.ResolvedRegions()[0])
}

// TranslateForRegion creates the regional service job and filters processes by
// their effective placement. Nomad regions provide an independent namespace,
// so the same stable app job ID is intentionally reused in every region.
func TranslateForRegion(spec *model.InfraSpec, imageTag string, env map[string]string, region model.ResolvedRegion) *nomadapi.Job {
	return TranslateForRegionAt(spec, imageTag, env, region, 0)
}

// TranslateForRegionAt is TranslateForRegion whose database templates read
// the delivery staged for databaseRevision (0 reads the promoted delivery).
func TranslateForRegionAt(spec *model.InfraSpec, imageTag string, env map[string]string, region model.ResolvedRegion, databaseRevision int64) *nomadapi.Job {
	jobID := spec.App
	jobType := "service"

	job := nomadapi.NewServiceJob(jobID, jobID, region.NomadRegion, 50)
	if pool := spec.EffectiveNodePool(); pool != "" {
		job.NodePool = strPtr(pool)
	}
	job.Datacenters = append([]string(nil), region.Datacenters...)
	job.Meta = map[string]string{
		"deploy_ts":   fmt.Sprintf("%d", time.Now().UnixMilli()),
		"norn_region": region.Name,
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
		if !spec.ProcessRunsInRegion(proc, region.Name) {
			continue
		}
		if proc.Schedule != "" || proc.Function != nil {
			// Scheduled and function processes run as separate batch jobs.
			continue
		}

		tg := nomadapi.NewTaskGroup(procName, 1)

		// Scaling
		if proc.Scaling != nil && (proc.Scaling.Min > 0 || proc.Scaling.PerRegion > 0) {
			count := proc.Scaling.Min
			if count == 0 {
				count = 1
			}
			if proc.Scaling.PerRegion > 0 {
				count = proc.Scaling.PerRegion
			}
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
			MaxParallel:    &maxParallel,
			MinHealthyTime: &healthy,
			AutoRevert:     &autoRevert,
		}

		// Canary deployment: Norn manages promotion/failure instead of Nomad auto-revert
		if proc.Canary != nil && proc.Canary.Count > 0 {
			canaryCount := proc.Canary.Count
			tg.Update.Canary = &canaryCount
			autoRevert = false
			tg.Update.AutoRevert = &autoRevert
		}

		// Task
		task := newDockerTask(procName)
		task.Config = map[string]interface{}{
			"image": imageTag,
		}
		if proc.Command != "" {
			task.Config["command"] = "/bin/sh"
			task.Config["args"] = []string{"-c", proc.Command}
		}

		configureProcessNetworking(spec, procName, proc, region, task, tg)

		// Environment
		task.Env = mergeProcessEnv(mergedEnv, proc.Env)
		addDatabaseTemplates(spec, jobID, databaseRevision, task)

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

		// Volume mounts
		for _, vol := range spec.Volumes {
			tg.Volumes = map[string]*nomadapi.VolumeRequest{
				vol.Name: {
					Name:     vol.Name,
					Type:     "host",
					Source:   vol.Name,
					ReadOnly: vol.ReadOnly,
				},
			}
			task.VolumeMounts = append(task.VolumeMounts, &nomadapi.VolumeMount{
				Volume:      &vol.Name,
				Destination: &vol.Mount,
				ReadOnly:    &vol.ReadOnly,
			})
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

// ApplyDesiredReplicaCounts overlays acknowledged control-plane scale intent
// on a newly translated service job. An absent entry deliberately preserves
// the InfraSpec Scaling.Min/PerRegion result, which is the compatibility
// behavior for apps never scaled through the durable API.
func ApplyDesiredReplicaCounts(job *nomadapi.Job, counts map[string]int) {
	if job == nil || len(counts) == 0 {
		return
	}
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil {
			continue
		}
		if count, ok := counts[*group.Name]; ok {
			group.Count = replicaCountPtr(count)
		}
	}
}

func replicaCountPtr(value int) *int { return &value }

func configureProcessNetworking(spec *model.InfraSpec, procName string, proc model.Process, region model.ResolvedRegion, task *nomadapi.Task, tg *nomadapi.TaskGroup) {
	ports := []string{}
	net := &nomadapi.NetworkResource{}
	services := []*nomadapi.Service{}

	if proc.Port > 0 {
		portLabel := fmt.Sprintf("%s-http", procName)
		ports = append(ports, portLabel)
		// Endpoint traffic is normally discovered through Consul/Traefik, so
		// host ports remain dynamic. Platform ingress can explicitly reserve a
		// stable hostPort with one allocation per region.
		if proc.HostPort > 0 {
			net.ReservedPorts = append(net.ReservedPorts, nomadapi.Port{Label: portLabel, Value: proc.HostPort, To: proc.Port})
		} else {
			net.DynamicPorts = append(net.DynamicPorts, nomadapi.Port{Label: portLabel, To: proc.Port})
		}
		svc := &nomadapi.Service{
			Name:      fmt.Sprintf("%s-%s", spec.App, procName),
			PortLabel: portLabel,
			Provider:  "consul",
		}
		svc.Tags = regionalIngressTags(spec, procName, proc, region)
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
		services = append(services, svc)
	}

	if proc.Metrics != nil && proc.Metrics.Enabled {
		metricsPort := proc.Metrics.Port
		if metricsPort == 0 {
			metricsPort = proc.Port
		}
		metricsPath := proc.Metrics.Path
		if metricsPath == "" {
			metricsPath = "/metrics"
		}
		metricsLabel := fmt.Sprintf("%s-metrics", procName)
		if metricsPort > 0 && metricsPort != proc.Port {
			ports = append(ports, metricsLabel)
			net.DynamicPorts = append(net.DynamicPorts, nomadapi.Port{Label: metricsLabel, To: metricsPort})
		} else if proc.Port > 0 {
			metricsLabel = fmt.Sprintf("%s-http", procName)
		}
		if metricsPort > 0 {
			services = append(services, &nomadapi.Service{
				Name:      fmt.Sprintf("%s-%s-metrics", spec.App, procName),
				PortLabel: metricsLabel,
				Provider:  "consul",
				Tags:      []string{"metrics", "prometheus"},
				Checks: []nomadapi.ServiceCheck{
					{
						Type:     "http",
						Path:     metricsPath,
						Interval: 30 * time.Second,
						Timeout:  5 * time.Second,
					},
				},
			})
		}
	}

	if len(ports) > 0 {
		task.Config["ports"] = ports
		tg.Networks = []*nomadapi.NetworkResource{net}
	}
	if len(services) > 0 {
		tg.Services = services
	}
}

func regionalIngressTags(spec *model.InfraSpec, procName string, proc model.Process, region model.ResolvedRegion) []string {
	if proc.Port <= 0 || len(spec.Endpoints) == 0 {
		return nil
	}
	router := strings.ReplaceAll(fmt.Sprintf("%s-%s-%s", spec.App, procName, region.Name), "_", "-")
	tags := []string{
		"traefik.enable=true",
		fmt.Sprintf("traefik.http.routers.%s.entrypoints=web", router),
		fmt.Sprintf("norn.region=%s", region.Name),
		fmt.Sprintf("norn.traffic-weight=%d", region.TrafficWeight),
	}
	var hosts []string
	for _, endpoint := range spec.Endpoints {
		if endpoint.Region != "" && endpoint.Region != region.Name {
			continue
		}
		parsed, err := url.Parse(endpoint.URL)
		if err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, fmt.Sprintf("Host(`%s`)", parsed.Hostname()))
		}
	}
	if len(hosts) > 0 {
		tags = append(tags, fmt.Sprintf("traefik.http.routers.%s.rule=%s", router, strings.Join(hosts, " || ")))
	}
	return tags
}

// TranslatePeriodic creates a separate Nomad periodic batch job for a scheduled process.
func TranslatePeriodic(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) *nomadapi.Job {
	return TranslatePeriodicForRegion(spec, procName, proc, imageTag, env, spec.ResolvedRegions()[0])
}

func TranslatePeriodicForRegion(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, region model.ResolvedRegion) *nomadapi.Job {
	return TranslatePeriodicForRegionAt(spec, procName, proc, imageTag, env, region, 0)
}

// TranslatePeriodicForRegionAt is TranslatePeriodicForRegion reading the
// delivery staged for databaseRevision (0 reads the promoted delivery).
func TranslatePeriodicForRegionAt(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, region model.ResolvedRegion, databaseRevision int64) *nomadapi.Job {
	jobID := fmt.Sprintf("%s-%s", spec.App, procName)
	job := nomadapi.NewBatchJob(jobID, jobID, region.NomadRegion, 50)
	if pool := spec.EffectiveNodePool(); pool != "" {
		job.NodePool = strPtr(pool)
	}
	job.Datacenters = append([]string(nil), region.Datacenters...)
	job.Meta = map[string]string{"norn_region": region.Name}
	job.Periodic = &nomadapi.PeriodicConfig{
		Enabled:  boolPtr(true),
		SpecType: strPtr("cron"),
		Spec:     &proc.Schedule,
	}
	if timezone := model.ResolveProcessTimezone(spec, proc); timezone != "" {
		job.Periodic.TimeZone = &timezone
	}

	mergedEnv := make(map[string]string)
	for k, v := range spec.Env {
		mergedEnv[k] = v
	}
	for k, v := range env {
		mergedEnv[k] = v
	}

	tg := nomadapi.NewTaskGroup(procName, 1)
	task := newDockerTask(procName)
	task.Config = map[string]interface{}{
		"image": imageTag,
	}
	if proc.Command != "" {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-c", proc.Command}
	}
	task.Env = mergeProcessEnv(mergedEnv, proc.Env)
	addDatabaseTemplates(spec, jobID, databaseRevision, task)

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

	// Volume mounts for periodic jobs
	for _, vol := range spec.Volumes {
		tg.Volumes = map[string]*nomadapi.VolumeRequest{
			vol.Name: {
				Name:     vol.Name,
				Type:     "host",
				Source:   vol.Name,
				ReadOnly: vol.ReadOnly,
			},
		}
		task.VolumeMounts = append(task.VolumeMounts, &nomadapi.VolumeMount{
			Volume:      &vol.Name,
			Destination: &vol.Mount,
			ReadOnly:    &vol.ReadOnly,
		})
	}

	tg.Tasks = []*nomadapi.Task{task}
	job.TaskGroups = []*nomadapi.TaskGroup{tg}

	return job
}

// TranslateBatch creates a one-shot Nomad batch job for a function invocation.
func TranslateBatch(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, jobID string) *nomadapi.Job {
	return TranslateBatchAt(spec, procName, proc, imageTag, env, jobID, 0)
}

// TranslateBatchAt is TranslateBatch whose database templates read the
// invocation's own copy of databaseRevision (0 fails closed at render).
func TranslateBatchAt(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, jobID string, databaseRevision int64) *nomadapi.Job {
	job := nomadapi.NewBatchJob(jobID, jobID, "global", 50)
	job.Datacenters = []string{"dc1"}

	mergedEnv := make(map[string]string)
	for k, v := range spec.Env {
		mergedEnv[k] = v
	}
	for k, v := range env {
		mergedEnv[k] = v
	}

	tg := nomadapi.NewTaskGroup(procName, 1)

	// No retries for batch jobs
	attempts := 0
	interval := 1 * time.Minute
	delay := 5 * time.Second
	mode := "fail"
	tg.RestartPolicy = &nomadapi.RestartPolicy{
		Attempts: &attempts,
		Interval: &interval,
		Delay:    &delay,
		Mode:     &mode,
	}

	task := newDockerTask(procName)
	task.Config = map[string]interface{}{
		"image": imageTag,
	}
	if proc.Command != "" {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-c", proc.Command}
	}
	task.Env = mergeProcessEnv(mergedEnv, proc.Env)
	// A function invocation reads its own copy of one delivery revision.
	addDatabaseTemplates(spec, jobID, databaseRevision, task)

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
	if proc.Function != nil && proc.Function.Memory > 0 {
		mem = proc.Function.Memory
	}
	task.Resources = &nomadapi.Resources{
		CPU:      &cpu,
		MemoryMB: &mem,
	}

	// Volume mounts for batch jobs
	for _, vol := range spec.Volumes {
		tg.Volumes = map[string]*nomadapi.VolumeRequest{
			vol.Name: {
				Name:     vol.Name,
				Type:     "host",
				Source:   vol.Name,
				ReadOnly: vol.ReadOnly,
			},
		}
		task.VolumeMounts = append(task.VolumeMounts, &nomadapi.VolumeMount{
			Volume:      &vol.Name,
			Destination: &vol.Mount,
			ReadOnly:    &vol.ReadOnly,
		})
	}

	tg.Tasks = []*nomadapi.Task{task}
	job.TaskGroups = []*nomadapi.TaskGroup{tg}

	return job
}

// Every task carries an explicit log rotation budget rather than relying on
// runtime defaults: at most TaskLogMaxFiles files of TaskLogMaxFileSizeMB per
// stream (stdout and stderr each), which fits Nomad's default ephemeral disk.
const (
	TaskLogMaxFiles      = 5
	TaskLogMaxFileSizeMB = 10
)

func newDockerTask(name string) *nomadapi.Task {
	task := nomadapi.NewTask(name, "docker")
	maxFiles, maxFileSize, disabled := TaskLogMaxFiles, TaskLogMaxFileSizeMB, false
	task.LogConfig = &nomadapi.LogConfig{MaxFiles: &maxFiles, MaxFileSizeMB: &maxFileSize, Disabled: &disabled}
	return task
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func mergeProcessEnv(base, process map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(process))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range process {
		out[key] = value
	}
	return out
}
