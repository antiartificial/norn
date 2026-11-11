package worker

import (
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
)

func TestBuildWorkloadReleaseTargetJobPlanIsExactCreateOnlySource(t *testing.T) {
	plan, spec := workloadReleaseTargetPlanFixture(t)
	first, err := BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	wantJobID, err := nomad.ManagedDeploymentJobID(plan.Intent.App, plan.Intent.Region, plan.Intent.TargetDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Request.JobDigest != second.Request.JobDigest || first.Request.App != plan.Intent.App || first.Request.JobID != plan.Intent.TargetJobID ||
		first.Request.JobID != wantJobID ||
		first.Request.ExpectedJobModifyIndex != 0 || first.Request.PlacementRegion != plan.Intent.Region ||
		first.Request.Region != plan.Intent.NomadRegion || first.Request.OperationID != plan.Intent.TargetOperationID ||
		first.Request.ExecutionID != plan.Intent.TargetExecutionID || first.Inputs == nil {
		t.Fatalf("target plan was not a deterministic create-only request: first=%+v second=%+v", first.Request, second.Request)
	}
	if first.Job.Meta[nomad.DeploymentJobDigestMeta] != first.Request.JobDigest ||
		first.Job.Meta[nomad.DeploymentIDMeta] != plan.Intent.TargetDeploymentID ||
		first.Job.Meta[nomad.SpecDigestMeta] != plan.Intent.TargetSpecDigest ||
		first.Job.Meta[nomad.DeploymentOperationIDMeta] != plan.Intent.TargetOperationID ||
		first.Job.Meta[nomad.DeploymentExecutionIDMeta] != plan.Intent.TargetExecutionID {
		t.Fatalf("target job metadata differs from its exact request: %#v", first.Job.Meta)
	}
}

func TestDeriveWorkloadReleaseTargetJobDigestMatchesExecutionBuilder(t *testing.T) {
	plan, spec := workloadReleaseTargetPlanFixture(t)
	intent := plan.Intent
	intent.TargetJobDigest = ""
	derived, err := pipeline.DeriveWorkloadReleaseTargetJobDigest(intent, spec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := pipeline.BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	if derived != "sha256:"+first.Request.JobDigest || first.Request.JobDigest != second.Request.JobDigest {
		t.Fatalf("admission digest=%q first=%q second=%q", derived, first.Request.JobDigest, second.Request.JobDigest)
	}

	if _, err := pipeline.DeriveWorkloadReleaseTargetJobDigest(plan.Intent, spec); err == nil {
		t.Fatal("admission accepted a caller-supplied target job digest")
	}
	changed := *spec
	changed.Processes = map[string]model.Process{"web": {Port: 8080, Command: "different"}}
	if _, err := pipeline.DeriveWorkloadReleaseTargetJobDigest(intent, &changed); err == nil {
		t.Fatal("admission derived a digest from source that differs from the accepted spec digest")
	}
}

func TestBuildWorkloadReleaseTargetJobPlanRejectsSourceOrDataContractDrift(t *testing.T) {
	plan, spec := workloadReleaseTargetPlanFixture(t)
	changed := *spec
	changed.Processes = map[string]model.Process{"web": {Port: 8080, Command: "different"}}
	if _, err := BuildWorkloadReleaseTargetJobPlan(plan, &changed); err == nil {
		t.Fatal("changed app source produced the accepted target")
	}
	databaseBacked := *spec
	databaseBacked.Databases = []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}
	if _, err := BuildWorkloadReleaseTargetJobPlan(plan, &databaseBacked); err == nil {
		t.Fatal("database-backed source entered the database-free first release builder")
	}
	changedPlan := plan
	changedPlan.Intent.TargetImage = "registry.example.test/demo@sha256:" + strings.Repeat("9", 64)
	if _, err := BuildWorkloadReleaseTargetJobPlan(changedPlan, spec); err == nil {
		t.Fatal("substituted image produced the accepted target job")
	}
}

func workloadReleaseTargetPlanFixture(t *testing.T) (pipeline.WorkloadReleasePlan, *model.InfraSpec) {
	t.Helper()
	spec := &model.InfraSpec{App: "demo", NomadNamespace: "default", Processes: map[string]model.Process{"web": {
		Port: 8080, Command: "./server", NomadVariables: &model.NomadVariableFiles{UID: 1000, GID: 1000,
			Files: []model.NomadVariableFile{{Key: "API_TOKEN", Destination: "api-token"}}},
		Resources: &model.Resources{CPU: 100, Memory: 128},
	}}, Endpoints: []model.Endpoint{{URL: "https://demo.example.test", Region: "local", Process: "web",
		TrafficProbe:      &model.TrafficProbeSpec{Path: "/", BodySHA256: strings.Repeat("a", 64)},
		RestrictedIngress: &model.RestrictedIngressSpec{PublicPaths: []string{"/"}, RetryGETPaths: []string{"/"}, PrivateReadinessPath: "/ready"}}}}
	region := spec.ResolvedRegions()[0]
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	intent := pipeline.WorkloadReleaseIntent{ID: "release-1", App: "demo", Region: region.Name, NomadRegion: region.NomadRegion,
		Namespace: spec.NomadNamespace, ControlEnvironment: "staging", FleetCluster: "norn-staging", FleetEnvironment: "staging/local",
		FleetDatacenters: append([]string(nil), region.Datacenters...), FleetTargetGeneration: 1, Process: "web", Endpoint: "https://demo.example.test",
		PublicPaths: []string{"/"}, RetryGETPaths: []string{"/"}, PrivateReadinessPath: "/ready", TrafficProbePath: "/",
		TrafficProbeBodySHA256: strings.Repeat("a", 64), SpecDigest: specDigest, SourceRevision: strings.Repeat("b", 40),
		OldDeploymentID: "old-deploy", OldImageDigest: "sha256:" + strings.Repeat("c", 64), OldSpecDigest: "sha256:" + strings.Repeat("d", 64),
		OldJobID: "old-job", OldJobDigest: "sha256:" + strings.Repeat("e", 64), OldJobVersion: 1, OldJobModifyIndex: 2,
		TargetDeploymentID: "target-deploy", TargetImage: "registry.example.test/demo@sha256:" + strings.Repeat("f", 64),
		TargetImageDigest: "sha256:" + strings.Repeat("f", 64), TargetJobID: "", TargetJobDigest: "", TargetSpecDigest: specDigest,
		TargetOperationID: "target-op", TargetExecutionID: "target-exec", OldAllocationIDs: []string{"alloc-old"},
		TargetJobVersion: 0, TargetJobModifyIndex: 0, TargetReplicas: 2, SurgeLimit: 1,
		PolicyApprovalID: "11111111-1111-4111-8111-111111111111", PolicyDigest: "sha256:" + strings.Repeat("a", 64), FirstRouteGeneration: 4,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
	intent.TargetJobID, err = nomad.ManagedDeploymentJobID(intent.App, intent.Region, intent.TargetDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := nomad.TranslateManagedDeploymentForRegionAt(spec, intent.TargetImage, nil, region, intent.TargetDeploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	nomad.ApplyDesiredReplicaCounts(job, map[string]int{intent.Process: intent.TargetReplicas})
	job.Meta[nomad.DeploymentIDMeta] = intent.TargetDeploymentID
	job.Meta[nomad.SpecDigestMeta] = intent.TargetSpecDigest
	job.Meta[nomad.DeploymentOperationIDMeta] = intent.TargetOperationID
	job.Meta[nomad.DeploymentExecutionIDMeta] = intent.TargetExecutionID
	digest, err := nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	intent.TargetJobDigest = "sha256:" + digest
	return pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}, spec
}
