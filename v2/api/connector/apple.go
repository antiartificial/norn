package connector

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/gorilla/websocket"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

type AppleConnector struct {
	Engine *engine.Engine
}

func NewApple(eng *engine.Engine) *AppleConnector { return &AppleConnector{Engine: eng} }

func (c *AppleConnector) Name() string { return AppleContainer }

func (c *AppleConnector) Info(ctx context.Context) Info {
	available := c != nil && c.Engine != nil && c.Engine.Healthy() == nil
	return Info{Name: AppleContainer, Scheduler: "norn-local", Discovery: "norn-local", Runtime: "apple-container", Available: available,
		ProductionReady: false, LocalOnly: true,
		Capabilities: []string{"local-lifecycle", "multi-allocation-workers", "logs", "exec", "norn-health-probes", "restart-reconciliation"},
		Limitations:  []string{"development only", "single macOS host", "no regional placement", "no native OCI healthcheck enforcement", "endpoint canaries require an external local ingress"}}
}

func (c *AppleConnector) Validate(spec *model.InfraSpec, production bool) error {
	if production {
		return fmt.Errorf("apple-container connector is development-only")
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("apple-container connector requires macOS")
	}
	if c == nil || c.Engine == nil {
		return fmt.Errorf("apple-container connector is not configured")
	}
	regions := spec.ResolvedRegions()
	if len(regions) != 1 || regions[0].Name != "local" {
		return fmt.Errorf("apple-container connector supports only the implicit local region")
	}
	for name, process := range spec.Processes {
		if len(engine.ContainerName(spec.App, name, 0))+len("-new") > 63 {
			return fmt.Errorf("process %s produces an Apple container name longer than 63 characters", name)
		}
		if process.Schedule != "" || process.Function != nil {
			return fmt.Errorf("process %s requires scheduled or batch execution, which is not yet supported by the apple-container connector", name)
		}
		count := 1
		if process.Scaling != nil && process.Scaling.Min > 0 {
			count = process.Scaling.Min
		}
		if process.Port > 0 && count > 1 {
			return fmt.Errorf("process %s publishes a port and cannot use multiple local allocations without an external local ingress", name)
		}
		if process.Canary != nil && process.Canary.Count > 0 {
			return fmt.Errorf("process %s uses canary deployment, which is not yet supported by the apple-container connector", name)
		}
	}
	if err := c.Engine.Healthy(); err != nil {
		return fmt.Errorf("apple-container runtime is unavailable: %w", err)
	}
	return nil
}

func (c *AppleConnector) Healthy(ctx context.Context) error {
	if c == nil || c.Engine == nil {
		return fmt.Errorf("apple-container connector is not configured")
	}
	return c.Engine.Healthy()
}

func (c *AppleConnector) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	if err := c.Validate(req.Spec, false); err != nil {
		return "", err
	}
	if err := c.Engine.SubmitJob(ctx, req.Spec, req.Image, req.Environment); err != nil {
		return "", err
	}
	return "apple-container/" + req.DeploymentID, nil
}

func (c *AppleConnector) Poll(ctx context.Context, app string, region model.ResolvedRegion) ([]Allocation, error) {
	values, err := c.Engine.PollInstances(app)
	if err != nil {
		return nil, err
	}
	out := make([]Allocation, 0, len(values))
	for _, value := range values {
		allocation := value.ToAllocation()
		out = append(out, Allocation{ID: allocation.ID, Process: allocation.TaskGroup, Status: allocation.Status,
			Healthy: allocation.Healthy, NodeID: allocation.NodeID, NodeName: allocation.NodeName,
			NodeAddress: allocation.NodeAddress, NodeProvider: "local", NodeRegion: "local",
			StartedAt: allocation.StartedAt, Lifecycle: allocation.Lifecycle})
	}
	return out, nil
}

func (c *AppleConnector) WaitHealthy(ctx context.Context, app string, region model.ResolvedRegion, timeout time.Duration) error {
	return c.Engine.WaitHealthy(ctx, app, timeout)
}

func (c *AppleConnector) Promote(ctx context.Context, app string, region model.ResolvedRegion) error {
	return c.Engine.PromoteDeployment(ctx, app)
}

func (c *AppleConnector) Fail(ctx context.Context, app string, region model.ResolvedRegion) error {
	return c.Engine.FailDeployment(ctx, app)
}

func (c *AppleConnector) Status(ctx context.Context, app string) (string, error) {
	return c.Engine.JobStatus(app)
}

func (c *AppleConnector) Restart(ctx context.Context, app string) error {
	return c.Engine.RestartJob(ctx, app)
}

func (c *AppleConnector) Scale(ctx context.Context, app, process string, count int) error {
	return c.Engine.ScaleJob(ctx, app, process, count)
}

func (c *AppleConnector) ServiceHealth(ctx context.Context, service string) ([]ServiceHealth, error) {
	if c == nil || c.Engine == nil {
		return nil, fmt.Errorf("apple-container connector is not configured")
	}
	values, err := c.Engine.ServiceHealthChecks(service)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceHealth, 0, len(values))
	for _, value := range values {
		out = append(out, ServiceHealth{ServiceName: value.ServiceName, Node: value.Node, Address: value.Address, Port: value.Port, Status: value.Status})
	}
	return out, nil
}

func (c *AppleConnector) EndpointOrigin(ctx context.Context, spec *model.InfraSpec) (string, error) {
	_, process, ok := endpointProcess(spec)
	if !ok {
		return "", fmt.Errorf("no port found in spec")
	}
	hostPort := process.HostPort
	if hostPort == 0 {
		hostPort = process.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d", hostPort), nil
}

func (c *AppleConnector) JobResourceUsage(ctx context.Context, app string) ([]ResourceUsage, error) {
	values, err := c.Engine.JobResourceUsage(ctx, app)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceUsage, 0, len(values))
	for _, value := range values {
		out = append(out, ResourceUsage{TaskGroup: value.TaskGroup, MemoryUsageBytes: value.MemoryUsageBytes, MemoryMaxBytes: value.MemoryMaxBytes, CPUPercent: value.CPUPercent})
	}
	return out, nil
}

func (c *AppleConnector) ClusterStats(ctx context.Context) (int, int, []UptimeEntry, error) {
	total, running, values, err := c.Engine.ClusterStats()
	if err != nil {
		return 0, 0, nil, err
	}
	out := make([]UptimeEntry, 0, len(values))
	for _, value := range values {
		out = append(out, UptimeEntry{AllocationID: engine.ShortID(value.ContainerName), App: value.App, Process: value.Process, Uptime: value.Uptime, NodeName: c.Engine.NodeInfo().Name, StartedAt: value.StartedAt})
	}
	return total, running, out, nil
}

func (c *AppleConnector) StreamLogs(ctx context.Context, app string, follow bool) (io.ReadCloser, error) {
	return c.Engine.StreamLogs(app, follow)
}

func (c *AppleConnector) ResolveExecTarget(ctx context.Context, app, allocation, process string) (string, string, error) {
	if allocation == "" {
		target, err := c.Engine.FindRunningInstance(app, process)
		return target, "", err
	}
	return allocation, "", nil
}

func (c *AppleConnector) ExecWebSocket(ctx context.Context, target, task string, command []string, conn *websocket.Conn) error {
	return c.Engine.ExecWebSocket(target, command, conn)
}
