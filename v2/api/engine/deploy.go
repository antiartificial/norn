package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// SubmitJob deploys an app from its InfraSpec. Handles both new deploys and
// updates (rolling replacement of running instances).
func (e *Engine) SubmitJob(ctx context.Context, spec *model.InfraSpec, imageTag string, env map[string]string) error {
	existing, _ := e.JobInstances(spec.App)
	isUpdate := len(existing) > 0

	deployID := uuid.New().String()
	deploy := &Deployment{
		ID:        deployID,
		App:       spec.App,
		ImageTag:  imageTag,
		Status:    "running",
		CreatedAt: time.Now(),
	}

	hasCanary := false
	for _, proc := range spec.Processes {
		if proc.Canary != nil && proc.Canary.Count > 0 {
			hasCanary = true
			break
		}
	}

	if hasCanary && isUpdate {
		deploy.IsCanary = true
		deploy.Status = "canary"
		if len(existing) > 0 {
			deploy.OldImage = existing[0].ImageTag
		}
	}

	e.mu.Lock()
	e.deploys[spec.App] = deploy
	e.mu.Unlock()

	for procName, proc := range spec.Processes {
		if proc.Schedule != "" {
			continue
		}

		mergedEnv := mergeEnv(spec.Env, proc.Env, env)

		if hasCanary && isUpdate && proc.Canary != nil && proc.Canary.Count > 0 {
			if err := e.deployCanary(ctx, spec, procName, proc, imageTag, mergedEnv); err != nil {
				return fmt.Errorf("canary %s/%s: %w", spec.App, procName, err)
			}
			continue
		}

		count := 1
		if proc.Scaling != nil && proc.Scaling.Min > 0 {
			count = proc.Scaling.Min
		}

		if isUpdate {
			if err := e.rollingUpdate(ctx, spec, procName, proc, imageTag, mergedEnv, count); err != nil {
				return fmt.Errorf("update %s/%s: %w", spec.App, procName, err)
			}
		} else {
			if err := e.deployNew(ctx, spec, procName, proc, imageTag, mergedEnv, count); err != nil {
				return fmt.Errorf("deploy %s/%s: %w", spec.App, procName, err)
			}
		}
	}

	if !hasCanary || !isUpdate {
		e.mu.Lock()
		deploy.Status = "complete"
		e.mu.Unlock()
	}

	return nil
}

func (e *Engine) deployNew(ctx context.Context, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, count int) error {
	for i := 0; i < count; i++ {
		name := ContainerName(spec.App, procName, i)
		opts := buildRunOpts(name, spec, procName, proc, imageTag, env)
		if err := containerRun(ctx, opts); err != nil {
			return fmt.Errorf("run %s: %w", name, err)
		}
		e.registerInstance(name, spec.App, procName, i, "service", imageTag, proc.Port)
	}
	return nil
}

func (e *Engine) rollingUpdate(ctx context.Context, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string, count int) error {
	// Collect old instances for this process
	old := e.processInstances(spec.App, procName)

	// Start new instances one at a time (maxParallel=1)
	for i := 0; i < count; i++ {
		name := ContainerName(spec.App, procName, i)

		// If an old instance has the same name, use a temporary name
		tempName := name
		for _, o := range old {
			if o.ContainerName == name {
				tempName = name + "-new"
				break
			}
		}

		opts := buildRunOpts(tempName, spec, procName, proc, imageTag, env)
		if err := containerRun(ctx, opts); err != nil {
			return fmt.Errorf("run %s: %w", tempName, err)
		}
		e.registerInstance(tempName, spec.App, procName, i, "service", imageTag, proc.Port)

		// Wait for new instance to become healthy (30s min healthy time)
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := e.WaitInstanceHealthy(waitCtx, tempName, 30*time.Second)
		cancel()
		if err != nil {
			log.Printf("engine: rolling update: new instance %s unhealthy: %v", tempName, err)
			containerStopAndRemove(ctx, tempName, 10*time.Second)
			e.removeInstance(tempName)
			return fmt.Errorf("new instance unhealthy: %w", err)
		}
	}

	// Drain and stop old instances
	drainTimeout := 30 * time.Second
	if proc.Drain != nil && proc.Drain.Timeout != "" {
		if d, err := time.ParseDuration(proc.Drain.Timeout); err == nil {
			drainTimeout = d
		}
	}

	for _, o := range old {
		containerStopAndRemove(ctx, o.ContainerName, drainTimeout)
		e.removeInstance(o.ContainerName)
	}

	// Rename temporary instances if needed
	e.mu.Lock()
	for name, inst := range e.instances {
		if inst.App == spec.App && inst.Process == procName {
			expected := ContainerName(spec.App, procName, inst.Replica)
			if name != expected && name == expected+"-new" {
				e.instances[expected] = inst
				inst.ContainerName = expected
				delete(e.instances, name)
			}
		}
	}
	e.mu.Unlock()

	return nil
}

