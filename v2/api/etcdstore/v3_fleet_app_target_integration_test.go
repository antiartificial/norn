package etcdstore

import (
	"context"
	"reflect"
	"testing"
	"time"

	"norn/v2/api/store"
)

func TestWorkloadReleasePolicyCASIsSeparateFromFleetPlacement(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: "pilot", ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global",
		Datacenters: []string{"dc1"}, Generation: 1}
	targetRevision, err := adapter.ConfigureFleetAppTarget(ctx, target, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy := store.WorkloadReleasePolicy{App: "pilot", ControlEnvironment: "staging", FleetTargetGeneration: 1,
		SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: "11111111-1111-4111-8111-111111111111",
		ApprovedBy: "platform-operator", ApprovedAt: time.Now().UTC(), Mode: store.WorkloadReleasePolicyStatelessOverlap,
		MinReplicas: 2, MaxReplicas: 5, SurgeLimit: 2,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
	policyRevision, err := adapter.ConfigureWorkloadReleasePolicy(ctx, policy, 0)
	if err != nil || policyRevision <= targetRevision {
		t.Fatalf("policy revision=%d target revision=%d err=%v", policyRevision, targetRevision, err)
	}
	loaded, loadedRevision, err := adapter.CurrentWorkloadReleasePolicy(ctx, "pilot", "staging")
	if err != nil || loadedRevision != policyRevision || !reflect.DeepEqual(loaded, policy) {
		t.Fatalf("loaded policy=%+v revision=%d err=%v", loaded, loadedRevision, err)
	}
	changedTarget := target
	changedTarget.Generation = 2
	changedTarget.Region = "east"
	if _, err := adapter.ConfigureFleetAppTarget(ctx, changedTarget, targetRevision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.CurrentWorkloadReleasePolicy(ctx, "pilot", "staging"); err != ErrWorkloadReleasePolicyStale {
		t.Fatalf("policy bound to old placement generation returned %v", err)
	}
	policy.FleetTargetGeneration = 2
	policy.ApprovalID = "22222222-2222-4222-8222-222222222222"
	policyRevision, err = adapter.ConfigureWorkloadReleasePolicy(ctx, policy, policyRevision)
	if err != nil {
		t.Fatalf("reapprove policy for new placement: %v", err)
	}
	if _, _, err := adapter.CurrentWorkloadReleasePolicy(ctx, "pilot", "staging"); err != nil {
		t.Fatalf("new placement policy unavailable: %v", err)
	}
	if _, err := adapter.ConfigureWorkloadReleasePolicy(ctx, policy, policyRevision-1); err == nil {
		t.Fatal("stale policy revision was accepted")
	}
	if err := validateWorkloadReleasePolicy(store.WorkloadReleasePolicy{App: "pilot", ControlEnvironment: "staging", FleetTargetGeneration: 2,
		SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: policy.ApprovalID, ApprovedBy: policy.ApprovedBy, ApprovedAt: policy.ApprovedAt,
		Mode: policy.Mode, MinReplicas: 2, MaxReplicas: 5, SurgeLimit: 2,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {TargetWeight: 100}, {TargetWeight: 100}}}); err == nil {
		t.Fatal("policy without a mixed-version route step was accepted")
	}
}

func TestFleetAppTargetCASAndIdentityEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: "pilot", ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global",
		Datacenters: []string{"dc1", "dc2"}, Generation: 1}
	revision, err := adapter.putFleetAppTarget(ctx, target, 0)
	if err != nil || revision <= 0 {
		t.Fatalf("initial target revision=%d err=%v", revision, err)
	}
	loaded, gotRevision, err := adapter.loadFleetAppTarget(ctx, "pilot", "staging")
	if err != nil || !reflect.DeepEqual(loaded, target) || gotRevision != revision {
		t.Fatalf("loaded target=%+v revision=%d err=%v", loaded, gotRevision, err)
	}
	current, currentRevision, err := adapter.CurrentFleetAppTarget(ctx, "pilot", "staging")
	if err != nil || !reflect.DeepEqual(current, target) || currentRevision != revision {
		t.Fatalf("current target=%+v revision=%d err=%v", current, currentRevision, err)
	}
	if _, _, err := adapter.loadFleetAppTarget(ctx, "other", "staging"); err == nil {
		t.Fatal("unrelated app loaded the Fleet target")
	}
	if _, err := adapter.putFleetAppTarget(ctx, target, 0); err == nil {
		t.Fatal("duplicate initial target was accepted")
	}
	changed := target
	changed.Generation = 2
	changed.Cluster = "replacement"
	if _, err := adapter.putFleetAppTarget(ctx, changed, revision+1); err == nil {
		t.Fatal("stale expected revision was accepted")
	}
	nextRevision, err := adapter.putFleetAppTarget(ctx, changed, revision)
	if err != nil || nextRevision <= revision {
		t.Fatalf("replacement revision=%d err=%v", nextRevision, err)
	}
	if _, err := adapter.putFleetAppTarget(ctx, changed, revision); err == nil {
		t.Fatal("replayed replacement was accepted")
	}
	key := adapter.fleetAppTargetKey("pilot", "staging")
	if _, err := client.Put(ctx, key, `{"schemaVersion":"norn.fleet-app-target/v1","app":"other","controlEnvironment":"staging","cluster":"replacement","fleetEnvironment":"staging/nyc3","region":"west","nomadRegion":"global","datacenters":["dc1","dc2"],"generation":2}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.loadFleetAppTarget(ctx, "pilot", "staging"); err == nil {
		t.Fatal("corrupt app target identity was accepted")
	}
	if _, _, err := adapter.CurrentFleetAppTarget(ctx, "pilot", "staging"); err == nil {
		t.Fatal("public target reader accepted a corrupt identity")
	}
}
