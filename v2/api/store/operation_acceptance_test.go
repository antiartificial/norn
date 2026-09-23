package store

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/model"
)

func TestCanonicalOperationRequestFingerprintNormalizesDefaultsAndOrdering(t *testing.T) {
	base := OperationAcceptance{
		Identity: OperationRequestIdentity{Authority: "00000000-0000-0000-0000-000000000001", Actor: OperationActor{Issuer: "issuer", Subject: "actor"}, Kind: "app.deploy", Resource: "demo", Key: "same"},
		Operation: model.Operation{ID: "generated-one", Kind: "app.deploy", App: "demo", SagaID: "saga-one", Payload: map[string]interface{}{
			"deploymentId": "deployment-one", "promotionQualification": map[string]interface{}{"deploymentId": "semantic-target"},
		}},
		Deployment: &model.Deployment{ID: "deployment-one", App: "demo", SagaID: "saga-one", SourceChanges: []string{"b", "a"}},
		Regions: []model.ResolvedRegion{
			{Name: "west", NomadRegion: "global", Datacenters: []string{"b", "a", "a"}, TrafficWeight: 50},
			{Name: "east", NomadRegion: "global", Datacenters: []string{"d"}, TrafficWeight: 50},
		},
	}
	first, err := CanonicalOperationRequestFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := base
	secondInput.Operation.ID, secondInput.Operation.SagaID = "generated-two", "saga-two"
	secondInput.Operation.Status, secondInput.Operation.MaxAttempts = model.OperationQueued, 1
	secondInput.Operation.Payload = map[string]interface{}{"deploymentId": "deployment-two", "promotionQualification": map[string]interface{}{"deploymentId": "semantic-target"}}
	secondDeployment := *base.Deployment
	secondDeployment.ID, secondDeployment.SagaID, secondDeployment.Status = "deployment-two", "saga-two", model.StatusQueued
	secondInput.Deployment = &secondDeployment
	secondInput.Regions[0], secondInput.Regions[1] = secondInput.Regions[1], secondInput.Regions[0]
	secondInput.Regions[1].Datacenters = []string{"a", "b"}
	second, err := CanonicalOperationRequestFingerprint(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("normalized fingerprints differ: %+v %+v", first, second)
	}

	nestedChanged := secondInput
	nestedChanged.Operation.Payload = map[string]interface{}{"deploymentId": "deployment-two", "promotionQualification": map[string]interface{}{"deploymentId": "different-semantic-target"}}
	changed, err := CanonicalOperationRequestFingerprint(nestedChanged)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("nested semantic deployment ID was omitted from fingerprint")
	}

	terminal := secondInput
	terminal.Operation.Status = model.OperationSucceeded
	terminalDigest, err := CanonicalOperationRequestFingerprint(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if terminalDigest == first {
		t.Fatal("queued and terminal dispositions have the same fingerprint")
	}
}

func TestHMACAcceptanceSignerVerifiesRetainedKeyAndRejectsTamper(t *testing.T) {
	oldKey, newKey := "old-acceptance-signing-key-000000000", "new-acceptance-signing-key-000000000"
	oldSigner, err := NewHMACAcceptanceSigner(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := oldSigner.Sign(context.Background(), []byte("accepted"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewHMACAcceptanceSigner(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotated.Verify(context.Background(), signature, []byte("accepted")); err != nil {
		t.Fatal(err)
	}
	if err := rotated.Verify(context.Background(), signature, []byte("altered")); err == nil {
		t.Fatal("altered canonical bytes verified")
	}
	withoutOld, err := NewHMACAcceptanceSigner(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutOld.Verify(context.Background(), signature, []byte("accepted")); err == nil {
		t.Fatal("signature verified without retained original key")
	}
	if _, err := NewHMACAcceptanceSigner("short"); err == nil {
		t.Fatal("short signing key accepted")
	}
	if !errors.Is((&AcceptanceSignatureError{}), ErrAcceptanceSignature) {
		t.Fatal("signature error is not typed")
	}
}