func (e *Engine) deployCanary(ctx context.Context, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) error {
	count := proc.Canary.Count
	for i := 0; i < count; i++ {
		name := CanaryName(spec.App, procName, i)
		opts := buildRunOpts(name, spec, procName, proc, imageTag, env)
		if err := containerRun(ctx, opts); err != nil {
			return fmt.Errorf("run canary %s: %w", name, err)
		}
		e.registerInstance(name, spec.App, procName, i, "canary", imageTag, proc.Port)
	}
	return nil
}

// PromoteDeployment promotes canary instances to service, stops old instances.
func (e *Engine) PromoteDeployment(ctx context.Context, appID string) error {
	e.mu.Lock()
	deploy, ok := e.deploys[appID]
	if !ok || deploy.Status != "canary" {
		e.mu.Unlock()
		return fmt.Errorf("no canary deployment for %s", appID)
	}
	deploy.Status = "promoted"
	e.mu.Unlock()

	specs := e.discoverSpecs()
	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == appID {
			spec = s
			break
		}
	}
	if spec == nil {
		return fmt.Errorf("spec not found for %s", appID)
	}

	// Start remaining non-canary instances with new image
	for procName, proc := range spec.Processes {
		if proc.Schedule != "" || proc.Canary == nil {
			continue
		}
		count := 1
		if proc.Scaling != nil && proc.Scaling.Min > 0 {
			count = proc.Scaling.Min
		}
		mergedEnv := mergeEnv(spec.Env, proc.Env, nil)

		// Deploy remaining replicas (beyond canary count)
		for i := proc.Canary.Count; i < count; i++ {
			name := ContainerName(appID, procName, i)
			opts := buildRunOpts(name, spec, procName, proc, deploy.ImageTag, mergedEnv)
			if err := containerRun(ctx, opts); err != nil {
				return fmt.Errorf("promote %s: %w", name, err)
			}
			e.registerInstance(name, appID, procName, i, "service", deploy.ImageTag, proc.Port)
		}

		// Stop old instances
		old := e.processInstances(appID, procName)
		drainTimeout := 30 * time.Second
		if proc.Drain != nil && proc.Drain.Timeout != "" {
			if d, err := time.ParseDuration(proc.Drain.Timeout); err == nil {
				drainTimeout = d
			}
		}
		for _, o := range old {
			if o.Kind == "canary" || o.ImageTag == deploy.ImageTag {
				continue
			}
			containerStopAndRemove(ctx, o.ContainerName, drainTimeout)
			e.removeInstance(o.ContainerName)
		}

		// Rename canary instances to service instances
		e.mu.Lock()
		for name, inst := range e.instances {
			if inst.App == appID && inst.Process == procName && inst.Kind == "canary" {
				newName := ContainerName(appID, procName, inst.Replica)
				inst.Kind = "service"
				inst.ContainerName = newName
				delete(e.instances, name)
				e.instances[newName] = inst
			}
		}
		e.mu.Unlock()
	}

	e.mu.Lock()
	deploy.Status = "complete"
	e.mu.Unlock()

	return nil
}

