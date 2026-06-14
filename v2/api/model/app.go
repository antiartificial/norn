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
	Status       string `json:"status"` // running, pending, complete, failed
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
	Running    int                               `json:"running"`
	Active     int                               `json:"active"`
	Retained   int                               `json:"retained"`
	Total      int                               `json:"total"`
	ByProcess  map[string]ProcessAllocationCount `json:"byProcess,omitempty"`
	ByStatus   map[string]int                    `json:"byStatus,omitempty"`
}

type ProcessAllocationCount struct {
	Running  int `json:"running"`
	Active   int `json:"active"`
	Retained int `json:"retained"`
	Total    int `json:"total"`
}
