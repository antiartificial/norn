package etcdstore

import (
	"context"
	"reflect"
	"testing"
)

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
}
