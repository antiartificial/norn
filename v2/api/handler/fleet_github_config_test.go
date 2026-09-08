package handler

import (
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/store"
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
	for configPath, wantEnvironment := range map[string]string{
		"environments/disposable/fleet/nyc3/cluster.yaml":        "disposable/fleet/nyc3",
		"environments/disposable/external-mac/nyc3/cluster.yaml": "disposable/external-mac/nyc3",
	} {
		t.Run(wantEnvironment, func(t *testing.T) {
			cfg := &config.Config{Environment: "staging", FleetGitHubPilotRunID: "pilot20260907", FleetGitHubConfigPath: configPath}
			environment, err := configuredFleetEnvironment(document, cfg)
			if err != nil || environment != wantEnvironment || !fleetEnvironmentMatchesControlPlane("staging", environment, cfg) {
				t.Fatalf("run-bound disposable environment = %q, %v", environment, err)
			}
			if fleetEnvironmentMatchesControlPlane("staging", "disposable/fleet/nyc3", cfg) != (wantEnvironment == "disposable/fleet/nyc3") {
				t.Fatal("run-bound root was accepted as a different disposable lane")
			}
		})
	}
	badConfig := &config.Config{Environment: "staging", FleetGitHubPilotRunID: "pilot20260907", FleetGitHubConfigPath: "environments/staging/nyc3/cluster.yaml"}
	if _, err := configuredFleetEnvironment(document, badConfig); err == nil {
		t.Fatal("disposable root accepted an ordinary staging config path")
	}
}

func TestOrdinaryFleetEnvironmentMappingsRemainUnchanged(t *testing.T) {
	for environment := range map[string]struct{}{"staging": {}, "production": {}} {
		t.Run(environment, func(t *testing.T) {
			document := &fleet.Document{Metadata: fleet.Metadata{Environment: environment}, Cluster: fleet.Cluster{Region: "nyc3"}}
			cfg := &config.Config{Environment: environment, FleetGitHubConfigPath: "environments/" + environment + "/nyc3/cluster.yaml"}
			fleetEnvironment, err := configuredFleetEnvironment(document, cfg)
			if err != nil || fleetEnvironment != environment+"/nyc3" || !fleetEnvironmentMatchesControlPlane(environment, fleetEnvironment, cfg) {
				t.Fatalf("ordinary fleet environment = %q, %v", fleetEnvironment, err)
			}
		})
	}
}

func TestCompletedDispatchReceiptCannotCrossDisposablePilotAuthority(t *testing.T) {
	binding := store.FleetGitHubDispatch{FleetEnvironment: "disposable/fleet/nyc3", PilotRunID: "pilot20260907", AllowDestructive: false}
	if !fleetGitHubDispatchMatchesCurrentLane(binding, "disposable/fleet/nyc3", "pilot20260907", false) {
		t.Fatal("current disposable receipt was not replay-compatible")
	}
	if fleetGitHubDispatchMatchesCurrentLane(binding, "disposable/fleet/nyc3", "pilot20260908", false) {
		t.Fatal("completed receipt from old pilot authority was replay-compatible")
	}
}
