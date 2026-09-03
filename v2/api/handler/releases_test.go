package handler

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/model"
)

func testQualification(t *testing.T, key string) model.ReleaseQualification {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	receipt := model.ReleaseQualification{
		SchemaVersion: releaseQualificationSchema, ID: "qualification-1", App: "demo", Environment: "staging", DeploymentID: "deployment-1",
		SourceSHA: strings.Repeat("a", 40), Artifact: "registry.example.test/demo@sha256:" + strings.Repeat("b", 64),
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), Candidate: testReleaseCandidate(),
	}
	if err := signReleaseQualification(key, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func releaseTestKey(ch byte) string {
	return base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{ch}, ed25519.SeedSize))
}
func releaseTestPublicKey(ch byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{ch}, ed25519.SeedSize)).Public().(ed25519.PublicKey))
}

func TestParseEd25519PrivateKeyRejectsMismatchedPublicHalf(t *testing.T) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{'k'}, ed25519.SeedSize))
	if _, err := parseEd25519PrivateKey(base64.RawStdEncoding.EncodeToString(private)); err != nil {
		t.Fatalf("valid 64-byte Ed25519 private key rejected: %v", err)
	}
	malformed := append(ed25519.PrivateKey(nil), private...)
	malformed[len(malformed)-1] ^= 0x01
	if _, err := parseEd25519PrivateKey(base64.RawStdEncoding.EncodeToString(malformed)); err == nil {
		t.Fatal("mismatched Ed25519 public half accepted")
	}
}

func testReleaseCandidate() model.ReleaseCandidate {
	return model.ReleaseCandidate{Provider: "github-actions", Repository: "owner/repo", RepositoryID: "1", OwnerID: "2", RepositoryVisibility: "public", RunID: "3", RunAttempt: "1", WorkflowRef: "owner/repo/.github/workflows/release.yml@" + strings.Repeat("c", 40), WorkflowSHA: strings.Repeat("c", 40), SignerWorkflowRef: "owner/repo/.github/workflows/release.yml@" + strings.Repeat("c", 40), SignerWorkflowSHA: strings.Repeat("c", 40), Ref: "refs/heads/main", Attestation: model.ReleaseAttestationIdentity{Mode: "github-public", Verifier: "Sigstore public-good", Issuer: githubActionsOIDCIssuer, SubjectDigest: "sha256:" + strings.Repeat("b", 64), MaterialSHA: strings.Repeat("a", 40)}}
}

func TestReleaseSpecBindingRequiresExactSourceAndArtifactNamespace(t *testing.T) {
	spec := &model.InfraSpec{
		App:   "orders-api",
		Repo:  &model.RepoSpec{URL: "git@github.com:acme/orders-api.git"},
		Build: &model.BuildSpec{Image: "ghcr.io/acme/orders-api@sha256:" + strings.Repeat("a", 64)},
	}
	candidate := testReleaseCandidate()
	candidate.Repository = "acme/orders-api"
	artifact := "ghcr.io/acme/orders-api@sha256:" + strings.Repeat("b", 64)
	if err := validateReleaseSpecBinding(spec, candidate, artifact, "ghcr.io/acme"); err != nil {
		t.Fatalf("exact app source/artifact binding rejected: %v", err)
	}

	for name, mutate := range map[string]func(*model.ReleaseCandidate, *string){
		"foreign application artifact": func(_ *model.ReleaseCandidate, artifact *string) {
			*artifact = "ghcr.io/acme/billing-api@sha256:" + strings.Repeat("b", 64)
		},
		"foreign source repository": func(candidate *model.ReleaseCandidate, _ *string) {
			candidate.Repository = "acme/billing-api"
		},
	} {
		t.Run(name, func(t *testing.T) {
			gotCandidate, gotArtifact := candidate, artifact
			mutate(&gotCandidate, &gotArtifact)
			if err := validateReleaseSpecBinding(spec, gotCandidate, gotArtifact, "ghcr.io/acme"); err == nil {
				t.Fatal("cross-app/cross-repository release binding accepted")
			}
		})
	}
}

func TestReleaseSpecBindingUsesServerRegistryForDockerfileBuild(t *testing.T) {
	spec := &model.InfraSpec{App: "orders-api", Repo: &model.RepoSpec{URL: "https://github.com/acme/orders-api.git"}, Build: &model.BuildSpec{Dockerfile: "Dockerfile"}}
	candidate := testReleaseCandidate()
	candidate.Repository = "acme/orders-api"
	artifact := "ghcr.io/acme/orders-api@sha256:" + strings.Repeat("b", 64)
	if err := validateReleaseSpecBinding(spec, candidate, artifact, "ghcr.io/acme/"); err != nil {
		t.Fatalf("server registry artifact repository rejected: %v", err)
	}
	if err := validateReleaseSpecBinding(spec, candidate, "ghcr.io/acme/other@sha256:"+strings.Repeat("b", 64), "ghcr.io/acme"); err == nil {
		t.Fatal("foreign Dockerfile artifact repository accepted")
	}
}

