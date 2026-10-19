package offline

import (
	"context"
	"testing"
	"time"
)

type fixedWorkloadSource struct {
	snapshot WorkloadSnapshot
	err      error
}

func (s fixedWorkloadSource) Snapshot(context.Context) (WorkloadSnapshot, error) {
	return s.snapshot, s.err
}

func validWorkloadSnapshot(now time.Time) WorkloadSnapshot {
	return WorkloadSnapshot{ObservedAt: now, FullNamespaceAccess: true, ACLPolicySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NamespaceListIndexBefore: 10, NamespaceListIndexAfter: 10,
		Namespaces:     []string{"default", "system"},
		NamespaceReads: []WorkloadNamespaceRead{{Name: "default", QueryIndexBefore: 1, QueryIndexAfter: 1, AllocationCount: 2}, {Name: "system", QueryIndexBefore: 2, QueryIndexAfter: 2, AllocationCount: 0}},
		Nodes: []WorkloadNode{{Name: "app-01", ID: "node-1", Status: "ready", EligibleForWorkload: true, FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-02", ID: "node-2", Status: "ready", EligibleForWorkload: true, FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-03", ID: "node-3", Status: "ready", FreeCPU: 400, FreeMemoryMB: 512}},
		Allocations: []WorkloadAllocation{{ID: "alloc-1", Namespace: "default", Job: "hello-norn-mysql", Node: "app-01", Status: "running", CPU: 200, MemoryMB: 128},
			{ID: "alloc-2", Namespace: "default", Job: "hello-norn-mysql", Node: "app-02", Status: "running", CPU: 200, MemoryMB: 128}},
		Services: []WorkloadService{
			{AllocationID: "alloc-1", Node: "app-01", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "readyz", CheckID: "alloc-1-readyz", Status: "passing"},
			{AllocationID: "alloc-1", Node: "app-01", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "healthz", CheckID: "alloc-1-healthz", Status: "passing"},
			{AllocationID: "alloc-2", Node: "app-02", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "readyz", CheckID: "alloc-2-readyz", Status: "passing"},
			{AllocationID: "alloc-2", Node: "app-02", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "healthz", CheckID: "alloc-2-healthz", Status: "passing"}}}
}

func TestObserveWorkloadFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	valid := validWorkloadSnapshot(now)
	got, err := ObserveWorkload(context.Background(), fixedWorkloadSource{snapshot: valid}, now)
	if err != nil || !got.AllNamespacesInspected || !got.App03Empty || !got.SurvivingPlacement || !got.SurvivingHeadroom || !got.ServicePassing || len(got.SHA256) != 64 {
		t.Fatalf("valid read-only observation: %+v %v", got, err)
	}
	checks := map[string]func(*WorkloadSnapshot){
		"filtered ACL":                             func(s *WorkloadSnapshot) { s.FullNamespaceAccess = false },
		"missing ACL digest":                       func(s *WorkloadSnapshot) { s.ACLPolicySHA256 = "" },
		"namespace list changed":                   func(s *WorkloadSnapshot) { s.NamespaceListIndexAfter++ },
		"missing namespace read":                   func(s *WorkloadSnapshot) { s.NamespaceReads = s.NamespaceReads[:1] },
		"zero namespace read index":                func(s *WorkloadSnapshot) { s.NamespaceReads[0].QueryIndexBefore = 0 },
		"namespace allocation changed during scan": func(s *WorkloadSnapshot) { s.NamespaceReads[0].QueryIndexAfter++ },
		"truncated allocation list":                func(s *WorkloadSnapshot) { s.Allocations = s.Allocations[:1] },
		"missing namespace":                        func(s *WorkloadSnapshot) { s.Namespaces = nil },
		"stale":                                    func(s *WorkloadSnapshot) { s.ObservedAt = now.Add(-31 * time.Second) },
		"duplicate namespace":                      func(s *WorkloadSnapshot) { s.Namespaces = append(s.Namespaces, "default") },
		"foreign allocation namespace": func(s *WorkloadSnapshot) {
			s.Allocations = append(s.Allocations, WorkloadAllocation{ID: "hidden", Namespace: "other", Node: "app-03", Status: "running"})
		},
		"app03 occupied": func(s *WorkloadSnapshot) {
			s.Allocations = append(s.Allocations, WorkloadAllocation{ID: "other", Namespace: "system", Node: "app-03", Status: "pending"})
		},
		"replica moved":            func(s *WorkloadSnapshot) { s.Allocations[1].Node = "app-03" },
		"low headroom":             func(s *WorkloadSnapshot) { s.Nodes[0].FreeCPU = 199 },
		"ineligible survivor":      func(s *WorkloadSnapshot) { s.Nodes[0].EligibleForWorkload = false },
		"larger request":           func(s *WorkloadSnapshot) { s.Allocations[0].CPU = 201; s.Allocations[1].CPU = 201 },
		"unequal request":          func(s *WorkloadSnapshot) { s.Allocations[0].CPU = 201 },
		"pending workload":         func(s *WorkloadSnapshot) { s.Allocations[0].Status = "pending" },
		"missing service":          func(s *WorkloadSnapshot) { s.Services = s.Services[:1] },
		"missing readiness check":  func(s *WorkloadSnapshot) { s.Services = append(s.Services[:2], s.Services[3:]...) },
		"failing service":          func(s *WorkloadSnapshot) { s.Services[0].Status = "critical" },
		"duplicate service check":  func(s *WorkloadSnapshot) { s.Services[1].CheckName = "readyz" },
		"wrong service":            func(s *WorkloadSnapshot) { s.Services[0].Name = "other" },
		"wrong service namespace":  func(s *WorkloadSnapshot) { s.Services[0].Namespace = "system" },
		"missing check provenance": func(s *WorkloadSnapshot) { s.Services[0].CheckID = "" },
		"duplicate allocation":     func(s *WorkloadSnapshot) { s.Allocations = append(s.Allocations, s.Allocations[0]) },
	}
	for name, mutate := range checks {
		t.Run(name, func(t *testing.T) {
			s := validWorkloadSnapshot(now)
			mutate(&s)
			if _, err := ObserveWorkload(context.Background(), fixedWorkloadSource{snapshot: s}, now); err != ErrWorkloadObservation {
				t.Fatalf("unsafe observation accepted: %v", err)
			}
		})
	}
}

func TestObserveWorkloadDoesNotMutateSourceSlices(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s := validWorkloadSnapshot(now)
	s.Namespaces[0], s.Namespaces[1] = s.Namespaces[1], s.Namespaces[0]
	s.Allocations[0], s.Allocations[1] = s.Allocations[1], s.Allocations[0]
	if _, err := ObserveWorkload(context.Background(), fixedWorkloadSource{snapshot: s}, now); err != nil {
		t.Fatal(err)
	}
	if s.Namespaces[0] != "system" || s.Allocations[0].ID != "alloc-2" {
		t.Fatal("collector mutated source-owned slices")
	}
}
