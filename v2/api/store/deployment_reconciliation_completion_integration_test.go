package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

func TestCompleteDeploymentReconciliationPreservesFailedSource(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	source := newAcceptance(t, stores[0], "source-deploy", "operator", "reconcile-final-app", true)
	source.Deployment.Environment = "staging"
	source.Deployment.ImageTag = "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source.Deployment.SpecDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	source.Operation.Payload["specDigest"] = source.Deployment.SpecDigest
	var err error
	source.Fingerprint, err = CanonicalOperationRequestFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := dbs[0].ClaimNextOperation(ctx, "source-worker", time.Minute, []string{"app.deploy"}); err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("source claim=%+v err=%v", claimed, err)
	}
	if err := dbs[0].StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: accepted.Deployment.ID, App: accepted.Operation.App,
		SagaID: accepted.Operation.SagaID, Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepComplete, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].UpdateDeploymentRegion(ctx, accepted.Deployment.ID, "west", model.StatusSubmitting, "eval-source", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	candidate, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := stores[0].Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	reconcile := OperationAcceptance{
		Identity: OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: "test-issuer", Subject: "operator"},
			Kind: "app.deployment-reconcile", Resource: accepted.Operation.App, Key: "reconcile-source"},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.deployment-reconcile", App: accepted.Operation.App,
			SagaID: uuid.NewString(), Ref: accepted.Operation.ID, Status: model.OperationQueued, Risk: "repair deployment projection",
			Source: "operator", StartedAt: now, NextAttemptAt: now, MaxAttempts: 1,
			Payload: map[string]interface{}{"sourceOperationId": accepted.Operation.ID, "deploymentId": accepted.Deployment.ID,
				"imageTag": candidate.ImageTag, "specDigest": accepted.Deployment.SpecDigest}},
		Audit: AcceptanceAuditContext{Source: "integration-test"}, Admission: OperationAdmissionPolicy{OneActiveMutablePerApp: true},
	}
	reconcile.Fingerprint, err = CanonicalOperationRequestFingerprint(reconcile)
	if err != nil {
		t.Fatal(err)
	}
	repair, err := stores[0].Accept(ctx, reconcile)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := dbs[0].ClaimNextOperation(ctx, "repair-worker", time.Minute, []string{"app.deployment-reconcile"})
	if err != nil || claimed == nil || claimed.ID != repair.Operation.ID {
		t.Fatalf("repair claim=%+v err=%v", claimed, err)
	}
	if err := dbs[0].CompleteDeploymentReconciliation(ctx, claim, candidate, time.Time{}); err == nil {
		t.Fatal("missing live observation repaired deployment")
	}
	if err := dbs[0].FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "forged reconciliation success", nil); err == nil {
		t.Fatal("generic completion forged a reconciliation success")
	}
	wrongImage := candidate
	wrongImage.ImageTag = "registry.example/other@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err := dbs[0].CompleteDeploymentReconciliation(ctx, claim, wrongImage, time.Now()); err == nil {
		t.Fatal("unaccepted observed image repaired deployment")
	}
	if err := dbs[0].CompleteDeploymentReconciliation(ctx, claim, candidate, time.Now()); err != nil {
		t.Fatal(err)
	}
	original, err := dbs[0].GetOperation(ctx, accepted.Operation.ID)
	if err != nil || original.Status != model.OperationFailed || original.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("original receipt changed=%+v err=%v", original, err)
	}
	completed, err := dbs[0].GetDeployment(ctx, accepted.Deployment.ID)
	if err != nil || completed.Status != model.StatusDeployed || completed.ImageTag != candidate.ImageTag || len(completed.Regions) != 1 || completed.Regions[0].Status != model.StatusDeployed {
		t.Fatalf("reconciled deployment=%+v err=%v", completed, err)
	}
	receipt, err := dbs[0].GetOperation(ctx, repair.Operation.ID)
	if err != nil || receipt.Status != model.OperationSucceeded || receipt.Metadata["sourceOperationId"] != accepted.Operation.ID {
		t.Fatalf("reconciliation receipt=%+v err=%v", receipt, err)
	}
	var intents int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1 AND subject_id=$2`, repair.Operation.ID, repair.Operation.SagaID).Scan(&intents); err != nil || intents != 1 {
		t.Fatalf("reconciliation archive intents=%d err=%v", intents, err)
	}
	if err := dbs[0].CompleteDeploymentReconciliation(ctx, claim, candidate, time.Now()); err == nil {
		t.Fatal("completed reconciliation executed twice")
	}
}
