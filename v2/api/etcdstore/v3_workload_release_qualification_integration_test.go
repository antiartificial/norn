package etcdstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestResolveWorkloadReleaseQualificationRequiresSignedCompletedSource(t *testing.T) {
	adapter, _, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	const app = "widgets"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging",
		FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default",
		Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseEtcdIntentFixture(app, target)
	intent.SourceRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	qualification, trustedKey := privateInputQualification(t, intent, "pending")
	qualification.DeploymentID = acceptPrivateWorkloadReleaseSource(t, adapter, intent, target)
	resignPrivateInputQualification(t, &qualification)
	finished := qualification.IssuedAt
	encoded, err := json.Marshal(qualification)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: qualification.ID, Kind: "release.qualification", App: app, Status: model.OperationSucceeded,
		Source: "release-control-api", StartedAt: qualification.IssuedAt, UpdatedAt: qualification.IssuedAt, FinishedAt: &finished, MaxAttempts: 1, Payload: payload}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "qualification-test", Subject: "staging-controller"},
			Kind: operation.Kind, Resource: app, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "qualification-test"},
		Semantics: map[string]interface{}{"qualificationId": qualification.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}

	resolved, err := adapter.ResolveWorkloadReleaseQualification(ctx, app, qualification.ID, []string{trustedKey}, time.Now().UTC())
	if err != nil || resolved.ID != qualification.ID || resolved.DeploymentID != qualification.DeploymentID {
		t.Fatalf("resolved qualification=%+v err=%v", resolved, err)
	}
	if _, err := adapter.ResolveWorkloadReleaseQualification(ctx, "other-app", qualification.ID, []string{trustedKey}, time.Now().UTC()); err == nil {
		t.Fatal("qualification was resolved for a different app")
	}
	if _, err := adapter.ResolveWorkloadReleaseQualification(ctx, app, qualification.ID, []string{"not-the-trusted-key"}, time.Now().UTC()); err == nil {
		t.Fatal("qualification was resolved without its trusted signer")
	}
	if _, err := adapter.ResolveWorkloadReleaseQualification(ctx, app, qualification.ID, []string{trustedKey}, qualification.ExpiresAt); err == nil {
		t.Fatal("expired qualification was resolved")
	}
}