func TestReleaseQualificationVerification(t *testing.T) {
	key := releaseTestKey('k')
	receipt := testQualification(t, key)
	if err := verifyReleaseQualification([]string{releaseTestPublicKey('k')}, receipt); err != nil {
		t.Fatalf("verify signed qualification: %v", err)
	}
	if err := verifyReleaseQualification([]string{releaseTestPublicKey('x')}, receipt); err == nil {
		t.Fatal("untrusted signing key was accepted")
	}
	tamperedDisplay := receipt
	tamperedDisplay.KeyID = "ed25519:other"
	if err := verifyReleaseQualification([]string{releaseTestPublicKey('k')}, tamperedDisplay); err == nil {
		t.Fatal("unauthenticated top-level signer display was accepted")
	}
	for name, mutate := range map[string]func(*model.ReleaseQualification){
		"wrong environment": func(value *model.ReleaseQualification) { value.Environment = "production" },
		"expired": func(value *model.ReleaseQualification) {
			value.ExpiresAt = time.Now().UTC().Add(-time.Minute)
			_ = signReleaseQualification(key, value)
		},
		"future issued": func(value *model.ReleaseQualification) {
			value.IssuedAt = time.Now().UTC().Add(6 * time.Minute)
			value.ExpiresAt = value.IssuedAt.Add(time.Hour)
			_ = signReleaseQualification(key, value)
		},
		"bad source": func(value *model.ReleaseQualification) {
			value.SourceSHA = strings.Repeat("A", 40)
			_ = signReleaseQualification(key, value)
		},
		"bad digest": func(value *model.ReleaseQualification) {
			value.Artifact = "registry.example.test/demo:latest"
			_ = signReleaseQualification(key, value)
		},
		"missing digest": func(value *model.ReleaseQualification) {
			value.Artifact = ""
			_ = signReleaseQualification(key, value)
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := receipt
			mutate(&invalid)
			if err := verifyReleaseQualification([]string{key}, invalid); err == nil {
				t.Fatal("invalid qualification was accepted")
			}
		})
	}
}

func TestQualificationIntentBindsFreshEvidenceToTheCurrentWorkload(t *testing.T) {
	candidate := testReleaseCandidate()
	ci := &CIIdentity{Provider: candidate.Provider, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, RepositoryOwnerID: candidate.OwnerID, RepositoryVisibility: candidate.RepositoryVisibility, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, WorkflowRef: candidate.WorkflowRef, WorkflowSHA: candidate.WorkflowSHA, JobWorkflowRef: candidate.SignerWorkflowRef, JobWorkflowSHA: candidate.SignerWorkflowSHA, Ref: candidate.Ref, SHA: candidate.Attestation.MaterialSHA, Intent: "qualify"}
	if allowed, _ := qualificationIntentPermitsCandidate(AccessPrincipal{CI: ci}, candidate); !allowed {
		t.Fatal("matching qualify workflow rejected")
	}
	ci.RunID = "other"
	if allowed, reason := qualificationIntentPermitsCandidate(AccessPrincipal{CI: ci}, candidate); allowed || reason != "qualification_identity_mismatch" {
		t.Fatalf("cross-run qualification accepted: %v %q", allowed, reason)
	}
	ci.Intent = "requalify"
	ci.SHA = strings.Repeat("d", 40) // current protected branch need not equal historic deployment
	if allowed, _ := qualificationIntentPermitsCandidate(AccessPrincipal{CI: ci}, candidate); !allowed {
		t.Fatal("protected requalification rejected for historical evidence")
	}
}

func TestPromotionAndRequalificationRejectCrossRepositoryIdentity(t *testing.T) {
	candidate := testReleaseCandidate()
	ci := &CIIdentity{Provider: candidate.Provider, Repository: "other/repo", RepositoryID: "99", RepositoryOwnerID: candidate.OwnerID, RepositoryVisibility: candidate.RepositoryVisibility, JobWorkflowRef: candidate.SignerWorkflowRef, JobWorkflowSHA: candidate.SignerWorkflowSHA, SHA: candidate.Attestation.MaterialSHA, Intent: "requalify"}
	principal := AccessPrincipal{CI: ci}
	if releasePromotionMatchesPrincipal(candidate, principal) {
		t.Fatal("cross-repository promotion identity accepted")
	}
	if allowed, reason := qualificationIntentPermitsCandidate(principal, candidate); allowed || reason != "qualification_identity_mismatch" {
		t.Fatalf("cross-repository requalification accepted: %v %q", allowed, reason)
	}
	ci.Repository, ci.RepositoryID = candidate.Repository, candidate.RepositoryID
	ci.JobWorkflowSHA = strings.Repeat("d", 40)
	if releasePromotionMatchesPrincipal(candidate, principal) {
		t.Fatal("different reusable signer accepted")
	}
}

