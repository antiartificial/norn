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
	return WorkloadSnapshot{ObservedAt: now, FullNamespaceAccess: true,
		Namespaces: []string{"default", "system"},
		Nodes: []WorkloadNode{{Name: "app-01", ID: "node-1", Status: "ready", FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-02", ID: "node-2", Status: "ready", FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-03", ID: "node-3", Status: "ready", FreeCPU: 400, FreeMemoryMB: 512}},
		Allocations: []WorkloadAllocation{{ID: "alloc-1", Namespace: "default", Job: "hello-norn-mysql", Node: "app-01", Status: "running", CPU: 200, MemoryMB: 128},
			{ID: "alloc-2", Namespace: "default", Job: "hello-norn-mysql", Node: "app-02", Status: "running", CPU: 200, MemoryMB: 128}},
		Services: []WorkloadService{{AllocationID: "alloc-1", Node: "app-01", Status: "passing"}, {AllocationID: "alloc-2", Node: "app-02", Status: "passing"}}}
}

func TestObserveWorkloadFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	valid := validWorkloadSnapshot(now)
	got, err := ObserveWorkload(context.Background(), fixedWorkloadSource{snapshot: valid}, now)
	if err != nil || !got.AllNamespacesInspected || !got.App03Empty || !got.SurvivingPlacement || !got.SurvivingHeadroom || !got.ServicePassing || len(got.SHA256) != 64 {
		t.Fatalf("valid read-only observation: %+v %v", got, err)
	}
	checks := map[string]func(*WorkloadSnapshot){
		"filtered ACL":        func(s *WorkloadSnapshot) { s.FullNamespaceAccess = false },
		"missing namespace":   func(s *WorkloadSnapshot) { s.Namespaces = nil },
		"stale":               func(s *WorkloadSnapshot) { s.ObservedAt = now.Add(-31 * time.Second) },
		"duplicate namespace": func(s *WorkloadSnapshot) { s.Namespaces = append(s.Namespaces, "default") },
		"foreign allocation namespace": func(s *WorkloadSnapshot) {
			s.Allocations = append(s.Allocations, WorkloadAllocation{ID: "hidden", Namespace: "other", Node: "app-03", Status: "running"})
		},
		"app03 occupied": func(s *WorkloadSnapshot) {
			s.Allocations = append(s.Allocations, WorkloadAllocation{ID: "other", Namespace: "system", Node: "app-03", Status: "pending"})
		},
		"replica moved":        func(s *WorkloadSnapshot) { s.Allocations[1].Node = "app-03" },
		"low headroom":         func(s *WorkloadSnapshot) { s.Nodes[0].FreeCPU = 199 },
		"missing service":      func(s *WorkloadSnapshot) { s.Services = s.Services[:1] },
		"failing service":      func(s *WorkloadSnapshot) { s.Services[0].Status = "critical" },
		"duplicate allocation": func(s *WorkloadSnapshot) { s.Allocations = append(s.Allocations, s.Allocations[0]) },
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
