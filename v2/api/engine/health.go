package engine

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"norn/v2/api/model"
)

const (
	healthDefaultInterval = 10 * time.Second
	healthDefaultTimeout  = 5 * time.Second
	healthWarningAfter    = 1  // consecutive failures to enter warning
	healthCriticalAfter   = 3  // consecutive failures to enter critical
)

func (e *Engine) runHealthChecker(ctx context.Context) {
	ticker := time.NewTicker(healthDefaultInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.runHealthChecks(ctx)
		}
	}
}

func (e *Engine) runHealthChecks(ctx context.Context) {
	specs := e.discoverSpecs()
	if specs == nil {
		return
	}

	specMap := make(map[string]*model.InfraSpec, len(specs))
	for _, s := range specs {
		specMap[s.App] = s
	}

	e.mu.RLock()
	var toCheck []Instance
	for _, inst := range e.instances {
		if !inst.IsRunning() || inst.Kind == "batch" {
			continue
		}
		toCheck = append(toCheck, *inst)
	}
	e.mu.RUnlock()

	for _, inst := range toCheck {
		spec, ok := specMap[inst.App]
		if !ok {
			continue
		}
		proc, ok := spec.Processes[inst.Process]
		if !ok {
			continue
		}
		if proc.Health == nil || proc.Health.Path == "" {
			// No health check configured — mark as healthy by default
			e.setHealthy(inst.ContainerName, true)
			continue
		}
		if inst.IP == "" || inst.Port == 0 {
			continue
		}

		timeout := healthDefaultTimeout
		if proc.Health.Timeout != "" {
			if d, err := time.ParseDuration(proc.Health.Timeout); err == nil {
				timeout = d
			}
		}

		url := fmt.Sprintf("http://%s:%d%s", inst.IP, inst.Port, proc.Health.Path)
		healthy := e.checkHTTP(ctx, url, timeout)
		e.updateHealthState(inst.ContainerName, healthy)
	}
}

func (e *Engine) checkHTTP(ctx context.Context, url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func (e *Engine) updateHealthState(containerName string, passed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	hs, ok := e.health[containerName]
	if !ok {
		hs = &healthState{status: "passing"}
		e.health[containerName] = hs
	}
	hs.lastCheck = time.Now()

	if passed {
		hs.failures = 0
		hs.status = "passing"
		hs.lastHealthy = time.Now()
	} else {
		hs.failures++
		if hs.failures >= healthCriticalAfter {
			hs.status = "critical"
		} else if hs.failures >= healthWarningAfter {
			hs.status = "warning"
		}
	}

	if inst, ok := e.instances[containerName]; ok {
		healthy := hs.status == "passing"
		inst.Healthy = &healthy
	}
}

func (e *Engine) setHealthy(containerName string, healthy bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if inst, ok := e.instances[containerName]; ok {
		inst.Healthy = &healthy
	}
	if _, ok := e.health[containerName]; !ok {
		status := "passing"
		if !healthy {
			status = "critical"
		}
		e.health[containerName] = &healthState{
			status:    status,
			lastCheck: time.Now(),
		}
	}
}

// WaitHealthy blocks until all non-terminal service instances for appID are
// healthy, or the context/timeout expires.
func (e *Engine) WaitHealthy(ctx context.Context, appID string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s to become healthy", appID)
		case <-ticker.C:
			if e.allHealthy(appID) {
				return nil
			}
		}
	}
}

func (e *Engine) allHealthy(appID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	count := 0
	healthy := 0
	for _, inst := range e.instances {
		if inst.App != appID || inst.IsTerminal() || inst.Kind == "cron" || inst.Kind == "batch" {
			continue
		}
		count++
		if inst.Healthy != nil && *inst.Healthy {
			healthy++
		}
	}
	return count > 0 && count == healthy
}

// WaitBatchComplete blocks until a batch container exits, returns its status and exit code.
func (e *Engine) WaitBatchComplete(ctx context.Context, containerName string, timeout time.Duration) (string, int, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", -1, ctx.Err()
		case <-deadline:
			return "", -1, fmt.Errorf("timeout waiting for batch %s", containerName)
		case <-ticker.C:
			info, err := containerInspect(ctx, containerName)
			if err != nil {
				continue
			}
			status := normalizeStatus(info.Status)
			if status == "stopped" || status == "failed" {
				exitCode := 0
				if info.ExitCode != nil {
					exitCode = *info.ExitCode
				}
				containerRemove(ctx, containerName)
				if exitCode != 0 {
					return "failed", exitCode, nil
				}
				return "complete", exitCode, nil
			}
		}
	}
}

func (e *Engine) discoverSpecs() []*model.InfraSpec {
	specs, err := model.DiscoverApps(e.appsDir)
	if err != nil {
		log.Printf("engine: discover apps: %v", err)
		return nil
	}
	return specs
}
