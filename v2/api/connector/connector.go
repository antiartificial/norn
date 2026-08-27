// Package connector defines the workload-control boundary used by Norn.
//
// The production connector delegates scheduling and discovery to Nomad and
// Consul. The Apple connector delegates local lifecycle to Apple's `container`
// CLI. Keeping this boundary above the container image builder prevents a
// runtime selection from silently replacing the production control plane.
package connector

import (
	"context"
	"io"
	"time"

	"github.com/gorilla/websocket"

	"norn/v2/api/model"
)

const (
	NomadConsul    = "nomad-consul"
	AppleContainer = "apple-container"
)

type Info struct {
	Name            string   `json:"name"`
	Scheduler       string   `json:"scheduler"`
	Discovery       string   `json:"discovery"`
	Runtime         string   `json:"runtime"`
	Available       bool     `json:"available"`
	ProductionReady bool     `json:"productionReady"`
	LocalOnly       bool     `json:"localOnly"`
	Capabilities    []string `json:"capabilities"`
	Limitations     []string `json:"limitations,omitempty"`
}

type Allocation struct {
	ID           string
	Process      string
	Status       string
	Healthy      *bool
	NodeID       string
	NodeName     string
	NodeAddress  string
	NodeProvider string
	NodeRegion   string
	StartedAt    string
	Lifecycle    string
}

type ServiceHealth struct {
	ServiceName       string
	ID                string
	AllocationID      string
	Node              string
	Address           string
	Port              int
	Status            string
	Region            string
	NodePool          string
	PlacementVerified bool
}

type ResourceUsage struct {
	TaskGroup        string
	MemoryUsageBytes uint64
	MemoryMaxBytes   uint64
	CPUPercent       float64
}

type UptimeEntry struct {
	AllocationID string    `json:"allocId,omitempty"`
	App          string    `json:"app,omitempty"`
	Process      string    `json:"process,omitempty"`
	Uptime       string    `json:"uptime"`
	NodeName     string    `json:"nodeName,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
}

type SubmitRequest struct {
	Spec         *model.InfraSpec
	Image        string
	Environment  map[string]string
	Region       model.ResolvedRegion
	DeploymentID string
}

// Connector is the scheduler/runtime contract used by deployments and the
// common application-control API. Backend-specific cluster administration
// remains outside this interface and is exposed only when that backend is
// active.
type Connector interface {
	Name() string
	Info(context.Context) Info
	Validate(*model.InfraSpec, bool) error
	Healthy(context.Context) error
	Submit(context.Context, SubmitRequest) (string, error)
	Poll(context.Context, string, model.ResolvedRegion) ([]Allocation, error)
	WaitHealthy(context.Context, string, model.ResolvedRegion, time.Duration) error
	Promote(context.Context, string, model.ResolvedRegion) error
	Fail(context.Context, string, model.ResolvedRegion) error
	Status(context.Context, string) (string, error)
	Restart(context.Context, string) error
	Scale(context.Context, string, string, int) error
	ServiceHealth(context.Context, string) ([]ServiceHealth, error)
	EndpointOrigin(context.Context, *model.InfraSpec) (string, error)
	JobResourceUsage(context.Context, string) ([]ResourceUsage, error)
	ClusterStats(context.Context) (int, int, []UptimeEntry, error)
	StreamLogs(context.Context, string, bool) (io.ReadCloser, error)
	ResolveExecTarget(context.Context, string, string, string) (string, string, error)
	ExecWebSocket(context.Context, string, string, []string, *websocket.Conn) error
}

func ToModelAllocation(value Allocation) model.Allocation {
	return model.Allocation{
		ID: value.ID, TaskGroup: value.Process, Status: value.Status,
		Healthy: value.Healthy, NodeID: value.NodeID, NodeName: value.NodeName,
		NodeAddress: value.NodeAddress, NodeProvider: value.NodeProvider,
		NodeRegion: value.NodeRegion, StartedAt: value.StartedAt,
		Lifecycle: value.Lifecycle,
	}
}
