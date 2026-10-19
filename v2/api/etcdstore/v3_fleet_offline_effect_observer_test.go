package etcdstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type effectEpochFixture struct {
	epoch int64
	err   error
}

func (f effectEpochFixture) ReadOfflineEffectEpoch(context.Context) (int64, error) {
	return f.epoch, f.err
}

type effectWorkloadFixture struct {
	snapshot offline.WorkloadSnapshot
	err      error
}

type effectWorkloadSourceFunc func(context.Context) (offline.WorkloadSnapshot, error)

func (f effectWorkloadSourceFunc) Snapshot(ctx context.Context) (offline.WorkloadSnapshot, error) {
	return f(ctx)
}

func (f effectWorkloadFixture) Snapshot(context.Context) (offline.WorkloadSnapshot, error) {
	return f.snapshot, f.err
}

func effectWorkloadSnapshot(now time.Time) offline.WorkloadSnapshot {
	return offline.WorkloadSnapshot{ObservedAt: now, FullNamespaceAccess: true,
		ACLPolicySHA256:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NamespaceListIndexBefore: 10, NamespaceListIndexAfter: 10,
		Namespaces: []string{"default"}, NamespaceReads: []offline.WorkloadNamespaceRead{{Name: "default", QueryIndexBefore: 1, QueryIndexAfter: 1, AllocationCount: 2}},
		Nodes: []offline.WorkloadNode{{Name: "app-01", ID: "n1", Status: "ready", EligibleForWorkload: true, FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-02", ID: "n2", Status: "ready", EligibleForWorkload: true, FreeCPU: 200, FreeMemoryMB: 128},
			{Name: "app-03", ID: "n3", Status: "ready"}},
		Allocations: []offline.WorkloadAllocation{{ID: "a1", Namespace: "default", Job: "hello-norn-mysql", Node: "app-01", Status: "running", CPU: 200, MemoryMB: 128},
			{ID: "a2", Namespace: "default", Job: "hello-norn-mysql", Node: "app-02", Status: "running", CPU: 200, MemoryMB: 128}},
		Services: []offline.WorkloadService{
			{AllocationID: "a1", Node: "app-01", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "readyz", CheckID: "a1-ready", Status: "passing"},
			{AllocationID: "a1", Node: "app-01", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "healthz", CheckID: "a1-health", Status: "passing"},
			{AllocationID: "a2", Node: "app-02", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "readyz", CheckID: "a2-ready", Status: "passing"},
			{AllocationID: "a2", Node: "app-02", Namespace: "default", Name: "hello-norn-mysql-web", CheckName: "healthz", CheckID: "a2-health", Status: "passing"}}}
}

func TestOfflineEffectObserverCompositionFailsClosed(t *testing.T) {
	_, b, a, now := offlineEffectFixture(t)
	state := offlineBackendReadFor(b)
	rows := make([]offlineProviderDroplet, 0, len(b.ProviderNodes))
	for _, node := range b.ProviderNodes {
		rows = append(rows, offlineProviderDroplet{ID: node.ProviderID, Name: node.Name, Status: "active", VPCUUID: "vpc", ProjectID: "project", Tags: []string{"norn-work-staging"}})
	}
	origin := &offlineOriginFixture{want: b.SourceCommit}
	d := offlineEffectObserverDependencies{origin: origin, provider: providerFixture{account: b.ProviderAccountUUID, droplets: rows},
		topology: offlineProviderTopology{AccountUUID: b.ProviderAccountUUID, ProjectID: "project", VPCUUID: "vpc", RequiredTags: []string{"norn-work-staging"}},
		backend:  state, workload: effectWorkloadFixture{snapshot: effectWorkloadSnapshot(now)}, epoch: effectEpochFixture{epoch: 1}, clock: func() time.Time { return now }}
	observer, err := newOfflineEffectReobserver(d)
	if err != nil {
		t.Fatal(err)
	}
	o, err := observer.reobserveEffect(context.Background(), b, a)
	if err != nil || offline.ValidateEffectObservation(o, b, a, 1, now) != nil {
		t.Fatalf("composed proof: %+v %v", o, err)
	}
	workloadOnly, err := offline.ObserveWorkload(context.Background(), d.workload, now)
	if err != nil || o.EvidenceSHA256 == workloadOnly.SHA256 {
		t.Fatal("composite digest omitted authenticated slices")
	}
	changed := d
	changed.provider = providerFixture{account: b.ProviderAccountUUID, droplets: append(append([]offlineProviderDroplet(nil), rows...), offlineProviderDroplet{ID: "999", Name: "unrelated", Status: "active", ProjectID: "elsewhere", VPCUUID: "elsewhere"})}
	changedObserver, err := newOfflineEffectReobserver(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedProof, err := changedObserver.reobserveEffect(context.Background(), b, a)
	if err != nil || changedProof.EvidenceSHA256 == o.EvidenceSHA256 {
		t.Fatal("provider receipt did not change composite digest")
	}
	advanced := now.Add(time.Second)
	clockCalls := 0
	changed = d
	changed.workload = effectWorkloadSourceFunc(func(context.Context) (offline.WorkloadSnapshot, error) { return effectWorkloadSnapshot(advanced), nil })
	changed.clock = func() time.Time { clockCalls++; return advanced }
	advancedObserver, err := newOfflineEffectReobserver(changed)
	if err != nil {
		t.Fatal(err)
	}
	advancedProof, err := advancedObserver.reobserveEffect(context.Background(), b, a)
	if err != nil || clockCalls != 1 || !advancedProof.ObservedAt.Equal(advanced) || offline.ValidateEffectObservation(advancedProof, b, a, 1, advanced) != nil {
		t.Fatalf("post-read timestamp rejected: %+v %v", advancedProof, err)
	}
	missing := map[string]func(*offlineEffectObserverDependencies){
		"origin":   func(d *offlineEffectObserverDependencies) { d.origin = nil },
		"provider": func(d *offlineEffectObserverDependencies) { d.provider = nil },
		"backend":  func(d *offlineEffectObserverDependencies) { d.backend = nil },
		"workload": func(d *offlineEffectObserverDependencies) { d.workload = nil },
		"epoch":    func(d *offlineEffectObserverDependencies) { d.epoch = nil },
		"clock":    func(d *offlineEffectObserverDependencies) { d.clock = nil },
	}
	for name, remove := range missing {
		t.Run("missing "+name, func(t *testing.T) {
			broken := d
			remove(&broken)
			if _, err := newOfflineEffectReobserver(broken); !errors.Is(err, offline.ErrEffectProofMissing) {
				t.Fatal(err)
			}
		})
	}
	faults := map[string]func(*offlineEffectObserverDependencies){
		"origin mismatch": func(d *offlineEffectObserverDependencies) { d.origin = &offlineOriginFixture{want: "wrong"} },
		"provider outage": func(d *offlineEffectObserverDependencies) {
			d.provider = providerFixture{account: "wrong", droplets: rows}
		},
		"backend stale": func(d *offlineEffectObserverDependencies) { f := state; f.version = "stale"; d.backend = f },
		"workload hidden": func(d *offlineEffectObserverDependencies) {
			s := effectWorkloadSnapshot(now)
			s.FullNamespaceAccess = false
			d.workload = effectWorkloadFixture{snapshot: s}
		},
		"epoch stale": func(d *offlineEffectObserverDependencies) { d.epoch = effectEpochFixture{epoch: 2} },
	}
	for name, inject := range faults {
		t.Run(name, func(t *testing.T) {
			broken := d
			inject(&broken)
			o, err := newOfflineEffectReobserver(broken)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := o.reobserveEffect(context.Background(), b, a)
			if err == nil && offline.ValidateEffectObservation(proof, b, a, 1, now) == nil {
				t.Fatal("fault accepted")
			}
		})
	}
}
