package privateattestation

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
)

func testCandidate(sourceSHA, digest string) model.ReleaseCandidate {
	signerSHA := strings.Repeat("c", 40)
	return model.ReleaseCandidate{
		Provider: "github-actions", Repository: "personal-owner/private-app", RepositoryID: "101", OwnerID: "202", RepositoryVisibility: "private",
		RunID: "303", RunAttempt: "1", WorkflowRef: "personal-owner/private-app/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: sourceSHA,
		SignerWorkflowRef: "personal-owner/norn/.github/workflows/norn-app-release.yml@" + signerSHA, SignerWorkflowSHA: signerSHA, Ref: "refs/heads/main",
		Attestation: model.ReleaseAttestationIdentity{Mode: "norn-signed-private", Verifier: "Norn private DSSE", Issuer: "https://token.actions.githubusercontent.com", SubjectDigest: digest, MaterialSHA: sourceSHA},
	}
}

func TestIssueAndVerifyPortablePrivateEvidence(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := &LocalSigner{private: private, keyID: KeyID(public)}
	verifier, err := NewVerifier([]string{base64.RawStdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := "ghcr.io/personal-owner/private-app@" + digest
	sourceSHA := strings.Repeat("b", 40)
	candidate := testCandidate(sourceSHA, digest)
	sbom := json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx/private-app"}`)
	bundle, err := Issue(context.Background(), signer, "private-app", sourceSHA, artifact, sbom, candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Attestation.Bundle = bundle
	if err := verifier.Verify(context.Background(), artifact, sourceSHA, "private-app", candidate); err != nil {
		t.Fatalf("portable private evidence rejected: %v", err)
	}

	tampered := candidate
	tampered.RepositoryID = "999"
	if err := verifier.Verify(context.Background(), artifact, sourceSHA, "private-app", tampered); err == nil {
		t.Fatal("tampered numeric repository identity accepted")
	}
	encoded, _ := json.Marshal(candidate)
	var tamperedBundle model.ReleaseCandidate
	_ = json.Unmarshal(encoded, &tamperedBundle)
	tamperedBundle.Attestation.Bundle.SBOM.Signatures[0].Sig = base64.RawStdEncoding.EncodeToString([]byte("tampered"))
	if err := verifier.Verify(context.Background(), artifact, sourceSHA, "private-app", tamperedBundle); err == nil {
		t.Fatal("tampered SPDX signature accepted")
	}
	_ = json.Unmarshal(encoded, &tamperedBundle)
	tamperedBundle.Attestation.Bundle.Provenance.Payload = base64.RawStdEncoding.EncodeToString([]byte(`{"_type":"https://in-toto.io/Statement/v1"}`))
	if err := verifier.Verify(context.Background(), artifact, sourceSHA, "private-app", tamperedBundle); err == nil {
		t.Fatal("tampered provenance payload accepted")
	}
}

func TestTrustedKeyRotationAcceptsCurrentAndPreviousEvidence(t *testing.T) {
	oldPublic, oldPrivate, _ := ed25519.GenerateKey(nil)
	newPublic, newPrivate, _ := ed25519.GenerateKey(nil)
	verifier, err := NewVerifier([]string{base64.RawStdEncoding.EncodeToString(oldPublic), base64.RawStdEncoding.EncodeToString(newPublic)})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact, sourceSHA := "ghcr.io/personal-owner/private-app@"+digest, strings.Repeat("b", 40)
	sbom := json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx/private-app"}`)
	for _, key := range []struct {
		public  ed25519.PublicKey
		private ed25519.PrivateKey
	}{{oldPublic, oldPrivate}, {newPublic, newPrivate}} {
		candidate := testCandidate(sourceSHA, digest)
		candidate.Attestation.Bundle, err = Issue(context.Background(), &LocalSigner{private: key.private, keyID: KeyID(key.public)}, "private-app", sourceSHA, artifact, sbom, candidate)
		if err != nil || verifier.Verify(context.Background(), artifact, sourceSHA, "private-app", candidate) != nil {
			t.Fatalf("rotated trusted key rejected: %v", err)
		}
	}
}

func TestLocalKeyAndKMSHelperSecurityChecks(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private.key")
	if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(private)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalSigner(keyPath); err == nil {
		t.Fatal("group/world-readable local signing key accepted")
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalSigner(keyPath); err != nil {
		t.Fatalf("owner-only local signing key rejected: %v", err)
	}
	malformed := append(ed25519.PrivateKey(nil), private...)
	malformed[len(malformed)-1] ^= 1
	if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(malformed)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalSigner(keyPath); err == nil {
		t.Fatal("local signing key with mismatched public half accepted")
	}

	helperPath := filepath.Join(dir, "kms-helper")
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"keyid\":\"wrong\",\"sig\":\"c2ln\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper, err := NewHelperSigner(helperPath, "expected")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Sign(context.Background(), []byte("payload")); err == nil {
		t.Fatal("KMS helper key-ID mismatch accepted")
	}
	t.Setenv("NORN_TEST_CONTROL_SECRET", "must-not-cross-helper-boundary")
	isolatedHelper := "#!/bin/sh\nset -eu\ntest -z \"${NORN_TEST_CONTROL_SECRET:-}\"\ntest \"$PWD\" = /\nprintf '%s\\n' '{\"keyid\":\"expected\",\"sig\":\"c2ln\"}'\n"
	if err := os.WriteFile(helperPath, []byte(isolatedHelper), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Sign(context.Background(), []byte("payload")); err != nil {
		t.Fatalf("isolated KMS helper failed: %v", err)
	}
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	previousTimeout := helperSigningTimeout
	helperSigningTimeout = 50 * time.Millisecond
	started := time.Now()
	_, timeoutErr := helper.Sign(context.Background(), []byte("payload"))
	helperSigningTimeout = previousTimeout
	if timeoutErr == nil || time.Since(started) > time.Second {
		t.Fatalf("KMS helper timeout error=%v duration=%s", timeoutErr, time.Since(started))
	}
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Sign(context.Background(), []byte("payload")); err == nil {
		t.Fatal("KMS helper failure accepted")
	}
}

func TestIssueRejectsOversizedSPDX(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	signer := &LocalSigner{private: private, keyID: KeyID(public)}
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := "ghcr.io/personal-owner/private-app@" + digest
	sourceSHA := strings.Repeat("b", 40)
	sbom := json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx","padding":"` + strings.Repeat("x", MaxSPDXDocumentBytes) + `"}`)
	if _, err := Issue(context.Background(), signer, "private-app", sourceSHA, artifact, sbom, testCandidate(sourceSHA, digest)); err == nil {
		t.Fatal("oversized SPDX document accepted")
	}
}

func TestVerifierRejectsMalformedArtifactWithoutPanic(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(nil)
	verifier, err := NewVerifier([]string{base64.RawStdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	candidate := testCandidate(strings.Repeat("b", 40), "sha256:"+strings.Repeat("a", 64))
	candidate.Attestation.Bundle = &model.ReleaseAttestationBundle{SchemaVersion: model.NornPrivateAttestationSchema, KeyID: KeyID(public)}
	if err := verifier.Verify(context.Background(), "not-an-artifact", candidate.Attestation.MaterialSHA, "private-app", candidate); err == nil {
		t.Fatal("malformed artifact accepted")
	}
}
