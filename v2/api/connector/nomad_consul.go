package connector

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"norn/v2/api/consul"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type NomadConsulConnector struct {
	Nomad  *nomad.Client
	Consul *consul.Client
}

func NewNomadConsul(n *nomad.Client, c *consul.Client) *NomadConsulConnector {
	return &NomadConsulConnector{Nomad: n, Consul: c}
}

func (c *NomadConsulConnector) Name() string { return NomadConsul }

func (c *NomadConsulConnector) Info(ctx context.Context) Info {
	available := c != nil && c.Nomad != nil && c.Consul != nil
	if available {
		available = c.Nomad.Healthy() == nil && c.Consul.Healthy() == nil
	}
	return Info{Name: NomadConsul, Scheduler: "nomad", Discovery: "consul", Runtime: "oci/docker-driver", Available: available, ProductionReady: true,
		Capabilities: []string{"regions", "multi-allocation", "canary", "cron", "batch", "logs", "exec", "health", "service-discovery", "acl-variable-files"}}
}

func (c *NomadConsulConnector) Validate(spec *model.InfraSpec, production bool) error {
	if c == nil || c.Nomad == nil {
		return fmt.Errorf("nomad connector is not configured")
	}
	return nil
}

func (c *NomadConsulConnector) Healthy(ctx context.Context) error {
	if c == nil || c.Nomad == nil || c.Consul == nil {
		return fmt.Errorf("nomad/consul connector is not configured")
	}
	if err := c.Nomad.Healthy(); err != nil {
		return fmt.Errorf("nomad: %w", err)
	}
	if err := c.Consul.Healthy(); err != nil {
		return fmt.Errorf("consul: %w", err)
	}
	return nil
}

func (c *NomadConsulConnector) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	if err := c.Validate(req.Spec, false); err != nil {
		return "", err
	}
	var firstEval string
	if serviceProcessCount(req.Spec, req.Region.Name) > 0 {
		job := nomad.TranslateForRegion(req.Spec, req.Image, req.Environment, req.Region)
		eval, err := c.Nomad.SubmitJobRegion(job, req.Region.NomadRegion)
		if err != nil {
			return "", err
		}
		firstEval = eval
	}
	for name, process := range req.Spec.Processes {
		if process.Schedule == "" || !req.Spec.ProcessRunsInRegion(process, req.Region.Name) {
			continue
		}
		job := nomad.TranslatePeriodicForRegion(req.Spec, name, process, req.Image, req.Environment, req.Region)
		eval, err := c.Nomad.SubmitJobRegion(job, req.Region.NomadRegion)
		if err != nil {
			return "", fmt.Errorf("periodic process %s: %w", name, err)
		}
		if firstEval == "" {
			firstEval = eval
		}
	}
	return firstEval, nil
}

func (c *NomadConsulConnector) Poll(ctx context.Context, app string, region model.ResolvedRegion) ([]Allocation, error) {
	values, err := c.Nomad.JobAllocationsRegion(app, region.NomadRegion)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]*nomad.NodeInfo)
	out := make([]Allocation, 0, len(values))
	for _, value := range values {
		if value.ClientStatus == "complete" || value.ClientStatus == "failed" || value.ClientStatus == "lost" {
			continue
		}
		allocation := Allocation{
			ID: shortID(value.ID), Process: value.TaskGroup, Status: value.ClientStatus,
			NodeID: shortID(value.NodeID), NodeName: shortID(value.NodeID),
			NodeProvider: "remote", NodeRegion: region.Name, Lifecycle: "active",
		}
		if value.DeploymentStatus != nil {
			allocation.Healthy = value.DeploymentStatus.Healthy
		}
		if node, ok := nodes[value.NodeID]; ok {
			applyNodeInfo(&allocation, node)
		} else if node, lookupErr := c.Nomad.NodeInfo(value.NodeID); lookupErr == nil {
			nodes[value.NodeID] = node
			applyNodeInfo(&allocation, node)
		}
		out = append(out, allocation)
	}
	return out, nil
}

