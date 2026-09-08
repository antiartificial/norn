package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang/snappy"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type externalSafeVerifierError string

func (e externalSafeVerifierError) Error() string                         { return string(e) }
func (e externalSafeVerifierError) SafeExternalVerificationError() string { return string(e) }

type externalFleetAttestationVerifierFunc func(context.Context, string, ExternalFleetDeploymentVerificationRequest) error

func (fn externalFleetAttestationVerifierFunc) Verify(ctx context.Context, token string, request ExternalFleetDeploymentVerificationRequest) error {
	return fn(ctx, token, request)
}

type externalFleetGitHubRunVerifierFunc func(context.Context, string, CIIdentity) error

func (fn externalFleetGitHubRunVerifierFunc) Verify(ctx context.Context, token string, ci CIIdentity) error {
	return fn(ctx, token, ci)
}

type externalFleetRoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn externalFleetRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func externalReceiptForTest() ExternalFleetDeploymentReceipt {
	now := time.Now().UTC().Truncate(time.Second).Add(-4 * time.Second)
	candidate := testReleaseCandidate()
	candidate.Repository = "acme/hello-norn-mysql"
	candidate.Attestation.MaterialSHA = strings.Repeat("a", 40)
	candidate.Attestation.ProvenanceURI = "https://evidence.example.test/attestation"
	candidate.Attestation.SBOMURI = "https://evidence.example.test/sbom"
	return ExternalFleetDeploymentReceipt{
		SchemaVersion: externalFleetReceiptSchema, Nonce: "00000000-0000-4000-8000-000000000001." + strings.Repeat("a", 64), App: "hello-norn-mysql",
		SourceSHA: strings.Repeat("a", 40), Artifact: "ghcr.io/acme/hello-norn-mysql@sha256:" + strings.Repeat("b", 64), Candidate: candidate,
		AttestationBundleSHA256: strings.Repeat("1", 64), SBOMBundleSHA256: strings.Repeat("2", 64),
		Fleet: ExternalFleetExecutionProof{Namespace: "norn-pilot", Migration: ExternalFleetNomadJobProof{JobID: "hello-norn-mysql-migrate", HCLSHA256: strings.Repeat("c", 64), EvalID: "00000000-0000-4000-8000-000000000011", JobModifyIndex: 11, CheckpointID: "migration-1"}, Runtime: ExternalFleetNomadJobProof{JobID: "hello-norn-mysql", HCLSHA256: strings.Repeat("e", 64), EvalID: "00000000-0000-4000-8000-000000000012", JobModifyIndex: 12, CheckpointID: "runtime-1"}, PlanID: "plan-1", ApplyRunID: "123", ApplyRunAttempt: "1", PlanSHA256: strings.Repeat("d", 64), RunnerAttemptID: "attempt-1", RootAttemptID: "attempt-root", NonceEvidenceRef: "nonce-1"},
		Chronology: []ExternalFleetChronologyStep{
			{Phase: "prepare", OccurredAt: now, EvidenceRef: "https://evidence.example.test/prepare"},
			{Phase: "migration", OccurredAt: now.Add(time.Second), EvidenceRef: "https://evidence.example.test/migration"},
			{Phase: "runtime", OccurredAt: now.Add(2 * time.Second), EvidenceRef: "https://evidence.example.test/runtime"},
			{Phase: "exercise", OccurredAt: now.Add(3 * time.Second), EvidenceRef: "https://evidence.example.test/exercise"},
		},
	}
}

func externalConfigForTest() ExternalFleetAdmissionConfig {
	return ExternalFleetAdmissionConfig{App: "hello-norn-mysql", Namespace: "norn-pilot", MigrationJobID: "hello-norn-mysql-migrate", MigrationHCLSHA256: strings.Repeat("c", 64), RuntimeJobID: "hello-norn-mysql", RuntimeHCLSHA256: strings.Repeat("e", 64), BootstrapSignerRef: "acme/hello-norn-mysql/.github/workflows/hello-norn-mysql-bootstrap-image.yml@" + strings.Repeat("f", 40)}
}

func verifiedExternalReceipt(receipt ExternalFleetDeploymentReceipt) ExternalFleetDeploymentVerification {
	chronology := append([]ExternalFleetChronologyStep(nil), receipt.Chronology...)
	return ExternalFleetDeploymentVerification{SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, AttestationBundleSHA256: receipt.AttestationBundleSHA256, SBOMBundleSHA256: receipt.SBOMBundleSHA256, Namespace: receipt.Fleet.Namespace, Migration: receipt.Fleet.Migration, Runtime: receipt.Fleet.Runtime, PlanID: receipt.Fleet.PlanID, ApplyRunID: receipt.Fleet.ApplyRunID, ApplyRunAttempt: receipt.Fleet.ApplyRunAttempt, PlanSHA256: receipt.Fleet.PlanSHA256, RunnerAttemptID: receipt.Fleet.RunnerAttemptID, NonceEvidenceRef: receipt.Fleet.NonceEvidenceRef, IngressNodeIDs: []string{"ingress-a", "ingress-b"}, PublicHTTPSVersion: "https://pilot.example.test/version", PrivateReadiness: ExternalFleetPrivateReadiness{Endpoint: "https://private.example.test/readyz", AllocationIDs: []string{"alloc-a", "alloc-b"}, CheckedAt: time.Now().UTC()}, Chronology: chronology, Regions: []ExternalFleetRegionProof{{Region: "global", NomadRegion: "global", EvalID: "00000000-0000-4000-8000-000000000012", DesiredWeight: 100, ActiveWeight: 100}}}
}

func TestExternalFleetReceiptValidationFailsClosed(t *testing.T) {
	receipt := externalReceiptForTest()
	if err := validateExternalFleetReceipt(receipt, externalConfigForTest(), receipt.App); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ExternalFleetDeploymentReceipt){
		"wrong schema":                 func(value *ExternalFleetDeploymentReceipt) { value.SchemaVersion = "v0" },
		"foreign app":                  func(value *ExternalFleetDeploymentReceipt) { value.App = "billing" },
		"mutable image":                func(value *ExternalFleetDeploymentReceipt) { value.Artifact = "ghcr.io/acme/hello-norn-mysql:latest" },
		"wrong runtime hcl":            func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Runtime.HCLSHA256 = strings.Repeat("f", 64) },
		"invalid bundle digest":        func(value *ExternalFleetDeploymentReceipt) { value.AttestationBundleSHA256 = "short" },
		"foreign namespace":            func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Namespace = "other" },
		"invalid migration submission": func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Migration.EvalID = "not-a-uuid" },
		"shared evaluation":            func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Runtime.EvalID = value.Fleet.Migration.EvalID },
		"shared checkpoint": func(value *ExternalFleetDeploymentReceipt) {
			value.Fleet.Runtime.CheckpointID = value.Fleet.Migration.CheckpointID
		},
		"missing nonce proof": func(value *ExternalFleetDeploymentReceipt) { value.Fleet.NonceEvidenceRef = "" },
		"unordered chronology": func(value *ExternalFleetDeploymentReceipt) {
			value.Chronology[2].OccurredAt = value.Chronology[1].OccurredAt
		},
		"missing exercise": func(value *ExternalFleetDeploymentReceipt) { value.Chronology = value.Chronology[:3] },
	} {
		t.Run(name, func(t *testing.T) {
			value := externalReceiptForTest()
			mutate(&value)
			if err := validateExternalFleetReceipt(value, externalConfigForTest(), "hello-norn-mysql"); err == nil {
				t.Fatal("unsafe external receipt accepted")
			}
		})
	}
}

