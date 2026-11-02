package etcdstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestHeldFleetDeploymentRecoveryAdmissionAndHealthEtcd(t *testing.T) {
	s, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	var err error
	request := deploymentAdmissionRequest(t, s.authority)
	request.Deployment.SpecDigest = "pinned-spec-digest"
	request.Deployment.ImageTag = "registry.example.test/demo@sha256:" + strings.Repeat("a", 64)
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := s.ClaimNextOperation(ctx, "first", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	lock, ok, err := s.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !ok {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Release()
	effects, _ := NewV3DeploymentEffectReservations(s)
	reservation, nomadClient := recoveryNomadFixture(t, accepted, claim, s.authority)
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	token := reserved.Record.Token
	if _, err := effects.MarkSubmitAttempt(ctx, token); err != nil {
		t.Fatal(err)
	}
	id := effect.ExecutionIdentity{Supervisor: reserved.Record.Reservation.Supervisor, SupervisorExecutionID: reserved.Record.Reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-job:global:demo:7"}
	if err := effects.MarkLaunched(ctx, token, id); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: reserved.Record.Reservation.InputDigest, ResultDigest: "healthy", ResultReference: id.RuntimeInstanceID, SupervisorExecutionID: id.SupervisorExecutionID, RuntimeInstanceID: id.RuntimeInstanceID, EvidenceSource: "nomad-job-health", EvidenceReference: id.RuntimeInstanceID, ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	result := *accepted.Deployment
	result.Status = model.StatusFailed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusFailed, DesiredWeight: 100}}
	if err := s.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationFailed, "held", map[string]interface{}{"manualRecoveryRequired": true, "externalEffectRecoveryPending": true, "effectId": token.EffectID}); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueHeldFleetDeploymentByID(ctx, claim.OperationID(), "wrong-deployment"); err == nil {
		t.Fatal("wrong deployment accepted")
	}
	stillHeld, err := s.GetOperation(ctx, claim.OperationID())
	if err != nil || stillHeld.Status != model.OperationFailed || stillHeld.Metadata["manualRecoveryRequired"] != true || stillHeld.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("wrong ID changed held operation: %+v %v", stillHeld, err)
	}
	if err := s.RequeueHeldFleetDeploymentByID(ctx, claim.OperationID(), result.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueHeldFleetDeployment(ctx, claim.OperationID(), token); err == nil {
		t.Fatal("replay accepted")
	}
	_, next, err := s.ClaimNextOperation(ctx, "second", time.Minute, []string{"app.deploy"})
	if err != nil || next.OperationID() == "" {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.requireFleetRecoveryHealth(ctx, next, token); err == nil {
		t.Fatal("missing fresh health accepted")
	}
	if _, err := client.Delete(ctx, s.ownerKey(next.OperationID())); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	expired, err := s.GetOperation(ctx, next.OperationID())
	if err != nil || expired.Status != model.OperationFailed || expired.Metadata[fleetDeploymentRecoveryEffect] == nil {
		t.Fatalf("expired recovery receipt: %+v %v", expired, err)
	}
	if err := s.RequeueHeldFleetDeploymentByID(ctx, next.OperationID(), result.ID); err != nil {
		t.Fatalf("expired recovery operator retry: %v", err)
	}
	_, next, err = s.ClaimNextOperation(ctx, "after-expiry", time.Minute, []string{"app.deploy"})
	if err != nil || next.OperationID() == "" {
		t.Fatalf("expired recovery claim: %v", err)
	}
	if _, err := s.requireFleetRecoveryHealth(ctx, next, token); err == nil {
		t.Fatal("expired claim health reused")
	}
	if _, err := s.RecordClaimedFleetRecoveryHealth(ctx, next, lock, nil); err == nil {
		t.Fatal("failed health accepted")
	}
	if _, err := s.requireFleetRecoveryHealth(ctx, next, token); err == nil {
		t.Fatal("failed health persisted")
	}
	if err := s.StopClaimedFleetDeploymentRecovery(ctx, next, lock); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.GetOperation(ctx, next.OperationID())
	if err != nil || stopped.Status != model.OperationFailed || stopped.Metadata["manualRecoveryRequired"] != true || stopped.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("preflight stop lost holds: %+v %v", stopped, err)
	}
	if err := s.RequeueHeldFleetDeploymentByID(ctx, next.OperationID(), result.ID); err != nil {
		t.Fatalf("operator retry: %v", err)
	}
	_, next, err = s.ClaimNextOperation(ctx, "third", time.Minute, []string{"app.deploy"})
	if err != nil || next.OperationID() == "" {
		t.Fatalf("retry claim: %v", err)
	}
	if err := s.DeferClaimedOperationWithAppLock(ctx, next, lock, "publication pending", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	stopped, err = s.GetOperation(ctx, next.OperationID())
	if err != nil || stopped.Status != model.OperationFailed || stopped.Metadata["manualRecoveryRequired"] != true || stopped.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("publication stop lost holds: %+v %v", stopped, err)
	}
	if err := s.RequeueHeldFleetDeploymentByID(ctx, next.OperationID(), result.ID); err != nil {
		t.Fatalf("publication operator retry: %v", err)
	}
	_, next, err = s.ClaimNextOperation(ctx, "fourth", time.Minute, []string{"app.deploy"})
	if err != nil || next.OperationID() == "" {
		t.Fatalf("retry claim: %v", err)
	}
	if _, err := s.RecordClaimedFleetRecoveryHealth(ctx, next, lock, nomadClient); err != nil {
		t.Fatal(err)
	}
	cmp, err := s.requireFleetRecoveryHealth(ctx, next, token)
	if err != nil {
		t.Fatal(err)
	}
	key := s.fleetRecoveryHealthKey(next.OperationID(), next.Generation())
	if _, err := client.Put(ctx, key, "changed"); err != nil {
		t.Fatal(err)
	}
	txn, err := client.Txn(ctx).If(cmp).Then().Commit()
	if err != nil || txn.Succeeded {
		t.Fatalf("stale health CAS accepted: %v", err)
	}
	if err := s.finishClaimedDeployment(ctx, next, lock, result, regions, model.OperationFailed, "recovery failed", nil); err != nil {
		t.Fatalf("failed recovery aggregate: %v", err)
	}
	failed, err := s.GetOperation(ctx, next.OperationID())
	if err != nil || failed.Status != model.OperationFailed || failed.Metadata["manualRecoveryRequired"] != true || failed.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("failed recovery holds: %+v %v", failed, err)
	}
	stored, err := s.loadDeploymentRegionResults(ctx, result.ID)
	if err != nil || len(stored) != 1 || stored[0].Status != model.StatusFailed {
		t.Fatalf("failed recovery region: %+v %v", stored, err)
	}
}
