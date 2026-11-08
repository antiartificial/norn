package etcdstore

import (
	"strings"
	"testing"

	"norn/v2/api/pipeline"
)

func TestWorkloadRouteStepAuthorizationRequiresHealthyForwardProgress(t *testing.T) {
	intent := pipeline.WorkloadReleaseIntent{
		ID: "workload-route-test", App: "widgets", Region: "nyc3", NomadRegion: "global", Namespace: "default",
		ControlEnvironment: "staging", FleetCluster: "norn-staging", FleetEnvironment: "staging/nyc3", FleetDatacenters: []string{"nyc3"}, FleetTargetGeneration: 1,
		Process: "web", Endpoint: "https://widgets.example.test", PublicPaths: []string{"/proof"}, RetryGETPaths: []string{"/proof"},
		PrivateReadinessPath: "/ready", TrafficProbePath: "/proof", TrafficProbeBodySHA256: strings.Repeat("a", 64),
		SpecDigest: "sha256:" + strings.Repeat("1", 64), SourceRevision: "source-1",
		OldDeploymentID: "old-deploy", OldImageDigest: "sha256:" + strings.Repeat("2", 64), OldSpecDigest: "sha256:" + strings.Repeat("3", 64),
		OldJobID: "widgets-web-old", OldJobDigest: "sha256:" + strings.Repeat("4", 64), OldJobVersion: 1, OldJobModifyIndex: 2,
		TargetDeploymentID: "new-deploy", TargetImage: "registry.example.test/widgets@sha256:" + strings.Repeat("5", 64), TargetImageDigest: "sha256:" + strings.Repeat("5", 64),
		TargetJobID: "widgets-web-new", TargetJobDigest: "sha256:" + strings.Repeat("6", 64), TargetSpecDigest: "sha256:" + strings.Repeat("7", 64),
		TargetOperationID: "target-operation", TargetExecutionID: "target-execution", OldAllocationIDs: []string{"old-allocation"},
		TargetJobVersion: 2, TargetJobModifyIndex: 3, TargetReplicas: 1, SurgeLimit: 1, FirstRouteGeneration: 10,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}},
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	route0, err := intent.RenderRouteStep(0)
	if err != nil {
		t.Fatal(err)
	}
	route1, err := intent.RenderRouteStep(1)
	if err != nil {
		t.Fatal(err)
	}
	route2, err := intent.RenderRouteStep(2)
	if err != nil {
		t.Fatal(err)
	}
	targets := []pipeline.WorkloadReleaseAllocationObservation{{
		AllocationID: "target-allocation", NodeID: "app-01", JobID: intent.TargetJobID, Namespace: intent.Namespace,
		TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
		ImageDigest: intent.TargetImageDigest, Healthy: true,
	}}
	warming := pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleaseWarming, TargetAllocations: targets}
	if !workloadRouteStepAllowed(intent, warming, 0) {
		t.Fatal("healthy warming receipt could not authorize the first pinned route generation")
	}
	mixed := pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleaseRouting, TargetAllocations: targets,
		RouteStep: 0, RouteOldWeight: 100, RouteGeneration: intent.FirstRouteGeneration, RouteSHA256: "sha256:" + route0.SHA256}
	if !workloadRouteStepAllowed(intent, mixed, 1) {
		t.Fatal("healthy route receipt could not authorize its next mixed step")
	}
	final := pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleaseDraining, TargetAllocations: targets,
		RouteStep: 1, RouteOldWeight: 90, RouteTargetWeight: 10, RouteGeneration: intent.FirstRouteGeneration + 1,
		RouteSHA256: "sha256:" + route1.SHA256, OldJobStoppedVersion: 2, OldJobStoppedModifyIndex: 3, DrainedOldIDs: []string{"old-allocation"}}
	if !workloadRouteStepAllowed(intent, final, 2) {
		t.Fatal("fully drained receipt could not authorize the final target-only route")
	}
	final.TargetAllocations = append([]pipeline.WorkloadReleaseAllocationObservation(nil), targets...)
	final.TargetAllocations[0].Healthy = false
	if workloadRouteStepAllowed(intent, final, 2) {
		t.Fatal("unhealthy target authorized a route transition")
	}
	wrongPhase := mixed
	wrongPhase.Phase = pipeline.WorkloadReleaseDraining
	if workloadRouteStepAllowed(intent, wrongPhase, 1) {
		t.Fatal("draining phase without complete old-generation proof authorized a mixed route")
	}
	prior := activeFleetRoute{IntentID: "initial-route", DeploymentID: intent.OldDeploymentID, Generation: intent.FirstRouteGeneration - 1, RouteSHA256: route0.SHA256}
	if !workloadRoutePredecessorMatches(intent, warming, 0, route0, prior) {
		t.Fatal("exact old-generation route was not accepted as the first predecessor")
	}
	prior.IntentID, prior.DeploymentID, prior.Generation, prior.RouteSHA256 = intent.ID, intent.TargetDeploymentID, mixed.RouteGeneration, route0.SHA256
	if !workloadRoutePredecessorMatches(intent, mixed, 1, route1, prior) {
		t.Fatal("exact mixed route was not accepted as the next predecessor")
	}
	prior.RouteSHA256 = route2.SHA256
	if workloadRoutePredecessorMatches(intent, mixed, 1, route1, prior) {
		t.Fatal("route predecessor with a different digest was accepted")
	}
}
