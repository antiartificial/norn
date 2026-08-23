package model

import "testing"

func TestRegionalPlacementDefaultsAndPinsSingletonWork(t *testing.T) {
	spec := &InfraSpec{Regions: map[string]RegionTarget{
		"ord": {NomadRegion: "us-central", Datacenters: []string{"ord1"}, TrafficWeight: regionTestInt(70)},
		"iad": {NomadRegion: "us-east", Datacenters: []string{"iad1"}, TrafficWeight: regionTestInt(30)},
	}, PrimaryRegion: "ord"}
	web := Process{}
	cron := Process{Schedule: "0 * * * *"}
	worker := Process{Singleton: true}
	if !spec.ProcessRunsInRegion(web, "ord") || !spec.ProcessRunsInRegion(web, "iad") {
		t.Fatal("ordinary process should run in all regions")
	}
	if !spec.ProcessRunsInRegion(cron, "ord") || spec.ProcessRunsInRegion(cron, "iad") {
		t.Fatal("scheduled process should default to primary")
	}
	if !spec.ProcessRunsInRegion(worker, "ord") || spec.ProcessRunsInRegion(worker, "iad") {
		t.Fatal("singleton should default to primary")
	}
}

func TestRegionalTrafficWeightDistinguishesOmittedFromZero(t *testing.T) {
	spec := &InfraSpec{Regions: map[string]RegionTarget{
		"active":  {},
		"standby": {TrafficWeight: regionTestInt(0)},
	}}
	regions := spec.ResolvedRegions()
	if regions[0].TrafficWeight != 100 || regions[1].TrafficWeight != 0 {
		t.Fatalf("expected omitted=100 and explicit zero=0, got %+v", regions)
	}
}

func TestValidateRejectsFixedHostPortWithPerRegionScaling(t *testing.T) {
	spec := &InfraSpec{App: "ingress", Processes: map[string]Process{
		"web": {Port: 8080, HostPort: 18080, Scaling: &Scaling{PerRegion: 2}},
	}}
	result := ValidateSpec(spec)
	if result.Valid {
		t.Fatalf("expected fixed host port with multiple regional allocations to fail: %+v", result.Findings)
	}
}

func regionTestInt(value int) *int { return &value }

func TestExplicitProcessRegionsOverrideDefaults(t *testing.T) {
	spec := &InfraSpec{Regions: map[string]RegionTarget{"ord": {}, "iad": {}}, PrimaryRegion: "ord"}
	cron := Process{Schedule: "0 * * * *", Regions: []string{"iad"}}
	if spec.ProcessRunsInRegion(cron, "ord") || !spec.ProcessRunsInRegion(cron, "iad") {
		t.Fatal("explicit placement should win")
	}
}
