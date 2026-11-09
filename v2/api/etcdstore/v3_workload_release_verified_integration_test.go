package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/effect"
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
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	stageInput, err := json.Marshal(workloadReleaseStageTargetEffectInput{PlanDigest: planDigest, TargetJobID: intent.TargetJobID,
		DeploymentID: intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest, Image: intent.TargetImage})
	if err != nil {
		t.Fatal(err)
	}
	stageEffects, err := NewV3WorkloadReleaseEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: adapter.authority, Resource: "app/" + app + "/workload-release/" + intent.Region,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          workloadReleaseStageTargetEffectStage, Supervisor: workloadReleaseStageTargetSupervisor,
		SupervisorExecutionID: workloadReleaseStageTargetExecutionID(claim.OperationID(), planDigest), LaunchPayload: stageInput}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	wrongTarget := reservation
	var changedStageInput workloadReleaseStageTargetEffectInput
	if err := json.Unmarshal(wrongTarget.LaunchPayload, &changedStageInput); err != nil {
		t.Fatal(err)
	}
	changedStageInput.TargetJobID = "substituted-target-job"
	wrongTarget.LaunchPayload, err = json.Marshal(changedStageInput)
	if err != nil {
		t.Fatal(err)
	}
	wrongTarget.InputDigest, err = effect.ComputeInputDigest(wrongTarget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageEffects.Reserve(ctx, wrongTarget); err == nil {
		t.Fatal("stage effect for a substituted target matched the signed plan")
	}
	reserved, err := stageEffects.Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("stage effect reservation=%+v err=%v", reserved, err)
	}
	staleClaim := claim
	if err := adapter.DeferClaimedOperationWithAppLock(ctx, claim, lock, "resume unattempted target reservation", time.Now(), nil); err != nil {
		t.Fatalf("defer before create attempt: %v", err)
	}
	_, claim, err = adapter.ClaimNextOperation(ctx, "workload-release-pre-attempt-successor", time.Minute, []string{operation.Kind})
	if err != nil || claim.OperationID() != operation.ID || claim.Generation() <= staleClaim.Generation() {
		t.Fatalf("pre-attempt successor claim=%+v err=%v", claim, err)
	}
	reservation.OperationClaim = effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}
	recoveredReservation, err := stageEffects.Reserve(ctx, reservation)
	if err != nil || recoveredReservation.Created || recoveredReservation.Record.Token != reserved.Record.Token {
		t.Fatalf("pre-attempt reservation recovery=%+v err=%v", recoveredReservation, err)
	}
	reserved = recoveredReservation
	if allowed, err := adapter.MarkWorkloadReleaseStageTargetAttempt(ctx, claim, lock, reserved.Record.Token.EffectID, "runtime-1"); err != nil || !allowed {
		t.Fatalf("stage attempt allowed=%v err=%v", allowed, err)
	}
	if allowed, err := adapter.MarkWorkloadReleaseStageTargetAttempt(ctx, claim, lock, reserved.Record.Token.EffectID, "runtime-2"); err != nil || allowed {
		t.Fatalf("replayed stage attempt allowed=%v err=%v", allowed, err)
	}
	staleClaim = claim
	if err := adapter.DeferClaimedOperationWithAppLock(ctx, claim, lock, "reconcile uncertain target registration", time.Now(), map[string]interface{}{"externalEffectRecoveryPending": true}); err != nil {
		t.Fatalf("defer after uncertain target registration: %v", err)
	}
	_, claim, err = adapter.ClaimNextOperation(ctx, "workload-release-successor", time.Minute, []string{operation.Kind})
	if err != nil || claim.OperationID() != operation.ID || claim.Generation() <= staleClaim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", claim, err)
	}
	observer := &workloadReleaseTargetObserver{observation: nomad.DeploymentJobObservation{State: nomad.DeploymentJobNotFound}}
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer); err == nil {
		t.Fatal("successor claim bound an absent target after an ambiguous create")
	}
	if observer.calls != 1 || observer.request.ExpectedJobModifyIndex != 0 {
		t.Fatalf("target lookup did not use exact create-only request: calls=%d request=%+v", observer.calls, observer.request)
	}
	observer.observation = nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 9, JobModifyIndex: 47}
	written, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer)
	if err != nil {
		t.Fatalf("binding=%+v err=%v", written, err)
	}
	binding := written
	if binding.ClaimGeneration != claim.Generation() || binding.ClaimGeneration == staleClaim.Generation() {
		t.Fatalf("successor did not own the recovered target binding: %+v", binding)
	}
	if observer.calls != 2 || observer.request.App != intent.App || observer.request.JobID != intent.TargetJobID ||
		observer.request.PlacementRegion != intent.Region || observer.request.ExpectedJobModifyIndex != 0 ||
		observer.request.JobDigest != strings.TrimPrefix(intent.TargetJobDigest, "sha256:") {
		t.Fatalf("observer did not receive exact create-only target identity: calls=%d request=%+v", observer.calls, observer.request)
	}
	completedEffect, _, _, err := stageEffects.loadToken(ctx, reserved.Record.Token)
	if err != nil || completedEffect.Lifecycle != effect.LifecycleCompleted || completedEffect.Completion == nil || completedEffect.Completion.Verification.ResultDigest != written.ObservationDigest {
		t.Fatalf("stage effect was not completed from the exact target readback: record=%+v err=%v", completedEffect, err)
	}
	if _, unresolved, err := stageEffects.UnresolvedForResource(ctx, adapter.authority, reservation.Resource); err != nil || unresolved {
		t.Fatalf("completed stage effect still holds the shared app gate: unresolved=%v err=%v", unresolved, err)
	}
	resolved, loaded, accepted, err := adapter.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, reserved.Record.Token.EffectID)
	if err != nil || resolved.TargetJobVersion != 9 || resolved.TargetJobModifyIndex != 47 || loaded != binding || accepted.Operation.ID != operation.ID {
		t.Fatalf("resolved=%+v binding=%+v accepted=%+v err=%v", resolved, loaded, accepted.Operation, err)
	}
	verifiedIntent, verifiedAcceptance, err := adapter.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil || verifiedIntent.TargetJobVersion != 9 || verifiedIntent.TargetJobModifyIndex != 47 || verifiedAcceptance.FleetAppTarget == nil {
		t.Fatalf("plan verifier did not resolve the bound strict intent: intent=%+v accepted=%+v err=%v", verifiedIntent, verifiedAcceptance, err)
	}
	strictIntentBytes, err := json.Marshal(verifiedIntent)
	if err != nil {
		t.Fatal(err)
	}
	stopDescriptor, err := json.Marshal(workloadReleaseEffectDescriptor{Intent: strictIntentBytes})
	if err != nil {
		t.Fatal(err)
	}
	stopReservation := effect.Reservation{Authority: adapter.authority, Resource: reservation.Resource,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          "app.workload-release.stop-old.nomad", Supervisor: "nomad-workload-release-stop",
		SupervisorExecutionID: "stop-" + uuid.NewString(), LaunchPayload: stopDescriptor}
	stopReservation.InputDigest, err = effect.ComputeInputDigest(stopReservation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageEffects.Reserve(ctx, stopReservation); err != nil {
		t.Fatalf("plan-derived stop effect did not validate against signed acceptance and binding: %v", err)
	}
	if again, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer); err != nil || again != binding {
		t.Fatalf("same binding replay=%+v err=%v", again, err)
	}
	observer.observation.Version++
	observer.observation.JobModifyIndex++
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer); err == nil {
		t.Fatal("a different staged target overwrote the write-once binding")
	}
	if _, err := client.Delete(ctx, adapter.ownerKey(operation.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.BindClaimedWorkloadReleaseTarget(ctx, staleClaim, lock, reserved.Record.Token.EffectID, observer); err == nil {
		t.Fatal("expired operation claim could write staged target evidence")
	}
	if _, _, _, err := adapter.ResolveClaimedWorkloadReleasePlan(ctx, staleClaim, lock, reserved.Record.Token.EffectID); err == nil {
		t.Fatal("expired operation claim could resolve staged target evidence")
	}
}

type workloadReleaseTargetObserver struct {
	observation nomad.DeploymentJobObservation
	request     nomad.CASDeploymentJobRequest
	calls       int
}

func (o *workloadReleaseTargetObserver) LookupDeploymentJobRevision(_ context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error) {
	o.calls++
	o.request = request
	return o.observation, nil
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
		TargetReplicas: 1, SurgeLimit: 1, PolicyApprovalID: workloadReleaseTestPolicyApprovalID,
		PolicyDigest: workloadReleaseTestPolicyDigest(app, target), FirstRouteGeneration: 4,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100, TargetWeight: 0}, {OldWeight: 90, TargetWeight: 10}, {OldWeight: 0, TargetWeight: 100}},
	}
}

const workloadReleaseTestPolicyApprovalID = "11111111-1111-4111-8111-111111111111"

func workloadReleaseTestPolicy(app string, target FleetAppTarget) store.WorkloadReleasePolicy {
	return store.WorkloadReleasePolicy{App: app, ControlEnvironment: target.ControlEnvironment, FleetTargetGeneration: target.Generation,
		SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: workloadReleaseTestPolicyApprovalID, ApprovedBy: "workload-release-test",
		ApprovedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Mode: store.WorkloadReleasePolicyStatelessOverlap,
		MinReplicas: 1, MaxReplicas: 5, SurgeLimit: 1,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
}

func workloadReleaseTestPolicyDigest(app string, target FleetAppTarget) string {
	digest, err := WorkloadReleasePolicyDigest(workloadReleaseTestPolicy(app, target))
	if err != nil {
		panic(err)
	}
	return digest
}