func TestExternalFleetChronologyIsFreshAgainstInjectedNonceClock(t *testing.T) {
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	receipt := externalReceiptForTest()
	for index := range receipt.Chronology {
		receipt.Chronology[index].OccurredAt = now.Add(time.Duration(index-4) * time.Second)
	}
	if err := validateExternalFleetReceiptAt(receipt, externalConfigForTest(), receipt.App, now); err != nil {
		t.Fatalf("fresh chronology rejected: %v", err)
	}
	if err := validateExternalFleetReceiptAt(receipt, externalConfigForTest(), receipt.App, now.Add(externalFleetAdmissionNonceTTL+time.Second)); err == nil {
		t.Fatal("chronology outside the nonce window was accepted")
	}
	verified := verifiedExternalReceipt(receipt)
	verified.PrivateReadiness.CheckedAt = now
	if err := verificationMatchesExternalReceiptAt(verified, receipt, externalConfigForTest(), now); err != nil {
		t.Fatalf("fresh verifier chronology rejected: %v", err)
	}
	if err := verificationMatchesExternalReceiptAt(verified, receipt, externalConfigForTest(), now.Add(externalFleetAdmissionNonceTTL+time.Second)); err == nil {
		t.Fatal("verifier accepted chronology outside the injected nonce window")
	}
}

func TestExternalFleetLiveVerifierUsesOnlyRedactedNonceAndCanonicalEvidence(t *testing.T) {
	receipt := externalReceiptForTest()
	request := ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "1"}, Config: externalConfigForTest()}
	nonce, err := externalAdmissionNonceFromReceipt(receipt.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/external-fleet/evidence":
			var got externalFleetEvidenceRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Receipt.Nonce != "" || got.NonceSHA256 != nonce.sha256() || got.Repository != request.CI.Repository {
				t.Fatalf("unsafe evidence request: %#v", got)
			}
			verification := verifiedExternalReceipt(receipt)
			verification.PublicHTTPSVersion = server.URL + "/version"
			verification.PrivateReadiness = ExternalFleetPrivateReadiness{Endpoint: "https://private.example.test/readyz", AllocationIDs: []string{"alloc-a", "alloc-b"}, CheckedAt: time.Now().UTC()}
			_ = json.NewEncoder(w).Encode(externalFleetEvidence{
				SchemaVersion: "norn.external-fleet-evidence/v1", Repository: request.CI.Repository, Verification: verification,
				FixtureHCLSHA256: map[string]string{"migration": request.Config.MigrationHCLSHA256, "runtime": request.Config.RuntimeHCLSHA256},
				Canonical:        map[string]string{"prepare.tlsRouting": "sha256:prepare", "migration.migration": "sha256:migration", "runtime.update": "sha256:runtime", "readiness.consulNomad": "sha256:ready"},
				PlanAttemptID:    receipt.Fleet.RunnerAttemptID, CheckpointAttemptID: receipt.Fleet.RunnerAttemptID, NonceSHA256: nonce.sha256(), NonceWrittenAt: time.Now().Add(-time.Second), NonceReadAt: time.Now(),
				Attempt:     externalFleetAttemptEvidence{PlanID: receipt.Fleet.PlanID, AttemptID: receipt.Fleet.RunnerAttemptID, RootAttemptID: receipt.Fleet.RootAttemptID, Revision: 1, TerminalStatus: "running", CurrentPhase: "complete", SourceDispatchRunID: receipt.Fleet.ApplyRunID, WorkflowURL: "https://github.com/" + request.CI.Repository + "/actions/runs/" + request.CI.RunID, RetryLineage: []string{receipt.Fleet.RootAttemptID, receipt.Fleet.RunnerAttemptID}},
				Checkpoints: []externalFleetCheckpointEvidence{{ID: "prepare-1", Phase: "prepare", Status: "succeeded", EvidenceSHA256: strings.Repeat("1", 64), AttemptID: receipt.Fleet.RunnerAttemptID}, {ID: receipt.Fleet.Migration.CheckpointID, Phase: "migration", Status: "succeeded", EvidenceSHA256: strings.Repeat("2", 64), AttemptID: receipt.Fleet.RunnerAttemptID}, {ID: receipt.Fleet.Runtime.CheckpointID, Phase: "runtime", Status: "succeeded", EvidenceSHA256: strings.Repeat("3", 64), AttemptID: receipt.Fleet.RunnerAttemptID}, {ID: "exercise-1", Phase: "exercise", Status: "succeeded", EvidenceSHA256: strings.Repeat("4", 64), AttemptID: receipt.Fleet.RunnerAttemptID}},
				Allocations: []externalFleetAllocationEvidence{{AllocationID: "alloc-a", JobID: receipt.Fleet.Runtime.JobID, EvalID: receipt.Fleet.Runtime.EvalID, Namespace: receipt.Fleet.Namespace, NodeID: "ingress-a", Region: "global", NomadStatus: "running", ConsulStatus: "passing"}, {AllocationID: "alloc-b", JobID: receipt.Fleet.Runtime.JobID, EvalID: receipt.Fleet.Runtime.EvalID, Namespace: receipt.Fleet.Namespace, NodeID: "ingress-b", Region: "global", NomadStatus: "running", ConsulStatus: "passing"}},
			})
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": receipt.SourceSHA, "allocation": "alloc-a", "region": "global"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	evidenceURL, _ := url.Parse(server.URL)
	newToken := func(t *testing.T) string {
		file, err := os.CreateTemp(t.TempDir(), "token")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("test-token"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file.Name(), 0o600); err != nil {
			t.Fatal(err)
		}
		return file.Name()
	}
	evidenceToken, githubToken := newToken(t), newToken(t)
	verifier := &ExternalFleetDeploymentLiveVerifier{evidenceURL: evidenceURL, publicURL: evidenceURL, evidenceTokenFile: evidenceToken, githubTokenFile: githubToken, httpClient: server.Client(), githubRun: externalFleetGitHubRunVerifierFunc(func(_ context.Context, gotToken string, gotCI CIIdentity) error {
		if gotToken != "test-token" || gotCI.RunAttempt != request.CI.RunAttempt {
			t.Fatal("GitHub run verifier did not receive the authenticated attempt")
		}
		return nil
	}), attest: externalFleetAttestationVerifierFunc(func(_ context.Context, gotToken string, gotRequest ExternalFleetDeploymentVerificationRequest) error {
		if gotToken != "test-token" || gotRequest.Receipt.Artifact != receipt.Artifact {
			t.Fatal("attestation verifier did not receive bounded identity")
		}
		return nil
	})}
	verified, err := verifier.VerifyExternalFleetDeployment(context.Background(), request)
	if err != nil {
		t.Fatalf("live verifier rejected complete fake evidence: %v", err)
	}
	if verified.PlanID != receipt.Fleet.PlanID || verified.Migration != receipt.Fleet.Migration {
		t.Fatalf("verification = %#v", verified)
	}
}