func TestReleaseAttestationModeIsDerivedFromVerifiedVisibility(t *testing.T) {
	if got := releaseAttestationMode("private"); got != "github-private" {
		t.Fatalf("private visibility mode = %q", got)
	}
	if got := releaseAttestationMode("internal"); got != "github-private" {
		t.Fatalf("internal visibility mode = %q", got)
	}
	if got := releaseAttestationMode("public"); got != "github-public" {
		t.Fatalf("public visibility mode = %q", got)
	}
	if got := releaseAttestationVerifier("private"); got != "GitHub private Sigstore" {
		t.Fatalf("private verifier = %q", got)
	}
	if got := releaseAttestationVerifier("public"); got != "Sigstore public-good" {
		t.Fatalf("public verifier = %q", got)
	}
}

func TestPromotionQualificationMustMatchURLApp(t *testing.T) {
	receipt := testQualification(t, releaseTestKey('k'))
	if !promotionQualificationMatchesApp("demo", receipt) {
		t.Fatal("matching qualification rejected")
	}
	if promotionQualificationMatchesApp("another-app", receipt) {
		t.Fatal("cross-app qualification accepted")
	}
}

func TestReleaseProvenanceRequiresFullLowercaseSHAAndDigest(t *testing.T) {
	sha := strings.Repeat("a", 40)
	digest := "registry.example.test/demo@sha256:" + strings.Repeat("b", 64)
	if !validReleaseProvenance(sha, digest) {
		t.Fatal("valid provenance rejected")
	}
	if validReleaseProvenance(strings.ToUpper(sha), digest) {
		t.Fatal("uppercase SHA accepted")
	}
	if validReleaseProvenance(sha[:39], digest) {
		t.Fatal("short SHA accepted")
	}
	if validReleaseProvenance(sha, "registry.example.test/demo:latest") {
		t.Fatal("mutable image accepted")
	}
}

func TestReleaseActionsCannotBypassTheProductionPromotionGate(t *testing.T) {
	if !releaseActionEnvironmentAllowed("staging", false) {
		t.Fatal("staging release deployment was rejected")
	}
	if releaseActionEnvironmentAllowed("production", false) {
		t.Fatal("direct production release deployment bypassed signed promotion")
	}
	if !releaseActionEnvironmentAllowed("production", true) {
		t.Fatal("signed production promotion was rejected")
	}
	if releaseActionEnvironmentAllowed("staging", true) {
		t.Fatal("promotion was accepted outside production")
	}
}

func TestQualificationDuplicateRecoveryStaysBoundToTheOriginalRequest(t *testing.T) {
	existing := &model.Operation{
		Kind: "release.qualification",
		App:  "demo",
		Metadata: map[string]interface{}{
			"requestDigest": "sha256:one",
		},
	}
	if !qualificationReplayMatches(existing, "demo", "sha256:one") {
		t.Fatal("matching qualification replay was rejected")
	}
	if qualificationReplayMatches(existing, "demo", "sha256:two") {
		t.Fatal("qualification replay accepted a different deployment request")
	}
	if qualificationReplayMatches(existing, "another-app", "sha256:one") {
		t.Fatal("qualification replay crossed the application boundary")
	}
}

func TestProductionEnvironmentRequiresSignedPromotionForLegacyDeployPaths(t *testing.T) {
	production := &Handler{cfg: &config.Config{Environment: "production"}}
	if !production.productionRequiresSignedPromotion() {
		t.Fatal("production environment did not require signed promotion")
	}
	staging := &Handler{cfg: &config.Config{Environment: "staging"}}
	if staging.productionRequiresSignedPromotion() {
		t.Fatal("staging environment rejected its direct release lane")
	}
}

func TestPromotionIdempotencyBindsTheCompleteQualification(t *testing.T) {
	key := releaseTestKey('k')
	first := testQualification(t, key)
	second := first
	second.ID = "qualification-2"
	if err := signReleaseQualification(key, &second); err != nil {
		t.Fatal(err)
	}
	request := releaseRequest{SourceSHA: first.SourceSHA, Artifact: first.Artifact}
	firstJSON, _ := json.Marshal(releaseIdempotencyPayload(request, &first))
	secondJSON, _ := json.Marshal(releaseIdempotencyPayload(request, &second))
	if bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("different staging qualifications produced the same idempotency payload")
	}
}