func applyNodeInfo(allocation *Allocation, node *nomad.NodeInfo) {
	allocation.NodeName = node.Name
	allocation.NodeAddress = node.Address
	allocation.NodeProvider = node.Provider
	allocation.NodeRegion = node.Region
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func (c *NomadConsulConnector) WaitHealthy(ctx context.Context, app string, region model.ResolvedRegion, timeout time.Duration) error {
	return c.Nomad.WaitHealthyRegion(ctx, app, region.NomadRegion, timeout)
}

func (c *NomadConsulConnector) Promote(ctx context.Context, app string, region model.ResolvedRegion) error {
	return c.Nomad.PromoteDeploymentRegion(app, region.NomadRegion)
}

func (c *NomadConsulConnector) Fail(ctx context.Context, app string, region model.ResolvedRegion) error {
	return c.Nomad.FailDeploymentRegion(app, region.NomadRegion)
}

func (c *NomadConsulConnector) Status(ctx context.Context, app string) (string, error) {
	return c.Nomad.JobStatus(app)
}

func (c *NomadConsulConnector) Restart(ctx context.Context, app string) error {
	return c.Nomad.RestartJob(app)
}

func (c *NomadConsulConnector) Scale(ctx context.Context, app, process string, count int) error {
	return c.Nomad.ScaleJob(app, process, count)
}

func (c *NomadConsulConnector) ServiceHealth(ctx context.Context, service string) ([]ServiceHealth, error) {
	if c == nil || c.Consul == nil {
		return nil, fmt.Errorf("consul discovery is not configured")
	}
	values, err := c.Consul.ServiceHealthChecks(service)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceHealth, 0, len(values))
	for _, value := range values {
		out = append(out, ServiceHealth{ServiceName: value.ServiceName, ID: value.ID, AllocationID: value.AllocationID, Node: value.Node, Address: value.Address, Port: value.Port, Status: value.Status, Region: value.Region, NodePool: value.NodePool, PlacementVerified: value.PlacementVerified})
	}
	return out, nil
}

func (c *NomadConsulConnector) EndpointOrigin(ctx context.Context, spec *model.InfraSpec) (string, error) {
	processName, process, ok := endpointProcess(spec)
	if !ok {
		return "", fmt.Errorf("no port found in spec")
	}
	service := spec.App + "-" + processName
	if values, err := c.ServiceHealth(ctx, service); err == nil {
		for _, value := range values {
			if value.Status == "passing" && value.Address != "" && value.Port > 0 {
				return httpOrigin(value.Address, value.Port), nil
			}
		}
		for _, value := range values {
			if value.Address != "" && value.Port > 0 {
				return httpOrigin(value.Address, value.Port), nil
			}
		}
	}
	allocations, err := c.Poll(ctx, spec.App, spec.ResolvedRegions()[0])
	if err != nil {
		return "", fmt.Errorf("poll allocations: %w", err)
	}
	if len(allocations) == 0 {
		return "", fmt.Errorf("no running allocations")
	}
	if allocations[0].NodeAddress == "" {
		return "", fmt.Errorf("allocation node address is unavailable")
	}
	return httpOrigin(allocations[0].NodeAddress, process.Port), nil
}

func httpOrigin(address string, port int) string {
	return "http://" + net.JoinHostPort(address, strconv.Itoa(port))
}

func (c *NomadConsulConnector) JobResourceUsage(ctx context.Context, app string) ([]ResourceUsage, error) {
	values, err := c.Nomad.JobResourceUsage(app)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceUsage, 0, len(values))
	for _, value := range values {
		out = append(out, ResourceUsage{TaskGroup: value.TaskGroup, MemoryUsageBytes: value.MemoryUsageBytes, MemoryMaxBytes: value.MemoryMaxBytes, CPUPercent: value.CPUPercent})
	}
	return out, nil
}

func (c *NomadConsulConnector) ClusterStats(ctx context.Context) (int, int, []UptimeEntry, error) {
	total, running, values, err := c.Nomad.ClusterStats()
	if err != nil {
		return 0, 0, nil, err
	}
	out := make([]UptimeEntry, 0, len(values))
	for _, value := range values {
		started, _ := time.Parse(time.RFC3339, value.StartedAt)
		out = append(out, UptimeEntry{AllocationID: value.AllocID, App: value.JobID, Process: value.TaskGroup, Uptime: value.Uptime, NodeName: value.NodeName, StartedAt: started})
	}
	return total, running, out, nil
}

func (c *NomadConsulConnector) StreamLogs(ctx context.Context, app string, follow bool) (io.ReadCloser, error) {
	return c.Nomad.StreamLogs(app, follow)
}

func (c *NomadConsulConnector) ResolveExecTarget(ctx context.Context, app, allocation, process string) (string, string, error) {
	return c.Nomad.ResolveExecTarget(app, allocation, process)
}

func (c *NomadConsulConnector) ExecWebSocket(ctx context.Context, target, task string, command []string, conn *websocket.Conn) error {
	return c.Nomad.ExecWebSocket(target, task, command, conn)
}

func serviceProcessCount(spec *model.InfraSpec, region string) int {
	count := 0
	for _, process := range spec.Processes {
		if process.Schedule == "" && spec.ProcessRunsInRegion(process, region) {
			count++
		}
	}
	return count
}

func endpointProcess(spec *model.InfraSpec) (string, model.Process, bool) {
	if process, ok := spec.Processes["web"]; ok && process.Port > 0 {
		return "web", process, true
	}
	names := make([]string, 0, len(spec.Processes))
	for name := range spec.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if process := spec.Processes[name]; process.Port > 0 {
			return name, process, true
		}
	}
	return "", model.Process{}, false
}