// This checked-in fixture is the cross-repository contract consumed by the
// Fleet bridge. Keep it free of credentials and raw nonce material.
func TestExternalFleetEvidenceV1FixtureContract(t *testing.T) {
	contents, err := os.ReadFile("testdata/external-fleet-evidence-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	// Fleet's final bridge commit 7ae090e asserts this same byte hash. Keeping
	// it here makes accidental cross-repository fixture drift fail closed.
	if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != "ff54aa118c9b9712ebf57cdbf6794502cf2ca9de2792423ad72e47e9debd0633" {
		t.Fatalf("Fleet evidence fixture SHA-256 = %s", got)
	}
	decoded, err := decodeExternalFleetJSON(strings.NewReader(string(contents)), externalFleetEvidenceMaxBody, new(externalFleetEvidence))
	if err != nil {
		t.Fatal(err)
	}
	evidence := decoded.(*externalFleetEvidence)
	if evidence.SchemaVersion != "norn.external-fleet-evidence/v1" || len(evidence.Checkpoints) != 4 || len(evidence.Allocations) != 2 || evidence.Attempt.TerminalStatus != "running" || evidence.Attempt.CurrentPhase != "complete" {
		t.Fatalf("fixture does not preserve required bridge contract: %#v", evidence)
	}
	receipt := externalReceiptForTest()
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	receipt.Chronology = []ExternalFleetChronologyStep{{Phase: "prepare", OccurredAt: base, EvidenceRef: "https://evidence.example.test/prepare"}, {Phase: "migration", OccurredAt: base.Add(time.Second), EvidenceRef: "https://evidence.example.test/migration"}, {Phase: "runtime", OccurredAt: base.Add(2 * time.Second), EvidenceRef: "https://evidence.example.test/runtime"}, {Phase: "exercise", OccurredAt: base.Add(3 * time.Second), EvidenceRef: "https://evidence.example.test/exercise"}}
	nonce := externalAdmissionNonce{ID: "00000000-0000-4000-8000-000000000001", Secret: strings.Repeat("a", 64)}
	if evidence.NonceSHA256 != nonce.sha256() {
		t.Fatal("fixture nonce digest is not bound to the canonical fixture nonce")
	}
	if err := validateExternalFleetEvidenceAt(*evidence, ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: CIIdentity{Repository: evidence.Repository, RunID: receipt.Fleet.ApplyRunID}, Config: externalConfigForTest()}, nonce.sha256(), evidence.NonceReadAt.Add(time.Minute)); err != nil {
		t.Fatalf("fresh-clock fixture failed production validation: %v", err)
	}
	if !validExternalFleetAllocations(evidence.Allocations, receipt, evidence.Verification, externalFleetPublicVersion{Allocation: "alloc-a", Region: "global"}) {
		t.Fatal("fixture allocations failed production allocation validation")
	}
}

func TestExternalFleetAdmissionIdempotencySurvivesTransientRotation(t *testing.T) {
	receipt := externalReceiptForTest()
	principal := AccessPrincipal{TokenID: "rotating-jti", Environment: "staging", CI: &CIIdentity{Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "1"}}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Idempotency-Key", "stable-client-key")
	key, digest, ok := externalFleetAdmissionIdempotency(httptest.NewRecorder(), request, principal, receipt.App, receipt)
	if !ok {
		t.Fatal("logical idempotency rejected")
	}
	rotated := receipt
	rotated.Nonce = "00000000-0000-4000-8000-000000000002." + strings.Repeat("f", 64)
	rotated.Fleet.ApplyRunID, rotated.Fleet.ApplyRunAttempt, rotated.Fleet.RunnerAttemptID = "456", "2", "attempt-2"
	rotated.Fleet.Migration.EvalID, rotated.Fleet.Runtime.EvalID = "00000000-0000-4000-8000-000000000021", "00000000-0000-4000-8000-000000000022"
	rotated.Fleet.Migration.JobModifyIndex, rotated.Fleet.Runtime.JobModifyIndex = 21, 22
	rotated.Fleet.Migration.CheckpointID, rotated.Fleet.Runtime.CheckpointID = "migration-2", "runtime-2"
	principal.TokenID, principal.CI.RunID, principal.CI.RunAttempt = "new-jti", "456", "2"
	rotatedKey, rotatedDigest, ok := externalFleetAdmissionIdempotency(httptest.NewRecorder(), request, principal, rotated.App, rotated)
	if !ok || key != rotatedKey || digest != rotatedDigest {
		t.Fatal("transient replay identity changed")
	}
	rotated.Artifact = "ghcr.io/acme/hello-norn-mysql@sha256:" + strings.Repeat("c", 64)
	_, changedDigest, ok := externalFleetAdmissionIdempotency(httptest.NewRecorder(), request, principal, rotated.App, rotated)
	if !ok || digest == changedDigest {
		t.Fatal("changed logical candidate was replayable")
	}
	rotated = receipt
	rotated.Fleet.PlanSHA256 = strings.Repeat("f", 64)
	_, changedDigest, ok = externalFleetAdmissionIdempotency(httptest.NewRecorder(), request, principal, rotated.App, rotated)
	if !ok || digest == changedDigest {
		t.Fatal("changed immutable plan hash was replayable")
	}
	rotated = receipt
	rotated.Fleet.Namespace = "other"
	_, changedDigest, ok = externalFleetAdmissionIdempotency(httptest.NewRecorder(), request, principal, rotated.App, rotated)
	if !ok || digest == changedDigest {
		t.Fatal("changed immutable namespace was replayable")
	}
}

