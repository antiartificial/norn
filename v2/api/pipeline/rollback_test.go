package pipeline

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/privateattestation"
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
	if err := pipeline.VerifyReleaseArtifact(context.Background(), &model.InfraSpec{App: "demo"}, strings.Repeat("b", 40), artifact, model.ReleaseCandidate{}); err != nil {
		t.Fatalf("final rollback admission failed: %v", err)
	}
	if got, want := strings.Join(checks, ","), "registry,signature,vulnerability"; got != want {
		t.Fatalf("release re-admission controls = %q, want %q", got, want)
	}
}

func TestNornPrivateRollbackAdmissionBindsServerOwnedApp(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := privateattestation.NewLocalSigner(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := privateattestation.NewVerifier([]string{base64.RawStdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	sourceSHA := strings.Repeat("b", 40)
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := "registry.example.test/norn/demo@" + digest
	signerSHA := strings.Repeat("c", 40)
	signerRef := "personal-owner/norn/.github/workflows/norn-app-release.yml@" + signerSHA
	candidate := model.ReleaseCandidate{
		Provider: "github-actions", Repository: "personal-owner/private-app", RepositoryID: "101", OwnerID: "202", RepositoryVisibility: "private", RunID: "303", RunAttempt: "1", WorkflowRef: "personal-owner/private-app/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: sourceSHA, SignerWorkflowRef: signerRef, SignerWorkflowSHA: signerSHA, Ref: "refs/heads/main",
		Attestation: model.ReleaseAttestationIdentity{Mode: "norn-signed-private", Verifier: "Norn private DSSE", Issuer: "https://token.actions.githubusercontent.com", SubjectDigest: digest, MaterialSHA: sourceSHA},
	}
	sbom := json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx/private-app"}`)
	candidate.Attestation.Bundle, err = privateattestation.Issue(context.Background(), signer, "demo", sourceSHA, artifact, sbom, candidate)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := &Pipeline{
		ReleaseAdmissionMode: "attested", ReleaseAttestationTrustMode: "norn-signed-private", ReleaseAttestationIssuer: candidate.Attestation.Issuer, ReleaseAttestationRepositories: []string{candidate.Repository}, ReleaseAttestationWorkflowRefs: []string{signerRef}, ReleaseRequireSBOM: true,
		VerifyArtifact: func(context.Context, string) error { return nil }, ScanArtifact: func(context.Context, string) error { return nil }, VerifyNornPrivateAttestations: verifier.Verify,
	}
	if err := pipeline.VerifyReleaseArtifact(context.Background(), &model.InfraSpec{App: "demo"}, sourceSHA, artifact, candidate); err != nil {
		t.Fatalf("valid Norn-private rollback evidence rejected: %v", err)
	}
	if err := pipeline.VerifyReleaseArtifact(context.Background(), &model.InfraSpec{App: "another-app"}, sourceSHA, artifact, candidate); err == nil {
		t.Fatal("Norn-private rollback evidence was accepted for a different server-owned app")
	}
}
