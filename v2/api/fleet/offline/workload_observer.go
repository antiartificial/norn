package offline

// This package-level collector validates a read-only Nomad/Consul snapshot.
// It is not an effect-grant observer: a production source must independently
// establish ACL coverage and fetch the snapshot directly from both services.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var ErrWorkloadObservation = errors.New("Fleet workload observation is incomplete or ambiguous")

type WorkloadAllocation struct {
	ID, Namespace, Job, Node, Status string
	CPU, MemoryMB                    int
}

type WorkloadNode struct {
	Name, ID, Status      string
	FreeCPU, FreeMemoryMB int
}

type WorkloadService struct {
	AllocationID, Node, Status string
}

// WorkloadSnapshot is an atomic logical observation. FullNamespaceAccess
// must come from an independently checked Nomad ACL policy, not from a
// successful list response: Nomad can silently filter unauthorized rows.
type WorkloadSnapshot struct {
	ObservedAt          time.Time
	FullNamespaceAccess bool
	Namespaces          []string
	Allocations         []WorkloadAllocation
	Nodes               []WorkloadNode
	Services            []WorkloadService
}

type WorkloadSnapshotSource interface {
	Snapshot(context.Context) (WorkloadSnapshot, error)
}

type WorkloadEvidence struct {
	ObservedAt                                             time.Time
	SHA256                                                 string
	AllNamespacesInspected, App03Empty, SurvivingPlacement bool
	SurvivingHeadroom, ServicePassing                      bool
}

// ObserveWorkload checks every namespace, not only the default workload.
// It requires exactly two running replicas, one on each survivor, and
// passing Consul entries tied to those exact allocations. It never mutates
// Nomad or Consul and cannot authorize a provider effect on its own.
func ObserveWorkload(ctx context.Context, source WorkloadSnapshotSource, now time.Time) (WorkloadEvidence, error) {
	if source == nil || ctx.Err() != nil {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	s, err := source.Snapshot(ctx)
	if err != nil || ctx.Err() != nil || !s.FullNamespaceAccess || s.ObservedAt.IsZero() ||
		now.Before(s.ObservedAt) || now.Sub(s.ObservedAt) > 30*time.Second || len(s.Namespaces) == 0 {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	namespaces := map[string]bool{}
	for _, name := range s.Namespaces {
		if name == "" || name == "*" || namespaces[name] {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		namespaces[name] = true
	}
	if !namespaces["default"] {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	nodes := map[string]WorkloadNode{}
	ids := map[string]bool{}
	for _, node := range s.Nodes {
		if node.Name == "" || node.ID == "" || nodes[node.Name].Name != "" || ids[node.ID] ||
			node.Status != "ready" || node.FreeCPU < 0 || node.FreeMemoryMB < 0 {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		nodes[node.Name], ids[node.ID] = node, true
	}
	for _, name := range []string{"app-01", "app-02", "app-03"} {
		if nodes[name].Name != name {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
	}
	allocIDs := map[string]bool{}
	placements := map[string]string{}
	for _, alloc := range s.Allocations {
		if alloc.ID == "" || allocIDs[alloc.ID] || !namespaces[alloc.Namespace] || nodes[alloc.Node].Name == "" ||
			alloc.Status == "" || alloc.CPU < 0 || alloc.MemoryMB < 0 {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		allocIDs[alloc.ID] = true
		// Terminal allocations do not consume the current placement, but an
		// unknown status is unsafe because it may still be live.
		switch alloc.Status {
		case "running", "pending":
			if alloc.Node == "app-03" {
				return WorkloadEvidence{}, ErrWorkloadObservation
			}
		case "complete", "failed", "lost":
		default:
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		if alloc.Job == "hello-norn-mysql" && alloc.Namespace == "default" && alloc.Status == "running" {
			if alloc.Node != "app-01" && alloc.Node != "app-02" || placements[alloc.Node] != "" {
				return WorkloadEvidence{}, ErrWorkloadObservation
			}
			placements[alloc.Node] = alloc.ID
		}
	}
	if len(placements) != 2 {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	// One replacement replica must fit on either survivor independently.
	for _, name := range []string{"app-01", "app-02"} {
		if nodes[name].FreeCPU < 200 || nodes[name].FreeMemoryMB < 128 {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
	}
	serviceIDs := map[string]bool{}
	for _, entry := range s.Services {
		if entry.AllocationID == "" || entry.Status != "passing" ||
			placements[entry.Node] != entry.AllocationID || serviceIDs[entry.AllocationID] {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		serviceIDs[entry.AllocationID] = true
	}
	if len(serviceIDs) != 2 {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	// Canonicalize unordered API list responses for a stable evidence digest.
	sort.Strings(s.Namespaces)
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Name < s.Nodes[j].Name })
	sort.Slice(s.Allocations, func(i, j int) bool { return s.Allocations[i].ID < s.Allocations[j].ID })
	sort.Slice(s.Services, func(i, j int) bool { return s.Services[i].AllocationID < s.Services[j].AllocationID })
	raw, err := json.Marshal(s)
	if err != nil {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	digest := sha256.Sum256(raw)
	return WorkloadEvidence{ObservedAt: s.ObservedAt, SHA256: hex.EncodeToString(digest[:]),
		AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true,
		SurvivingHeadroom: true, ServicePassing: true}, nil
}