func TestExternalFleetVerifiedAttestationOutputBindsOneExactCandidateAttempt(t *testing.T) {
	good := []byte(`[{"verificationResult":{"statement":{"predicateType":"https://slsa.dev/provenance/v1"},"signature":{"certificate":{"runInvocationURI":"https://github.com/acme/hello-norn-mysql/actions/runs/77/attempts/3"}},"verifiedTimestamps":[{}]}}]`)
	if !externalFleetVerifiedAttestationOutput(good, "https://slsa.dev/provenance/v1", "acme/hello-norn-mysql", "77", "3") {
		t.Fatal("exact verified statement rejected")
	}
	for _, bad := range [][]byte{[]byte(`[]`), []byte(`[{"verificationResult":{"statement":{"predicateType":"https://spdx.dev/Document/v2.3"},"signature":{"certificate":{"runInvocationURI":"https://github.com/acme/hello-norn-mysql/actions/runs/77/attempts/3"}},"verifiedTimestamps":[{}]}}]`), []byte(`[{"verificationResult":{"statement":{"predicateType":"https://slsa.dev/provenance/v1"},"signature":{"certificate":{"runInvocationURI":"https://github.com/acme/hello-norn-mysql/actions/runs/77/attempts/3"}},"verifiedTimestamps":[{}]}},{"verificationResult":{"statement":{"predicateType":"https://slsa.dev/provenance/v1"},"signature":{"certificate":{"runInvocationURI":"https://github.com/acme/hello-norn-mysql/actions/runs/77/attempts/3"}},"verifiedTimestamps":[{}]}}]`)} {
		if externalFleetVerifiedAttestationOutput(bad, "https://slsa.dev/provenance/v1", "acme/hello-norn-mysql", "77", "3") {
			t.Fatal("ambiguous or wrong statement accepted")
		}
	}
}

