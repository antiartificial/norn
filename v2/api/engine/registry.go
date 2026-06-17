package engine

import (
	"fmt"
	"strings"

	"norn/v2/api/model"
)

// ServiceHealthChecks returns health status for all instances of a service.
// serviceName follows the convention "{app}-{process}" (same as Consul registration).
func (e *Engine) ServiceHealthChecks(serviceName string) ([]ServiceHealth, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []ServiceHealth
	for _, inst := range e.instances {
		if !inst.IsRunning() {
			continue
		}
		svcName := inst.App + "-" + inst.Process
		if svcName != serviceName {
			continue
		}
		status := "passing"
		if hs, ok := e.health[inst.ContainerName]; ok {
			status = hs.status
		} else if inst.Healthy != nil && !*inst.Healthy {
			status = "critical"
		}
		out = append(out, ServiceHealth{
			ServiceName: serviceName,
			Node:        "local",
			Address:     inst.IP,
			Port:        inst.Port,
			Status:      status,
		})
	}
	return out, nil
}

// ServiceInstances returns model.ServiceInstance entries for all running instances
// of a service, suitable for the service manifest.
func (e *Engine) ServiceInstances(serviceName string) ([]model.ServiceInstance, error) {
	checks, err := e.ServiceHealthChecks(serviceName)
	if err != nil {
		return nil, err
	}
	out := make([]model.ServiceInstance, 0, len(checks))
	for _, check := range checks {
		out = append(out, model.ServiceInstance{
			Node:    check.Node,
			Address: check.Address,
			Port:    check.Port,
			Status:  check.Status,
		})
	}
	return out, nil
}

// ServiceAddress returns the address of a healthy instance for a service,
// suitable for cloudflared routing. Prefers passing instances.
func (e *Engine) ServiceAddress(serviceName string) (string, error) {
	checks, err := e.ServiceHealthChecks(serviceName)
	if err != nil {
		return "", err
	}

	// Prefer passing, fall back to any with an address
	for _, c := range checks {
		if c.Status == "passing" && c.Address != "" && c.Port > 0 {
			return formatAddr(c.Address, c.Port), nil
		}
	}
	for _, c := range checks {
		if c.Address != "" && c.Port > 0 {
			return formatAddr(c.Address, c.Port), nil
		}
	}
	return "", nil
}

func formatAddr(ip string, port int) string {
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}
