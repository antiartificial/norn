package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

func TestDeploymentReconciliationCandidateRequiresSignedUnsupersededProvenance(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	input := newAcceptance(t, stores[0], "reconcile-candidate", "operator", "reconcile-app", true)
	input.Deployment.Environment = "staging"
	input.Deployment.ImageTag = "registry.example/app@sha256:" + strings.Repeat("a", 64)
	input.Deployment.SourceKind = "git_clone"
	input.Deployment.SourceRef = "refs/heads/main"
	input.Deployment.CommitSHA = strings.Repeat("c", 40)
	input.Deployment.SpecDigest = "sha256:" + strings.Repeat("b", 64)
	input.Operation.Payload["specDigest"] = input.Deployment.SpecDigest
	var err error
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID); !errors.Is(err, ErrDeploymentReconciliationUnavailable) {
		t.Fatalf("queued deployment became candidate: %v", err)
	}
	claimed, _, err := dbs[0].ClaimNextOperation(ctx, "candidate-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := dbs[0].StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: accepted.Deployment.ID, App: accepted.Operation.App,
		SagaID: accepted.Operation.SagaID, Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepRunning, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].UpdateDeploymentRegion(ctx, accepted.Deployment.ID, "west", model.StatusSubmitting, "eval-accepted", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	candidate, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID)
	if err != nil || candidate.ImageTag != input.Deployment.ImageTag || candidate.Acceptance.Deployment.ID != accepted.Deployment.ID {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	newer := &model.Deployment{ID: uuid.NewString(), App: input.Operation.App, SagaID: uuid.NewString(), Environment: input.Deployment.Environment,
		Status: model.StatusQueued, StartedAt: accepted.Deployment.StartedAt.Add(time.Second)}
	if err := dbs[0].InsertDeployment(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID); !errors.Is(err, ErrDeploymentReconciliationUnavailable) {
		t.Fatalf("superseded deployment became candidate: %v", err)
	}
}

func TestDeploymentReconciliationCandidateUsesVerifiedBuildCheckpoint(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	input := newAcceptance(t, stores[0], "build-checkpoint-candidate", "operator", "build-candidate-app", true)
	input.Deployment.SpecDigest = "sha256:" + strings.Repeat("b", 64)
	input.Operation.Payload["specDigest"] = input.Deployment.SpecDigest
	var err error
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := dbs[0].ClaimNextOperation(ctx, "build-candidate-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	source := json.RawMessage(`{"sourceKind":"git_clone","commitSha":"0123456789abcdef0123456789abcdef01234567","sourceRef":"refs/heads/main","treeDigest":"sha256:source"}`)
	if _, err := dbs[0].RecordOperationCheckpoint(ctx, claim, CheckpointSource, source); err != nil {
		t.Fatal(err)
	}
	image := "registry.example/app@sha256:" + strings.Repeat("a", 64)
	build, err := json.Marshal(map[string]string{"imageTag": image, "sourceIdentity": checkpointDigest(source)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].RecordOperationCheckpoint(ctx, claim, CheckpointBuild, build); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: accepted.Deployment.ID, App: accepted.Operation.App,
		SagaID: accepted.Operation.SagaID, Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepRunning, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].UpdateDeploymentRegion(ctx, accepted.Deployment.ID, "west", model.StatusSubmitting, "eval-checkpoint", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	candidate, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID)
	if err != nil || candidate.ImageTag != image || candidate.CommitSHA != "0123456789abcdef0123456789abcdef01234567" || candidate.SourceRef != "refs/heads/main" {
		t.Fatalf("checkpoint candidate=%+v err=%v", candidate, err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_checkpoints SET outputs=$2 WHERE operation_id=$1 AND stage='build'`,
		accepted.Operation.ID, []byte(`{"imageTag":"registry.example/other@sha256:`+strings.Repeat("c", 64)+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].DeploymentReconciliationCandidate(ctx, accepted.Operation.ID); err == nil {
		t.Fatal("tampered build checkpoint remained a candidate")
	}
}