func TestExternalFleetReadOnlyGitHubAppMintsOnlyAuditedInstallation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/9":
			io.WriteString(w, `{"repository_selection":"selected","permissions":{"metadata":"read","actions":"read","attestations":"read"}}`)
		case "/app/installations/9/access_tokens":
			io.WriteString(w, `{"token":"ephemeral","expires_at":"2099-01-01T00:00:00Z"}`)
		case "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer ephemeral" {
				t.Fatal("repository audit was not installation-token authenticated")
			}
			io.WriteString(w, `{"total_count":1,"repositories":[{"id":42}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := t.TempDir() + "/app.pem"
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := newExternalFleetGitHubApp(externalFleetGitHubAppConfig{AppID: "7", InstallationID: 9, PrivateKeyFile: path, RepositoryIDs: []string{"42"}, APIBaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if token, err := client.token(context.Background()); err != nil || token != "ephemeral" {
		t.Fatalf("mint=%q err=%v", token, err)
	}
}

func TestExternalFleetLiveGitHubAttestationCapabilityShape(t *testing.T) {
	const slsa = "https://slsa.dev/provenance/v1"
	const spdx = "https://spdx.dev/Document/v2.3"
	digest := strings.Repeat("b", 64)
	makeBundle := func(predicate string) []byte {
		statement, err := json.Marshal(map[string]any{"predicateType": predicate, "subject": []any{map[string]any{"digest": map[string]string{"sha256": digest}}}})
		if err != nil {
			t.Fatal(err)
		}
		return []byte("{\n  \"dsseEnvelope\": {\"payload\": \"" + base64.StdEncoding.EncodeToString(statement) + "\"}\n}\n")
	}
	slsaRaw, spdxRaw := makeBundle(slsa), makeBundle(spdx)
	oldSLSARaw := []byte(strings.Replace(string(slsaRaw), "\n}\n", ",\"historical\":true\n}\n", 1))
	hash := func(raw []byte) string {
		value, err := externalFleetCanonicalBundleDigest(raw)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "" || r.Header.Get("X-GitHub-Api-Version") != "" {
			t.Error("storage request carried GitHub credentials or headers")
		}
		w.Header().Set("Content-Type", "application/x-snappy")
		if strings.HasPrefix(r.URL.Path, "/provenance-old") {
			_, _ = w.Write(snappy.Encode(nil, oldSLSARaw))
		} else if strings.HasPrefix(r.URL.Path, "/provenance") {
			_, _ = w.Write(snappy.Encode(nil, slsaRaw))
		} else {
			_, _ = w.Write(snappy.Encode(nil, spdxRaw))
		}
	}))
	defer storage.Close()
	var list *httptest.Server
	list = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer installation-token" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("GitHub list request was not exact 2026-03-10 authentication")
		}
		filter, cursor := r.URL.Query().Get("predicate_type"), r.URL.Query().Get("before")
		if r.URL.Query().Get("page") != "" {
			t.Error("attestation pagination must use GitHub cursors, not page numbers")
		}
		if filter == "provenance" && cursor == "" {
			items := make([]any, 0, externalFleetMaxAttestations)
			for index := 0; index < externalFleetMaxAttestations; index++ {
				items = append(items, map[string]any{"repository_id": 1, "bundle_url": storage.URL + "/provenance-old/" + strconv.Itoa(index) + "?opaque=capability", "initiator": map[string]any{"login": "norn"}})
			}
			w.Header().Set("Link", "<"+list.URL+r.URL.Path+"?per_page=30&predicate_type=provenance&before=older>; rel=\"next\"")
			_ = json.NewEncoder(w).Encode(map[string]any{"attestations": items})
			return
		}
		path := "/provenance?opaque=capability"
		if filter == "sbom" {
			path = "/spdx?opaque=capability"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"attestations": []any{map[string]any{"repository_id": 1, "bundle_url": storage.URL + path, "initiator": map[string]any{"login": "norn"}}}})
	}))
	defer list.Close()
	receipt := externalReceiptForTest()
	receipt.Candidate.Repository = "acme/app"
	receipt.Candidate.RepositoryID = "1"
	receipt.AttestationBundleSHA256, receipt.SBOMBundleSHA256 = hash(slsaRaw), hash(spdxRaw)
	app := &externalFleetGitHubApp{cfg: externalFleetGitHubAppConfig{APIBaseURL: list.URL, RepositoryIDs: []string{"1"}, StorageHosts: []string{"127.0.0.1"}}, client: list.Client(), storageClient: storage.Client()}
	got, err := app.attestations(context.Background(), "installation-token", receipt)
	if err != nil || !bytes.Equal(got[slsa], slsaRaw) || !bytes.Equal(got[spdx], spdxRaw) {
		t.Fatalf("live-shape bundles did not preserve exact raw bytes: %v", err)
	}

	for name, mutate := range map[string]func(*ExternalFleetDeploymentReceipt, *externalFleetGitHubApp){
		"wrong repository ID": func(r *ExternalFleetDeploymentReceipt, _ *externalFleetGitHubApp) { r.Candidate.RepositoryID = "2" },
		"wrong stable bundle digest": func(r *ExternalFleetDeploymentReceipt, _ *externalFleetGitHubApp) {
			r.AttestationBundleSHA256 = strings.Repeat("0", 64)
		},
		"unreviewed storage host": func(_ *ExternalFleetDeploymentReceipt, a *externalFleetGitHubApp) {
			a.cfg.StorageHosts = []string{"example.invalid"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyReceipt, copyApp := receipt, *app
			mutate(&copyReceipt, &copyApp)
			_, err := copyApp.attestations(context.Background(), "installation-token", copyReceipt)
			if err == nil || strings.Contains(err.Error(), "opaque=capability") || strings.Contains(err.Error(), "installation-token") {
				t.Fatalf("invalid live capability binding was accepted or leaked: %v", err)
			}
		})
	}
}

func TestExternalFleetAttestationCursorPaginationFailsClosed(t *testing.T) {
	const slsa = "https://slsa.dev/provenance/v1"
	digest := strings.Repeat("b", 64)
	makeBundle := func(marker string) []byte {
		statement, err := json.Marshal(map[string]any{"predicateType": slsa, "subject": []any{map[string]any{"digest": map[string]string{"sha256": digest}}}, "marker": marker})
		if err != nil {
			t.Fatal(err)
		}
		return []byte(`{"dsseEnvelope":{"payload":"` + base64.StdEncoding.EncodeToString(statement) + `"}}`)
	}
	desired, historical := makeBundle("desired"), makeBundle("historical")
	desiredDigest, err := externalFleetCanonicalBundleDigest(desired)
	if err != nil {
		t.Fatal(err)
	}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-snappy")
		if strings.Contains(r.URL.Path, "desired") {
			_, _ = w.Write(snappy.Encode(nil, desired))
			return
		}
		_, _ = w.Write(snappy.Encode(nil, historical))
	}))
	defer storage.Close()

	for _, scenario := range []string{"cursor loop", "path drift", "cursor overflow", "duplicate desired"} {
		t.Run(scenario, func(t *testing.T) {
			var list *httptest.Server
			list = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cursor := r.URL.Query().Get("before")
				if r.URL.Query().Get("predicate_type") != "provenance" {
					t.Fatal("unexpected predicate request after a failed provenance scan")
				}
				bundlePath := "/historical-" + cursor
				nextCursor := "next-" + cursor
				switch scenario {
				case "cursor loop":
					if cursor == "" {
						nextCursor = "loop"
					} else {
						nextCursor = "loop"
					}
				case "path drift":
					w.Header().Set("Link", "<"+list.URL+"/repos/acme/other/attestations/sha256:"+digest+"?per_page=30&predicate_type=provenance&before=drift>; rel=\"next\"")
					_ = json.NewEncoder(w).Encode(map[string]any{"attestations": []any{map[string]any{"repository_id": 1, "bundle_url": storage.URL + bundlePath + "?opaque=capability"}}})
					return
				case "duplicate desired":
					bundlePath = "/desired-" + cursor
					if cursor == "" {
						nextCursor = "duplicate"
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"attestations": []any{map[string]any{"repository_id": 1, "bundle_url": storage.URL + bundlePath + "?opaque=capability"}}})
						return
					}
				}
				w.Header().Set("Link", "<"+list.URL+r.URL.Path+"?per_page=30&predicate_type=provenance&before="+nextCursor+">; rel=\"next\"")
				_ = json.NewEncoder(w).Encode(map[string]any{"attestations": []any{map[string]any{"repository_id": 1, "bundle_url": storage.URL + bundlePath + "?opaque=capability"}}})
			}))
			defer list.Close()
			receipt := externalReceiptForTest()
			receipt.Candidate.Repository, receipt.Candidate.RepositoryID = "acme/app", "1"
			receipt.AttestationBundleSHA256 = desiredDigest
			receipt.SBOMBundleSHA256 = strings.Repeat("2", 64)
			app := &externalFleetGitHubApp{cfg: externalFleetGitHubAppConfig{APIBaseURL: list.URL, RepositoryIDs: []string{"1"}, StorageHosts: []string{"127.0.0.1"}}, client: list.Client(), storageClient: storage.Client()}
			if _, err := app.attestations(context.Background(), "installation-token", receipt); err == nil || strings.Contains(err.Error(), "opaque=capability") {
				t.Fatalf("%s was accepted or leaked a capability: %v", scenario, err)
			}
		})
	}
}

func TestExternalFleetAttestationNextLinkRejectsMalformedOrDriftedTargets(t *testing.T) {
	digest := strings.Repeat("b", 64)
	app := &externalFleetGitHubApp{cfg: externalFleetGitHubAppConfig{APIBaseURL: "https://api.github.example"}}
	valid := "https://api.github.example/repos/acme/app/attestations/sha256:" + digest + "?per_page=30&predicate_type=provenance&before=cursor"
	if path, found, err := app.nextAttestationListPath([]string{"<" + valid + ">; rel=\"next\""}, "acme/app", digest, "provenance"); err != nil || !found || !strings.Contains(path, "before=cursor") {
		t.Fatalf("valid GitHub cursor link rejected: path=%q found=%t err=%v", path, found, err)
	}
	for name, link := range map[string]string{
		"malformed":     "not-a-link",
		"multiple next": "<" + valid + ">; rel=\"next\", <" + valid + ">; rel=\"next\"",
		"origin drift":  "<https://api.attacker.example/repos/acme/app/attestations/sha256:" + digest + "?per_page=30&predicate_type=provenance&before=cursor>; rel=\"next\"",
		"path drift":    "<https://api.github.example/repos/acme/other/attestations/sha256:" + digest + "?per_page=30&predicate_type=provenance&before=cursor>; rel=\"next\"",
		"query drift":   "<https://api.github.example/repos/acme/app/attestations/sha256:" + digest + "?per_page=30&predicate_type=provenance&page=2&before=cursor>; rel=\"next\"",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := app.nextAttestationListPath([]string{link}, "acme/app", digest, "provenance"); err == nil {
				t.Fatal("unsafe Link target was accepted")
			}
		})
	}
}

func TestExternalFleetCanonicalBundleDigestIsWhitespaceInsensitiveAndMatchesHandoff(t *testing.T) {
	pretty := []byte("{\n  \"mediaType\": \"application/vnd.dev.sigstore.bundle+json;version=0.3\",\n  \"dsseEnvelope\": { \"payload\": \"c2lnbmVkLWNvbnRlbnQ=\" },\n  \"verificationMaterial\": {\"tlogEntries\": []}\n}\n")
	minified := []byte(`{"dsseEnvelope":{"payload":"c2lnbmVkLWNvbnRlbnQ="},"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3","verificationMaterial":{"tlogEntries":[]}}`)
	withNewline := append(append([]byte(nil), minified...), '\n')
	want, err := externalFleetCanonicalBundleDigest(pretty)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{minified, withNewline} {
		got, err := externalFleetCanonicalBundleDigest(raw)
		if err != nil || got != want {
			t.Fatalf("canonical digest changed with whitespace: %q %v", got, err)
		}
	}
	changed := []byte(`{"dsseEnvelope":{"payload":"ZGlmZmVyZW50LXNpZ25lZC1jb250ZW50"},"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3","verificationMaterial":{"tlogEntries":[]}}`)
	got, err := externalFleetCanonicalBundleDigest(changed)
	if err != nil || got == want {
		t.Fatalf("semantic bundle change did not change digest: %q %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, pretty, 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := exec.Command("python3", "../../scripts/canonical-sigstore-bundle-digest", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if handoffDigest := strings.TrimSpace(string(canonical)); handoffDigest != want {
		t.Fatalf("workflow canonical digest = %s, Go verifier = %s", handoffDigest, want)
	}
}

func TestExternalFleetCanonicalBundleDigestRejectsCrossRuntimeAmbiguity(t *testing.T) {
	valid := []byte(`{"bundle":"café","nested":{"count":1}}`)
	want, err := externalFleetCanonicalBundleDigest(valid)
	if err != nil {
		t.Fatalf("valid canonical-profile bundle rejected: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("python3", "../../scripts/canonical-sigstore-bundle-digest", path).Output()
	if err != nil || strings.TrimSpace(string(output)) != want {
		t.Fatalf("publisher digest %q err=%v, want %q", output, err, want)
	}
	maxInteger := []byte(`{"count":18446744073709551615}`)
	maxDigest, err := externalFleetCanonicalBundleDigest(maxInteger)
	if err != nil {
		t.Fatalf("uint64-max canonical integer rejected: %v", err)
	}
	maxPath := filepath.Join(t.TempDir(), "max-integer.json")
	if err := os.WriteFile(maxPath, maxInteger, 0o600); err != nil {
		t.Fatal(err)
	}
	maxOutput, err := exec.Command("python3", "../../scripts/canonical-sigstore-bundle-digest", maxPath).Output()
	if err != nil || strings.TrimSpace(string(maxOutput)) != maxDigest {
		t.Fatalf("publisher uint64-max digest %q err=%v, want %q", maxOutput, err, maxDigest)
	}
	for name, raw := range map[string][]byte{
		"fractional number":     []byte(`{"count":1.0}`),
		"negative number":       []byte(`{"count":-1}`),
		"overflow integer":      []byte(`{"count":18446744073709551616}`),
		"oversized integer":     []byte(`{"count":100000000000000000000}`),
		"leading zero":          []byte(`{"count":01}`),
		"exponent":              []byte(`{"count":1e0}`),
		"HTML-sensitive string": []byte(`{"value":"<"}`),
		"line separator":        []byte("{\"value\":\"\\u2028\"}"),
		"duplicate key":         []byte(`{"value":1,"value":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := externalFleetCanonicalBundleDigest(raw); err == nil {
				t.Fatal("ambiguous bundle representation was accepted")
			}
			candidate := filepath.Join(t.TempDir(), "bundle.json")
			if err := os.WriteFile(candidate, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("python3", "../../scripts/canonical-sigstore-bundle-digest", candidate).Run(); err == nil {
				t.Fatal("publisher canonicalizer accepted an ambiguous representation")
			}
		})
	}
}

