package handler

import (
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
)

func TestFleetAuthorityOnlyPinsGitHubAppAPI(t *testing.T) {
	cfg := &config.Config{
		FleetAuthorityOnly:       true,
		FleetGitHubAPIBaseURL:    "https://api.github.com",
		FleetGitHubRepository:    "acme/norn-fleet",
		FleetGitHubEnvironment:   "staging",
		FleetGitHubConfigPath:    "environments/staging/nyc3/cluster.yaml",
		FleetGitHubDefaultBranch: "main",
		FleetGitHubPlanWorkflow:  "plan.yml",
		FleetGitHubApplyWorkflow: "apply.yml",
	}
	if !fleetGitHubConfig(cfg).Production {
		t.Fatal("Fleet authority-only GitHub App configuration did not enable GitHub API host pinning")
	}
}

func TestRunBoundDisposableFleetEnvironmentIsNotStagingAlias(t *testing.T) {
	document := &fleet.Document{Metadata: fleet.Metadata{Environment: "staging"}, Cluster: fleet.Cluster{Region: "nyc3"}}
	cfg := &config.Config{Environment: "staging", FleetGitHubPilotRunID: "pilot20260907", FleetGitHubConfigPath: "environments/disposable/fleet/nyc3/cluster.yaml"}
	environment, err := configuredFleetEnvironment(document, cfg)
	if err != nil || environment != "disposable/fleet/nyc3" || !fleetEnvironmentMatchesControlPlane("staging", environment, cfg) {
		t.Fatalf("run-bound disposable environment = %q, %v", environment, err)
	}
	cfg.FleetGitHubConfigPath = "environments/staging/nyc3/cluster.yaml"
	if _, err := configuredFleetEnvironment(document, cfg); err == nil {
		t.Fatal("disposable root accepted an ordinary staging config path")
	}
}
