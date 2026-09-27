package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/store"
)

func deploymentEffectReservation(t *testing.T, accepted store.AcceptedOperation, claim store.OperationClaim, authority string) effect.Reservation {
	t.Helper()
	digest := sha256.Sum256([]byte("pinned-nomad-job"))
	input := deploymentEffectInput{App: accepted.Operation.App, DeploymentID: accepted.Deployment.ID, Region: accepted.Regions[0].Name, NomadRegion: accepted.Regions[0].NomadRegion,
		ImageTag: accepted.Deployment.ImageTag, SpecDigest: accepted.Deployment.SpecDigest, JobDigest: hex.EncodeToString(digest[:])}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := effect.Reservation{Authority: authority, Resource: "app/" + input.App + "/deploy/" + input.Region,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: deploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest), LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestV3DeploymentEffectReservationBindsSignedPlacementEtcd(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	request.Deployment.SpecDigest = "pinned-spec-digest"
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewV3DeploymentEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	r := deploymentEffectReservation(t, accepted, claim, adapter.authority)
	changed := r
	var input deploymentEffectInput
	if err := json.Unmarshal(changed.LaunchPayload, &input); err != nil {
		t.Fatal(err)
	}
	input.Region = "unaccepted"
	changed.LaunchPayload, _ = json.Marshal(input)
	changed.Resource = "app/demo/deploy/unaccepted"
	changed.SupervisorExecutionID = deploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest)
	changed.InputDigest, _ = effect.ComputeInputDigest(changed)
	if _, err := effects.Reserve(ctx, changed); err == nil {
		t.Fatal("unaccepted deployment region reserved a Nomad effect")
	}
	reserved, err := effects.Reserve(ctx, r)
	if err != nil || !reserved.Created {
		t.Fatalf("reserve=%+v err=%v", reserved, err)
	}
	identity := effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-deployment:demo:west"}
	if err := effects.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: r.InputDigest, ResultDigest: "sha256:result", ResultReference: "nomad-eval-1",
		SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID, EvidenceSource: "nomad.deployment", EvidenceReference: "eval-1", ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := effects.UnresolvedForResource(ctx, adapter.authority, r.Resource); err != nil || found {
		t.Fatalf("completed effect retained app gate: found=%v err=%v", found, err)
	}
	if _, err := effects.Reserve(ctx, r); err != nil && !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("same-input reservation replay: %v", err)
	}
}