func TestExternalFleetCommandVerifierUsesExactRawBundleFile(t *testing.T) {
	tmp := t.TempDir()
	raw := json.RawMessage(`{ "dsseEnvelope" : { "payload" : "cHJlc2VydmUtbWU=" } }`)
	expected := filepath.Join(tmp, "expected.json")
	if err := os.WriteFile(expected, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	output := func(predicate string) string {
		return `[{"verificationResult":{"statement":{"predicateType":"` + predicate + `"},"signature":{"certificate":{"runInvocationURI":"https://github.com/acme/app/actions/runs/3/attempts/1"}},"verifiedTimestamps":[{}]}}]`
	}
	path := filepath.Join(tmp, "fake-gh")
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$last\" = --bundle ]; then cmp -s \"$arg\" \"" + expected + "\" || exit 9; fi; last=$arg; done\ncase \"$*\" in *provenance*) echo '" + output("https://slsa.dev/provenance/v1") + "';; *) echo '" + output("https://spdx.dev/Document/v2.3") + "';; esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := externalReceiptForTest()
	receipt.Candidate.Repository = "acme/app"
	receipt.Candidate.RunID = "3"
	receipt.Candidate.RunAttempt = "1"
	bundles := map[string]json.RawMessage{"https://slsa.dev/provenance/v1": raw, "https://spdx.dev/Document/v2.3": raw}
	if err := (externalFleetCommandAttestationVerifier{path: path}).VerifyBundles(context.Background(), "installation-token", ExternalFleetDeploymentVerificationRequest{Receipt: receipt}, bundles); err != nil {
		t.Fatalf("exact raw bundle file rejected: %v", err)
	}
}

func TestExternalFleetGitHubRunVerifierBindsExactAttemptAndWorkflow(t *testing.T) {
	client := &http.Client{Transport: externalFleetRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/repos/acme/norn-fleet/actions/runs/123" || request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected GitHub request: %s", request.URL)
		}
		body := `{"id":123,"run_attempt":2,"status":"in_progress","conclusion":null,"event":"workflow_dispatch","path":".github/workflows/apply.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_branch":"main","repository":{"full_name":"acme/norn-fleet"},"head_repository":{"full_name":"acme/norn-fleet"},"new_github_field":"ignored"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	identity := CIIdentity{Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "2", SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40)}
	if err := (externalFleetGitHubRunVerifier{client: client}).Verify(context.Background(), "token", identity); err != nil {
		t.Fatalf("matching GitHub run rejected: %v", err)
	}
	identity.RunAttempt = "3"
	if err := (externalFleetGitHubRunVerifier{client: client}).Verify(context.Background(), "token", identity); err == nil {
		t.Fatal("foreign GitHub attempt accepted")
	}
}

func TestExternalReceiptBindsApplyRunAndAttemptToAuthenticatedCI(t *testing.T) {
	receipt := externalReceiptForTest()
	ci := CIIdentity{RunID: receipt.Fleet.ApplyRunID, RunAttempt: receipt.Fleet.ApplyRunAttempt}
	if !externalReceiptMatchesCI(receipt, ci) {
		t.Fatal("matching protected Fleet run was rejected")
	}
	ci.RunAttempt = "2"
	if externalReceiptMatchesCI(receipt, ci) {
		t.Fatal("receipt from another Fleet run attempt was accepted")
	}
}

func TestExternalVerificationMustMatchEveryReceiptBinding(t *testing.T) {
	receipt := externalReceiptForTest()
	verified := verifiedExternalReceipt(receipt)
	if err := verificationMatchesExternalReceipt(verified, receipt, externalConfigForTest()); err != nil {
		t.Fatalf("matching independent evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ExternalFleetDeploymentVerification){
		"different artifact": func(value *ExternalFleetDeploymentVerification) {
			value.Artifact = "ghcr.io/acme/other@sha256:" + strings.Repeat("b", 64)
		},
		"one ingress node": func(value *ExternalFleetDeploymentVerification) { value.IngressNodeIDs = []string{"ingress-a"} },
		"duplicate ingress node": func(value *ExternalFleetDeploymentVerification) {
			value.IngressNodeIDs = []string{"ingress-a", "ingress-a"}
		},
		"private readiness is public": func(value *ExternalFleetDeploymentVerification) {
			value.PrivateReadiness.Endpoint = "https://pilot.example.test/ready"
		},
		"missing nonce evidence": func(value *ExternalFleetDeploymentVerification) { value.NonceEvidenceRef = "different" },
		"different chronology": func(value *ExternalFleetDeploymentVerification) {
			value.Chronology[3].EvidenceRef = "https://evidence.example.test/forged"
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := verifiedExternalReceipt(receipt)
			mutate(&value)
			if err := verificationMatchesExternalReceipt(value, receipt, externalConfigForTest()); err == nil {
				t.Fatal("non-matching verifier evidence accepted")
			}
		})
	}
}

func TestExternalAdmissionRequiresOnlyExactScopedFleetIdentity(t *testing.T) {
	principal := AccessPrincipal{Subject: "github-actions:acme/norn-fleet:123", Scopes: []string{ScopeFleetExternalAdmission}, App: "hello-norn-mysql", Environment: "staging", CI: &CIIdentity{Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "1", Environment: "staging", RefProtected: true, Intent: "apply"}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/hello-norn-mysql/external-deployments", nil)
	if _, ok := requireExternalFleetAdmissionScope(httptest.NewRecorder(), WithAccessPrincipal(request, &principal), "hello-norn-mysql"); !ok {
		t.Fatal("exact external admission principal rejected")
	}
	for name, mutate := range map[string]func(*AccessPrincipal){
		"legacy token":       func(value *AccessPrincipal) { value.Legacy = true },
		"admin token":        func(value *AccessPrincipal) { value.Scopes = []string{ScopeAdmin} },
		"fleet operate only": func(value *AccessPrincipal) { value.Scopes = []string{ScopeFleetOperate} },
		"wrong app":          func(value *AccessPrincipal) { value.App = "other" },
		"unprotected ref":    func(value *AccessPrincipal) { value.CI.RefProtected = false },
		"wrong intent":       func(value *AccessPrincipal) { value.CI.Intent = "plan" },
		"wrong environment":  func(value *AccessPrincipal) { value.Environment, value.CI.Environment = "production", "production" },
	} {
		t.Run(name, func(t *testing.T) {
			value := principal
			ci := *principal.CI
			value.CI = &ci
			mutate(&value)
			recorder := httptest.NewRecorder()
			if _, ok := requireExternalFleetAdmissionScope(recorder, WithAccessPrincipal(request, &value), "hello-norn-mysql"); ok || recorder.Code != http.StatusForbidden {
				t.Fatalf("unauthorized identity accepted: ok=%v status=%d", ok, recorder.Code)
			}
		})
	}
}

func TestExternalAdmissionDisabledWithoutCompleteExactConfiguration(t *testing.T) {
	h := &Handler{cfg: &config.Config{Environment: "staging", ExternalFleetAdmissionApp: "hello-norn-mysql", ExternalFleetAdmissionNamespace: "norn-pilot", ExternalFleetAdmissionMigrationJobID: "hello-norn-mysql-migrate", ExternalFleetAdmissionMigrationHCLSHA256: strings.Repeat("c", 64), ExternalFleetAdmissionRuntimeJobID: "hello-norn-mysql", ExternalFleetAdmissionRuntimeHCLSHA256: strings.Repeat("e", 64), ExternalFleetAdmissionBootstrapSignerRef: "acme/hello-norn-mysql/.github/workflows/hello-norn-mysql-bootstrap-image.yml@" + strings.Repeat("f", 40)}}
	if _, err := h.externalFleetAdmissionConfig("hello-norn-mysql"); err != nil {
		t.Fatalf("complete exact staging config rejected: %v", err)
	}
	for _, mutate := range []func(*config.Config){
		func(value *config.Config) { value.ExternalFleetAdmissionApp = "other" },
		func(value *config.Config) { value.ExternalFleetAdmissionRuntimeHCLSHA256 = "short" },
		func(value *config.Config) {
			value.ExternalFleetAdmissionRuntimeJobID = value.ExternalFleetAdmissionMigrationJobID
		},
		func(value *config.Config) {
			value.ExternalFleetAdmissionRuntimeHCLSHA256 = value.ExternalFleetAdmissionMigrationHCLSHA256
		},
		func(value *config.Config) {
			value.ReleaseAttestationWorkflowRefs = []string{value.ExternalFleetAdmissionBootstrapSignerRef}
		},
	} {
		copy := *h.cfg
		mutate(&copy)
		if _, err := (&Handler{cfg: &copy}).externalFleetAdmissionConfig("hello-norn-mysql"); err == nil {
			t.Fatal("incomplete or cross-lane config enabled external admission")
		}
	}
}

func TestExternalAdmissionNonceFormatRejectsForgedValues(t *testing.T) {
	valid := "00000000-0000-4000-8000-000000000001." + strings.Repeat("a", 64)
	if _, err := externalAdmissionNonceFromReceipt(valid); err != nil {
		t.Fatalf("valid nonce rejected: %v", err)
	}
	for _, invalid := range []string{"", "not-a-uuid." + strings.Repeat("a", 64), "00000000-0000-4000-8000-000000000001.short", "00000000-0000-4000-8000-000000000001." + strings.Repeat("A", 64)} {
		if _, err := externalAdmissionNonceFromReceipt(invalid); err == nil {
			t.Fatalf("invalid nonce accepted: %q", invalid)
		}
	}
}

func TestExternalAdmissionDoesNotIssueNonceWithoutLiveVerifier(t *testing.T) {
	h := &Handler{db: &store.DB{}, cfg: &config.Config{Environment: "staging", ExternalFleetAdmissionApp: "hello-norn-mysql", ExternalFleetAdmissionNamespace: "norn-pilot", ExternalFleetAdmissionMigrationJobID: "hello-norn-mysql-migrate", ExternalFleetAdmissionMigrationHCLSHA256: strings.Repeat("c", 64), ExternalFleetAdmissionRuntimeJobID: "hello-norn-mysql", ExternalFleetAdmissionRuntimeHCLSHA256: strings.Repeat("e", 64), ExternalFleetAdmissionBootstrapSignerRef: "acme/hello-norn-mysql/.github/workflows/hello-norn-mysql-bootstrap-image.yml@" + strings.Repeat("f", 40)}}
	principal := AccessPrincipal{Scopes: []string{ScopeFleetExternalAdmission}, App: "hello-norn-mysql", Environment: "staging", CI: &CIIdentity{Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "1", Environment: "staging", RefProtected: true, Intent: "apply"}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/hello-norn-mysql/external-deployments", strings.NewReader(`{"action":"issue-nonce"}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "hello-norn-mysql")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	h.AdmitExternalFleetDeployment(recorder, WithAccessPrincipal(request, &principal))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "external_deployment_verifier_unavailable") {
		t.Fatalf("nil verifier nonce issuance status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestExternalAdmissionProofNeverRetainsOrReturnsRawNonce(t *testing.T) {
	receipt := externalReceiptForTest()
	proof := redactExternalFleetReceipt(receipt)
	if proof.Nonce != "" {
		t.Fatal("redacted proof retains raw nonce")
	}
	canonical, err := externalReceiptCanonicalJSON(receipt)
	if err != nil || strings.Contains(string(canonical), receipt.Nonce) {
		t.Fatalf("canonical durable proof leaked raw nonce: %s err=%v", canonical, err)
	}
	encoded, err := json.Marshal(map[string]interface{}{"externalFleetProof": proof, "nonceSHA256": externalAdmissionNonce{ID: "00000000-0000-4000-8000-000000000001", Secret: strings.Repeat("a", 64)}.sha256()})
	if err != nil || strings.Contains(string(encoded), receipt.Nonce) || strings.Contains(string(encoded), "."+strings.Repeat("a", 64)) {
		t.Fatalf("durable proof leaked raw nonce: %s err=%v", encoded, err)
	}
	nonce := externalAdmissionNonce{ID: receipt.Nonce[:36], Secret: receipt.Nonce[37:]}
	if got := safeExternalVerificationError(errors.New("mysql://secret@example.test"), nonce); got != "independent evidence was rejected" {
		t.Fatalf("untyped verifier error leaked: %q", got)
	}
	// Nested evidence and DSSE maps are caller controlled too. Matching the
	// simple identifier grammar is not sufficient because a nonce does.
	proof.Fleet.NonceEvidenceRef = receipt.Nonce
	proof.Candidate.Attestation.Bundle = &model.ReleaseAttestationBundle{Provenance: model.DSSEEnvelope{Payload: receipt.Nonce[37:]}}
	if !externalValueContainsNonce(proof, nonce) {
		t.Fatal("recursive nonce scanner missed nested raw nonce material")
	}
	for _, leaked := range []externalSafeVerifierError{
		externalSafeVerifierError("Nomad read failed for " + receipt.Nonce + " during verification"),
		externalSafeVerifierError("Consul evidence contains secret=" + nonce.Secret + " (adapter context)"),
	} {
		if got := safeExternalVerificationError(leaked, nonce); got != "independent evidence was rejected" {
			t.Fatalf("typed verifier error leaked nonce material: %q", got)
		}
	}
	if got := safeExternalVerificationError(externalSafeVerifierError("Nomad allocation missing after independent lookup"), nonce); got != "Nomad allocation missing after independent lookup" {
		t.Fatalf("safe verifier detail was unnecessarily discarded: %q", got)
	}
}

func TestExternalBootstrapSignerIsSeparatelyPinned(t *testing.T) {
	receipt := externalReceiptForTest()
	configured := externalConfigForTest()
	if validExternalBootstrapCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, "github-public", configured) {
		t.Fatal("normal release signer was accepted as the bootstrap signer")
	}
	receipt.Candidate.SignerWorkflowRef = configured.BootstrapSignerRef
	receipt.Candidate.SignerWorkflowSHA = configured.BootstrapSignerRef[strings.LastIndex(configured.BootstrapSignerRef, "@")+1:]
	if !validExternalBootstrapCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, "github-public", configured) {
		t.Fatal("exact separately pinned bootstrap signer was rejected")
	}
	receipt.Candidate.SignerWorkflowSHA = strings.Repeat("a", 40)
	if validExternalBootstrapCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, "github-public", configured) {
		t.Fatal("bootstrap signer SHA mismatch was accepted")
	}
}

