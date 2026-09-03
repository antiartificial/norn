package pipeline

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestRollbackEnvironmentPreservesLiveLane(t *testing.T) {
	for _, environment := range []string{"staging", "production"} {
		environment := environment
		t.Run(environment, func(t *testing.T) {
			got, err := rollbackEnvironment(
				model.Deployment{Environment: environment},
				model.Deployment{Environment: environment},
			)
			if err != nil || got != environment {
				t.Fatalf("rollback environment = %q, %v; want %q, nil", got, err, environment)
			}
		})
	}
}

func TestRollbackDeploymentCarriesLiveEnvironment(t *testing.T) {
	deploy, err := newRollbackDeployment("demo", "saga-1", model.Deployment{Environment: "production"}, model.Deployment{ID: "prior", Environment: "production"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Environment != "production" || deploy.SourceRef != "prior" {
		t.Fatalf("rollback deployment = %#v; want production deployment sourced from prior", deploy)
	}
}

func TestRollbackEnvironmentRejectsCrossLaneTarget(t *testing.T) {
	if _, err := rollbackEnvironment(model.Deployment{Environment: "production"}, model.Deployment{Environment: "staging"}); err == nil {
		t.Fatal("rollback accepted a cross-environment target")
	}
}

func TestProductionAutomaticRollbackRejectsNonProductionCurrentDeployment(t *testing.T) {
	pipeline := &Pipeline{ReleaseEnvironment: "production"}
	_, err := pipeline.autoRollbackTarget(context.Background(), &model.Deployment{App: "demo", Environment: "staging"})
	if err == nil || err.Error() != "production automatic rollback requires a production deployment" {
		t.Fatalf("production automatic rollback error = %v; want production lane rejection", err)
	}
}

func TestHardenedStagingIsNotAProductionRollbackLane(t *testing.T) {
	pipeline := &Pipeline{Production: true, ReleaseEnvironment: "staging"}
	if pipeline.productionReleaseLane() {
		t.Fatal("hardened staging was treated as the production release lane")
	}
	if !pipeline.LegacyDeploymentAllowed() {
		t.Fatal("hardened staging unexpectedly rejected its staging deployment lane")
	}
}

func TestPromotionCandidateReverifiesQualificationAgainstCurrentKeys(t *testing.T) {
	receipt, trustedKey := signedPromotionReceipt(t, 'k')
	operation := model.Operation{
		Kind:   "app.deploy",
		Status: model.OperationSucceeded,
		App:    "demo",
		Payload: map[string]interface{}{
			"sourceSha": receipt.SourceSHA,
			"artifact":  receipt.Artifact,
		},
		Metadata: map[string]interface{}{
			"promotionQualification": receipt,
		},
	}
	candidate, err := promotionCandidate([]string{trustedKey}, operation)
	if err != nil || candidate.Repository != "acme/demo" {
		t.Fatalf("valid currently trusted promotion qualification = %#v, %v", candidate, err)
	}
	if _, err := promotionCandidate([]string{qualificationPublicKey('x')}, operation); err == nil {
		t.Fatal("qualification signed by a rotated-out key was accepted for rollback")
	}
	tampered := receipt
	tampered.Candidate.Repository = "acme/other"
	operation.Metadata["promotionQualification"] = tampered
	if _, err := promotionCandidate([]string{trustedKey}, operation); err == nil {
		t.Fatal("tampered signed qualification was accepted for rollback")
	}
}

func signedPromotionReceipt(t *testing.T, keyByte byte) (model.ReleaseQualification, string) {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytesOf(keyByte, ed25519.SeedSize))
	now := time.Now().UTC().Truncate(time.Microsecond)
	receipt := model.ReleaseQualification{
		SchemaVersion: model.ReleaseQualificationSchema,
		ID:            "qualification-1",
		App:           "demo",
		Environment:   "staging",
		DeploymentID:  "deployment-1",
		SourceSHA:     strings.Repeat("a", 40),
		Artifact:      "registry.example.test/demo@sha256:" + strings.Repeat("b", 64),
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Hour),
		Candidate: model.ReleaseCandidate{
			Provider: "github-actions", Repository: "acme/demo",
			Attestation: model.ReleaseAttestationIdentity{MaterialSHA: strings.Repeat("a", 40), SubjectDigest: "sha256:" + strings.Repeat("b", 64)},
		},
	}
	payload := []byte(model.CanonicalReleaseQualificationPayload(receipt))
	keyID := model.ReleaseQualificationKeyID(private.Public().(ed25519.PublicKey))
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, model.DSSEPAE(model.ReleaseQualificationPayloadType, payload)))
	receipt.KeyID, receipt.Signature = keyID, signature
	receipt.DSSE = model.DSSEEnvelope{PayloadType: model.ReleaseQualificationPayloadType, Payload: base64.RawStdEncoding.EncodeToString(payload), Signatures: []model.DSSESignature{{KeyID: keyID, Sig: signature}}}
	return receipt, qualificationPublicKey(keyByte)
}

func qualificationPublicKey(keyByte byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytesOf(keyByte, ed25519.SeedSize)).Public().(ed25519.PublicKey))
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestVerifyReleaseArtifactRechecksAllControlsOutsideNormalProductionDeploy(t *testing.T) {
	checks := []string{}
	pipeline := &Pipeline{
		ReleaseAdmissionMode: "keyed",
		VerifyArtifact: func(context.Context, string) error {
			checks = append(checks, "registry")
			return nil
		},
		VerifySignature: func(context.Context, string) error {
			checks = append(checks, "signature")
			return nil
		},
		ScanArtifact: func(context.Context, string) error {
			checks = append(checks, "vulnerability")
			return nil
		},
	}
	artifact := "registry.example.test/demo@sha256:" + strings.Repeat("a", 64)
	if err := pipeline.VerifyReleaseArtifact(context.Background(), strings.Repeat("b", 40), artifact, model.ReleaseCandidate{}); err != nil {
		t.Fatalf("final rollback admission failed: %v", err)
	}
	if got, want := strings.Join(checks, ","), "registry,signature,vulnerability"; got != want {
		t.Fatalf("release re-admission controls = %q, want %q", got, want)
	}
}
