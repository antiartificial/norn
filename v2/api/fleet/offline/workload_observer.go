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
	EligibleForWorkload   bool
	FreeCPU, FreeMemoryMB int
}

type WorkloadService struct {
	AllocationID, Node, Namespace, Name, CheckName, CheckID, Status string
}

type WorkloadNamespaceRead struct {
	Name             string
	QueryIndexBefore uint64
	QueryIndexAfter  uint64
	AllocationCount  int
}

// WorkloadSnapshot is an atomic logical observation. A source must verify its
// Nomad ACL policy grants wildcard namespace read, read the namespace list,
// read allocations separately for each namespace, then repeat those reads
// and the namespace list. Both query indices for each list must agree.
// A successful list response alone is insufficient: Nomad may silently
// filter unauthorized namespaces. Consul entries must contain every check
// from the target service rather than only its passing subset.
type WorkloadSnapshot struct {
	ObservedAt               time.Time
	FullNamespaceAccess      bool
	ACLPolicySHA256          string
	NamespaceListIndexBefore uint64
	NamespaceListIndexAfter  uint64
	Namespaces               []string
	NamespaceReads           []WorkloadNamespaceRead
	Allocations              []WorkloadAllocation
	Nodes                    []WorkloadNode
	Services                 []WorkloadService
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
	if err != nil || ctx.Err() != nil || !s.FullNamespaceAccess || !hex64.MatchString(s.ACLPolicySHA256) ||
		s.NamespaceListIndexBefore == 0 || s.NamespaceListIndexAfter != s.NamespaceListIndexBefore || s.ObservedAt.IsZero() ||
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
	reads := map[string]WorkloadNamespaceRead{}
	for _, read := range s.NamespaceReads {
		if !namespaces[read.Name] || read.QueryIndexBefore == 0 || read.QueryIndexAfter != read.QueryIndexBefore ||
			read.AllocationCount < 0 || reads[read.Name].Name != "" {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		reads[read.Name] = read
	}
	if len(reads) != len(namespaces) {
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
		read := reads[alloc.Namespace]
		read.AllocationCount--
		reads[alloc.Namespace] = read
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
		if alloc.Job == "hello-norn-mysql" && alloc.Namespace == "default" && alloc.Status == "pending" {
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
	for _, read := range reads {
		if read.AllocationCount != 0 {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
	}
	requestCPU, requestMemory := 0, 0
	for _, alloc := range s.Allocations {
		if alloc.Job == "hello-norn-mysql" && alloc.Namespace == "default" && alloc.Status == "running" {
			if requestCPU == 0 {
				requestCPU, requestMemory = alloc.CPU, alloc.MemoryMB
			}
			if alloc.CPU <= 0 || alloc.MemoryMB <= 0 || alloc.CPU != requestCPU || alloc.MemoryMB != requestMemory {
				return WorkloadEvidence{}, ErrWorkloadObservation
			}
		}
	}
	// One replacement replica must fit on either survivor independently.
	for _, name := range []string{"app-01", "app-02"} {
		if !nodes[name].EligibleForWorkload || nodes[name].FreeCPU < requestCPU || nodes[name].FreeMemoryMB < requestMemory {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
	}
	serviceChecks := map[string]map[string]bool{}
	checkIDs := map[string]bool{}
	for _, entry := range s.Services {
		if entry.AllocationID == "" || entry.Namespace != "default" || entry.Name != "hello-norn-mysql-web" ||
			(entry.CheckName != "readyz" && entry.CheckName != "healthz") || entry.CheckID == "" ||
			checkIDs[entry.CheckID] || entry.Status != "passing" ||
			placements[entry.Node] != entry.AllocationID || serviceChecks[entry.AllocationID][entry.CheckName] {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
		if serviceChecks[entry.AllocationID] == nil {
			serviceChecks[entry.AllocationID] = map[string]bool{}
		}
		serviceChecks[entry.AllocationID][entry.CheckName] = true
		checkIDs[entry.CheckID] = true
	}
	if len(serviceChecks) != 2 || len(checkIDs) != 4 {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	for _, checks := range serviceChecks {
		if len(checks) != 2 || !checks["readyz"] || !checks["healthz"] {
			return WorkloadEvidence{}, ErrWorkloadObservation
		}
	}
	// Canonicalize unordered API list responses for a stable evidence digest.
	s.Namespaces = append([]string(nil), s.Namespaces...)
	s.NamespaceReads = append([]WorkloadNamespaceRead(nil), s.NamespaceReads...)
	s.Nodes = append([]WorkloadNode(nil), s.Nodes...)
	s.Allocations = append([]WorkloadAllocation(nil), s.Allocations...)
	s.Services = append([]WorkloadService(nil), s.Services...)
	sort.Strings(s.Namespaces)
	sort.Slice(s.NamespaceReads, func(i, j int) bool { return s.NamespaceReads[i].Name < s.NamespaceReads[j].Name })
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Name < s.Nodes[j].Name })
	sort.Slice(s.Allocations, func(i, j int) bool { return s.Allocations[i].ID < s.Allocations[j].ID })
	sort.Slice(s.Services, func(i, j int) bool {
		if s.Services[i].AllocationID != s.Services[j].AllocationID {
			return s.Services[i].AllocationID < s.Services[j].AllocationID
		}
		return s.Services[i].CheckName < s.Services[j].CheckName
	})
	raw, err := json.Marshal(s)
	if err != nil {
		return WorkloadEvidence{}, ErrWorkloadObservation
	}
	digest := sha256.Sum256(raw)
	return WorkloadEvidence{ObservedAt: s.ObservedAt, SHA256: hex.EncodeToString(digest[:]),
		AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true,
		SurvivingHeadroom: true, ServicePassing: true}, nil
}