func TestBootstrapSignerAdoptionSupportsQualificationAndPromotionPolicy(t *testing.T) {
	candidate := externalReceiptForTest().Candidate
	configured := externalConfigForTest()
	candidate.SignerWorkflowRef = configured.BootstrapSignerRef
	candidate.SignerWorkflowSHA = configured.BootstrapSignerRef[strings.LastIndex(configured.BootstrapSignerRef, "@")+1:]
	h := &Handler{cfg: &config.Config{Environment: "production", ExternalFleetAdmissionApp: configured.App, ExternalFleetAdmissionNamespace: configured.Namespace, ExternalFleetAdmissionMigrationJobID: configured.MigrationJobID, ExternalFleetAdmissionMigrationHCLSHA256: configured.MigrationHCLSHA256, ExternalFleetAdmissionRuntimeJobID: configured.RuntimeJobID, ExternalFleetAdmissionRuntimeHCLSHA256: configured.RuntimeHCLSHA256, ExternalFleetAdmissionBootstrapSignerRef: configured.BootstrapSignerRef}}
	ci := &CIIdentity{Provider: candidate.Provider, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, RepositoryOwnerID: candidate.OwnerID, RepositoryVisibility: candidate.RepositoryVisibility, Intent: "requalify", JobWorkflowRef: "acme/hello-norn-mysql/.github/workflows/requalify.yml@" + strings.Repeat("d", 40), JobWorkflowSHA: strings.Repeat("d", 40)}
	principal := AccessPrincipal{CI: ci}
	if !h.externalBootstrapCandidateForApp(configured.App, candidate) {
		t.Fatal("server-pinned bootstrap candidate was not usable for promotion verification")
	}
	if allowed, reason := h.qualificationIntentPermitsCandidate(configured.App, principal, candidate); !allowed || reason != "" {
		t.Fatalf("separate protected requalification could not adopt bootstrap evidence: allowed=%v reason=%q", allowed, reason)
	}
	if releasePromotionMatchesPrincipal(candidate, principal) || !releaseRepositoryMatchesPrincipal(candidate, principal) {
		t.Fatal("bootstrap promotion compatibility did not preserve distinct normal and bootstrap signer boundaries")
	}
}
