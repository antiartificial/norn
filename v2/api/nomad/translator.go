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
		if proc.Schedule != "" {
			// Scheduled processes become separate batch jobs — skip here
			continue
		}

		tg := nomadapi.NewTaskGroup(procName, 1)
		if spec.RequiresDistinctHosts() {
			// Nomad requires distinct_hosts at job or group scope, never task
			// scope. Keeping it on each service group avoids coupling unrelated
			// processes while making every replica of this HA process distinct.
			tg.Constraints = append(tg.Constraints, nomadapi.NewConstraint("", nomadapi.ConstraintDistinctHosts, ""))
		}

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
		task := nomadapi.NewTask(procName, "docker")
		task.Config = map[string]interface{}{
			"image": imageTag,
		}
		if proc.Command != "" {
			task.Config["command"] = "/bin/sh"
			task.Config["args"] = []string{"-c", proc.Command}
		}

		configureProcessNetworking(spec, procName, proc, region, task, tg)

		// Job-owned Nomad variable values are deliberately withheld from task.Env
		// and instead rendered by Nomad into owner-only allocation files.
		task.Env = processEnvironment(spec, proc, mergedEnv, proc.Env)
		configureNomadVariableFiles(proc.NomadVariables, spec.App, task)

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
			metricsTags := append([]string{"metrics", "prometheus"}, servicePlacementTags(spec, region)...)
			services = append(services, &nomadapi.Service{
				Name:      fmt.Sprintf("%s-%s-metrics", spec.App, procName),
				PortLabel: metricsLabel,
				Provider:  "consul",
				Tags:      metricsTags,
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
	tags := servicePlacementTags(spec, region)
	if proc.Port <= 0 || len(spec.Endpoints) == 0 {
		return tags
	}
	router := strings.ReplaceAll(fmt.Sprintf("%s-%s-%s", spec.App, procName, region.Name), "_", "-")
	tags = append(tags,
		"traefik.enable=true",
		fmt.Sprintf("traefik.http.routers.%s.entrypoints=web", router),
		fmt.Sprintf("norn.traffic-weight=%d", region.TrafficWeight),
	)
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

func servicePlacementTags(spec *model.InfraSpec, region model.ResolvedRegion) []string {
	tags := []string{
		fmt.Sprintf("norn.region=%s", region.Name),
		"norn.allocation=${NOMAD_ALLOC_ID}",
	}
	if pool := spec.EffectiveNodePool(); pool != "" {
		tags = append(tags, fmt.Sprintf("norn.node-pool=%s", pool))
	}
	return tags
}

// TranslatePeriodic creates a separate Nomad periodic batch job for a scheduled process.
func TranslatePeriodic(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) *nomadapi.Job {
	return TranslatePeriodicForRegion(spec, procName, proc, imageTag, env, spec.ResolvedRegions()[0])
}

func TranslatePeriodicForRegion(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, region model.ResolvedRegion) *nomadapi.Job {
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
	configureNoRetryPolicy(tg)
	task := nomadapi.NewTask(procName, "docker")
	task.Config = map[string]interface{}{
		"image": imageTag,
	}
	if proc.Command != "" {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-c", proc.Command}
	}
	task.Env = processEnvironment(spec, proc, mergedEnv, proc.Env)
	configureNomadVariableFiles(proc.NomadVariables, fmt.Sprintf("%s-%s", spec.App, procName), task)

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
	configureNoRetryPolicy(tg)

	task := nomadapi.NewTask(procName, "docker")
	task.Config = map[string]interface{}{
		"image": imageTag,
	}
	if proc.Command != "" {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-c", proc.Command}
	}
	task.Env = processEnvironment(spec, proc, mergedEnv, proc.Env)
	configureNomadVariableFiles(proc.NomadVariables, jobID, task)

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

// configureNoRetryPolicy makes batch work fail once. Both policies must be
// explicit: a zero restart count does not by itself prevent Nomad from
// rescheduling a failed allocation.
func configureNoRetryPolicy(tg *nomadapi.TaskGroup) {
	attempts := 0
	mode := "fail"
	unlimited := false
	tg.RestartPolicy = &nomadapi.RestartPolicy{
		Attempts: &attempts,
		Mode:     &mode,
	}
	tg.ReschedulePolicy = &nomadapi.ReschedulePolicy{
		Attempts:  &attempts,
		Unlimited: &unlimited,
	}
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

func processEnvironment(spec *model.InfraSpec, proc model.Process, runtime, process map[string]string) map[string]string {
	// A configured Nomad-variable transport is an explicit opt-out from the
	// legacy pipeline secret environment. This prevents an unrelated resolved
	// secret from silently reintroducing the very task.Env channel the transport
	// is intended to eliminate.
	if proc.NomadVariables != nil {
		return mergeProcessEnv(spec.Env, process)
	}
	return mergeProcessEnv(runtime, process)
}

func configureNomadVariableFiles(transport *model.NomadVariableFiles, jobID string, task *nomadapi.Task) {
	if transport == nil || task == nil {
		return
	}
	variablePath := "nomad/jobs/" + jobID
	for _, file := range transport.Files {
		destination := "secrets/" + file.Destination
		mode := "0400"
		changeMode := "restart"
		envvars := false
		errorOnMissingKey := true
		uid, gid := transport.UID, transport.GID
		// All executable template text is constructed here from validated schema
		// fields. In particular, no arbitrary template, env template, or command
		// interpolation can be introduced through an InfraSpec.
		data := fmt.Sprintf("{{ with nomadVar %q }}{{ .%s.Value }}{{ end }}\n", variablePath, file.Key)
		task.Templates = append(task.Templates, &nomadapi.Template{
			DestPath:      &destination,
			EmbeddedTmpl:  &data,
			ChangeMode:    &changeMode,
			Perms:         &mode,
			Uid:           &uid,
			Gid:           &gid,
			Envvars:       &envvars,
			ErrMissingKey: &errorOnMissingKey,
		})
	}
}