// FailDeployment stops canary instances and reverts to old instances.
func (e *Engine) FailDeployment(ctx context.Context, appID string) error {
	e.mu.Lock()
	deploy, ok := e.deploys[appID]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("no deployment for %s", appID)
	}
	deploy.Status = "failed"
	e.mu.Unlock()

	// Stop all canary instances
	e.mu.RLock()
	var canaries []string
	for name, inst := range e.instances {
		if inst.App == appID && inst.Kind == "canary" {
			canaries = append(canaries, name)
		}
	}
	e.mu.RUnlock()

	for _, name := range canaries {
		containerStopAndRemove(ctx, name, 10*time.Second)
		e.removeInstance(name)
	}

	return nil
}

// LatestDeployment returns info about the current deployment for an app.
func (e *Engine) LatestDeployment(appID string) (*DeploymentInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	deploy, ok := e.deploys[appID]
	if !ok {
		return nil, nil
	}
	return &DeploymentInfo{
		ID:         deploy.ID,
		App:        deploy.App,
		Status:     deploy.Status,
		StatusDesc: deploy.Status + " deployment",
		IsCanary:   deploy.IsCanary,
	}, nil
}

// StopJob stops all containers for an app.
func (e *Engine) StopJob(ctx context.Context, appID string, purge bool) error {
	instances, _ := e.JobInstances(appID)
	for _, inst := range instances {
		if inst.IsRunning() {
			containerStopAndRemove(ctx, inst.ContainerName, 30*time.Second)
		} else if purge {
			containerRemove(ctx, inst.ContainerName)
		}
		e.removeInstance(inst.ContainerName)
	}

	if purge {
		e.mu.Lock()
		delete(e.deploys, appID)
		// Remove cron entries for this app
		for id, entry := range e.cronJobs {
			if entry.App == appID {
				delete(e.cronJobs, id)
			}
		}
		e.mu.Unlock()
	}

	return nil
}

// RestartJob restarts all running instances for an app by stopping and re-running them.
func (e *Engine) RestartJob(ctx context.Context, appID string) error {
	instances, _ := e.JobInstances(appID)
	for _, inst := range instances {
		if !inst.IsRunning() || inst.Kind == "cron" || inst.Kind == "batch" {
			continue
		}
		containerStopAndRemove(ctx, inst.ContainerName, 30*time.Second)
		opts := RunOpts{
			Name:  inst.ContainerName,
			Image: inst.ImageTag,
			Port:  inst.Port,
		}
		if err := containerRun(ctx, opts); err != nil {
			log.Printf("engine: restart %s: %v", inst.ContainerName, err)
			continue
		}
		e.mu.Lock()
		inst2 := e.instances[inst.ContainerName]
		if inst2 != nil {
			inst2.Status = "running"
			inst2.StartedAt = time.Now()
		}
		e.mu.Unlock()
	}
	return nil
}

