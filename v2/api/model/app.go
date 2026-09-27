package model

// AppStatus represents the status of a discovered app backed by Nomad.
type AppStatus struct {
	Spec              *InfraSpec        `json:"spec"`
	NomadStatus       string            `json:"nomadStatus"` // running, pending, dead
	Healthy           bool              `json:"healthy"`
	Allocations       []Allocation      `json:"allocations"`
	AllocationSummary AllocationSummary `json:"allocationSummary"`
}

// Allocation represents a Nomad task allocation.
type Allocation struct {
	ID           string `json:"id"`
	TaskGroup    string `json:"taskGroup"`
	Status       string `json:"status"`    // running, pending, complete, failed
	Lifecycle    string `json:"lifecycle"` // active or retained
	Healthy      *bool  `json:"healthy,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	NodeAddress  string `json:"nodeAddress,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	NodeProvider string `json:"nodeProvider,omitempty"` // local, do, hz, remote
	NodeRegion   string `json:"nodeRegion,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
}

// AllocationSummary separates live capacity from Nomad's retained allocation
// history so clients do not mistake completed allocations for running instances.
type AllocationSummary struct {
	Running   int                               `json:"running"`
	Active    int                               `json:"active"`
	Retained  int                               `json:"retained"`
	Total     int                               `json:"total"`
	ByProcess map[string]ProcessAllocationCount `json:"byProcess,omitempty"`
	ByStatus  map[string]int                    `json:"byStatus,omitempty"`
}

type ProcessAllocationCount struct {
	Running  int `json:"running"`
	Active   int `json:"active"`
	Retained int `json:"retained"`
	Total    int `json:"total"`
}

// ProcessScaleStatus separates declared source intent, accepted control-plane
// intent, and Nomad's observed scale projection for one process and region.
type ProcessScaleStatus struct {
	Region       string `json:"region"`
	NomadRegion  string `json:"nomadRegion"`
	Process      string `json:"process"`
	Declared     int    `json:"declared"`
	Desired      int    `json:"desired"`
	IntentSource string `json:"intentSource"`
	NomadPresent bool   `json:"nomadPresent"`
	NomadDesired *int   `json:"nomadDesired,omitempty"`
	Placed       *int   `json:"placed,omitempty"`
	Running      *int   `json:"running,omitempty"`
	Healthy      *int   `json:"healthy,omitempty"`
}
