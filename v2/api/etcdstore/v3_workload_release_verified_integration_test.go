package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestV3VerifyClaimedWorkloadReleaseBindsSignedIntentTargetAndLock(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	const app = "widgets"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default", Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseEtcdIntentFixture(app, target)
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued, MaxAttempts: 3, Source: "test", Payload: map[string]interface{}{"workloadReleaseIntent": string(intentBytes)}}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "workload-release-test", Subject: "operator"}, Kind: operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "workload-release-test"}, Semantics: map[string]interface{}{"intentId": intent.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "workload-release-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	verified, accepted, err := adapter.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil || verified.ID != intent.ID || accepted.FleetAppTarget == nil || !sameFleetAppTarget(*accepted.FleetAppTarget, target) {
		t.Fatalf("verified=%+v target=%+v err=%v", verified, accepted.FleetAppTarget, err)
	}

	changedTarget := target
	changedTarget.Cluster = "norn-staging-replacement"
	changedTarget.Generation++
	changedBytes, err := json.Marshal(changedTarget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.fleetAppTargetKey(app, target.ControlEnvironment), string(changedBytes)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.VerifyClaimedWorkloadRelease(ctx, claim, lock); err == nil {
		t.Fatal("changed Fleet target remained authorized by the prior workload intent")
	}
	targetBytes, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.fleetAppTargetKey(app, target.ControlEnvironment), string(targetBytes)); err != nil {
		t.Fatal(err)
	}

	current, _, err := adapter.load(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Operation.Payload["workloadReleaseIntent"] = string(bytes.Replace(intentBytes, []byte("norn-staging"), []byte("norn-substituted"), 1))
	currentBytes, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.opKey(operation.ID), string(currentBytes)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.VerifyClaimedWorkloadRelease(ctx, claim, lock); err == nil {
		t.Fatal("operation payload drift remained authorized by the signed acceptance")
	}

	lock.Release()
	if _, _, err := adapter.VerifyClaimedWorkloadRelease(ctx, claim, lock); err == nil {
		t.Fatal("released app lock still authorized workload release")
	}
}

func TestV3WorkloadReleaseStagedTargetBindingIsClaimFencedAndWriteOnce(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	const app = "widgets"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default", Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseEtcdIntentFixture(app, target)
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	plan := pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		MaxAttempts: 3, Source: "test", Payload: map[string]interface{}{"workloadReleasePlan": string(planBytes)}}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "workload-release-test", Subject: "operator"},
			Kind: operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "workload-release-test"}, Semantics: map[string]interface{}{"planId": intent.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "workload-release-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	request := nomad.CASDeploymentJobRequest{App: intent.TargetJobID, Region: intent.NomadRegion, Namespace: intent.Namespace,
		DeploymentID: intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest, OperationID: intent.TargetOperationID,
		ExecutionID: intent.TargetExecutionID, JobDigest: strings.TrimPrefix(intent.TargetJobDigest, "sha256:"), ImageTag: intent.TargetImage}
	observation := nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 9, JobModifyIndex: 47}
	binding, err := plan.BindObservedTarget("stage-effect-1", claim.Generation(), request, observation)
	if err != nil {
		t.Fatal(err)
	}
	written, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, binding)
	if err != nil || written != binding {
		t.Fatalf("binding=%+v err=%v", written, err)
	}
	resolved, loaded, accepted, err := adapter.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, "stage-effect-1")
	if err != nil || resolved.TargetJobVersion != 9 || resolved.TargetJobModifyIndex != 47 || loaded != binding || accepted.Operation.ID != operation.ID {
		t.Fatalf("resolved=%+v binding=%+v accepted=%+v err=%v", resolved, loaded, accepted.Operation, err)
	}
	if again, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, binding); err != nil || again != binding {
		t.Fatalf("same binding replay=%+v err=%v", again, err)
	}
	conflictObservation := observation
	conflictObservation.Version++
	conflictObservation.JobModifyIndex++
	conflict, err := plan.BindObservedTarget("stage-effect-1", claim.Generation(), request, conflictObservation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, conflict); err == nil {
		t.Fatal("a different staged target overwrote the write-once binding")
	}
	if _, err := client.Delete(ctx, adapter.ownerKey(operation.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, conflict); err == nil {
		t.Fatal("expired operation claim could write staged target evidence")
	}
	if _, _, _, err := adapter.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, "stage-effect-1"); err == nil {
		t.Fatal("expired operation claim could resolve staged target evidence")
	}
}

func workloadReleaseEtcdIntentFixture(app string, target FleetAppTarget) pipeline.WorkloadReleaseIntent {
	return pipeline.WorkloadReleaseIntent{
		ID: "release-1", App: app, Region: target.Region, NomadRegion: target.NomadRegion, Namespace: target.NomadNamespace,
		ControlEnvironment: target.ControlEnvironment, FleetCluster: target.Cluster, FleetEnvironment: target.FleetEnvironment, FleetDatacenters: append([]string(nil), target.Datacenters...), FleetTargetGeneration: target.Generation,
		Process: "web", Endpoint: "https://widgets.example.test", PublicPaths: []string{"/"}, RetryGETPaths: []string{"/"}, PrivateReadinessPath: "/ready", TrafficProbePath: "/", TrafficProbeBodySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpecDigest: functionInvocationEffectAttemptDigest, SourceRevision: "source-1", OldDeploymentID: "old-deploy", OldImageDigest: functionInvocationEffectAttemptDigest,
		OldSpecDigest: functionInvocationEffectAttemptDigest, OldJobID: "widgets-web-old", OldJobDigest: functionInvocationEffectAttemptDigest, OldJobVersion: 1, OldJobModifyIndex: 2,
		TargetDeploymentID: "target-deploy", TargetImage: "registry.example.test/widgets@" + functionInvocationEffectAttemptDigest, TargetImageDigest: functionInvocationEffectAttemptDigest,
		TargetJobID: "widgets-web-target", TargetJobDigest: functionInvocationEffectAttemptDigest, TargetSpecDigest: functionInvocationEffectAttemptDigest,
		TargetOperationID: "target-operation", TargetExecutionID: "target-execution", OldAllocationIDs: []string{"old-allocation"}, TargetJobVersion: 1, TargetJobModifyIndex: 3,
		TargetReplicas: 1, SurgeLimit: 1, FirstRouteGeneration: 4,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100, TargetWeight: 0}, {OldWeight: 90, TargetWeight: 10}, {OldWeight: 0, TargetWeight: 100}},
	}
}