// ScaleJob adjusts the replica count for a process.
func (e *Engine) ScaleJob(ctx context.Context, appID, process string, count int) error {
	current := e.processInstances(appID, process)
	running := 0
	for _, inst := range current {
		if inst.IsRunning() && inst.Kind == "service" {
			running++
		}
	}

	if count > running {
		// Scale up
		specs := e.discoverSpecs()
		var spec *model.InfraSpec
		for _, s := range specs {
			if s.App == appID {
				spec = s
				break
			}
		}
		if spec == nil {
			return fmt.Errorf("spec not found for %s", appID)
		}
		proc, ok := spec.Processes[process]
		if !ok {
			return fmt.Errorf("process %s not found in %s", process, appID)
		}

		// Find the current image from a running instance
		imageTag := ""
		for _, inst := range current {
			if inst.IsRunning() {
				imageTag = inst.ImageTag
				break
			}
		}
		if imageTag == "" {
			return fmt.Errorf("no running instance to derive image for %s/%s", appID, process)
		}

		mergedEnv := mergeEnv(spec.Env, proc.Env, nil)
		for i := running; i < count; i++ {
			name := ContainerName(appID, process, i)
			opts := buildRunOpts(name, spec, process, proc, imageTag, mergedEnv)
			if err := containerRun(ctx, opts); err != nil {
				return fmt.Errorf("scale up %s: %w", name, err)
			}
			e.registerInstance(name, appID, process, i, "service", imageTag, proc.Port)
		}
	} else if count < running {
		// Scale down — remove highest-numbered instances first
		toRemove := running - count
		for i := len(current) - 1; i >= 0 && toRemove > 0; i-- {
			inst := current[i]
			if inst.IsRunning() && inst.Kind == "service" {
				containerStopAndRemove(ctx, inst.ContainerName, 30*time.Second)
				e.removeInstance(inst.ContainerName)
				toRemove--
			}
		}
	}

	return nil
}

// WaitInstanceHealthy waits for a specific instance to become healthy.
func (e *Engine) WaitInstanceHealthy(ctx context.Context, containerName string, minHealthyTime time.Duration) error {
	healthySince := time.Time{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.mu.RLock()
			inst, ok := e.instances[containerName]
			var isHealthy bool
			if ok && inst.Healthy != nil {
				isHealthy = *inst.Healthy
			}
			e.mu.RUnlock()

			if !ok {
				return fmt.Errorf("instance %s not found", containerName)
			}
			if isHealthy {
				if healthySince.IsZero() {
					healthySince = time.Now()
				}
				if time.Since(healthySince) >= minHealthyTime {
					return nil
				}
			} else {
				healthySince = time.Time{}
			}
		}
	}
}

func (e *Engine) processInstances(appID, process string) []Instance {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Instance
	for _, inst := range e.instances {
		if inst.App == appID && inst.Process == process {
			out = append(out, *inst)
		}
	}
	return out
}

func (e *Engine) registerInstance(name, app, process string, replica int, kind, imageTag string, port int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.instances[name] = &Instance{
		ContainerName: name,
		App:           app,
		Process:       process,
		Replica:       replica,
		Kind:          kind,
		Status:        "running",
		ImageTag:      imageTag,
		Port:          port,
		StartedAt:     time.Now(),
	}
}

func (e *Engine) removeInstance(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.instances, name)
	delete(e.health, name)
	delete(e.restarts, name)
}

// RunBatch starts a one-shot batch container for a function invocation.
// It returns the container name so callers can wait on completion.
func (e *Engine) RunBatch(ctx context.Context, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) (string, error) {
	name := BatchName(spec.App, procName, time.Now().Unix())
	mergedEnv := mergeEnv(spec.Env, proc.Env, env)
	opts := buildRunOpts(name, spec, procName, proc, imageTag, mergedEnv)
	if err := containerRun(ctx, opts); err != nil {
		return "", fmt.Errorf("run batch %s: %w", name, err)
	}
	e.registerInstance(name, spec.App, procName, 0, "batch", imageTag, proc.Port)
	return name, nil
}

func buildRunOpts(name string, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) RunOpts {
	opts := RunOpts{
		Name:    name,
		Image:   imageTag,
		Command: proc.Command,
		Env:     env,
		Port:    proc.Port,
	}
	if proc.Resources != nil {
		opts.CPUs = proc.Resources.CPU / 100 // MHz to approximate cores
		opts.MemoryMB = proc.Resources.Memory
	}
	for _, vol := range spec.Volumes {
		opts.Volumes = append(opts.Volumes, VolumeMount{
			Source:   vol.Name,
			Target:   vol.Mount,
			ReadOnly: vol.ReadOnly,
		})
	}
	return opts
}

func mergeEnv(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
