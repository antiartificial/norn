package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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
	now := time.Now().UTC().Truncate(time.Second)
	candidate := testReleaseCandidate()
	candidate.Repository = "acme/hello-norn-mysql"
	candidate.Attestation.MaterialSHA = strings.Repeat("a", 40)
	candidate.Attestation.ProvenanceURI = "https://evidence.example.test/attestation"
	candidate.Attestation.SBOMURI = "https://evidence.example.test/sbom"
	return ExternalFleetDeploymentReceipt{
		SchemaVersion: externalFleetReceiptSchema, Nonce: "00000000-0000-4000-8000-000000000001." + strings.Repeat("a", 64), App: "hello-norn-mysql",
		SourceSHA: strings.Repeat("a", 40), Artifact: "ghcr.io/acme/hello-norn-mysql@sha256:" + strings.Repeat("b", 64), Candidate: candidate,
		AttestationURI: "https://evidence.example.test/attestation", SBOMURI: "https://evidence.example.test/sbom",
		Fleet: ExternalFleetExecutionProof{Namespace: "norn-pilot", Migration: ExternalFleetNomadJobProof{JobID: "hello-norn-mysql-migrate", HCLSHA256: strings.Repeat("c", 64), EvalID: "00000000-0000-4000-8000-000000000011", JobModifyIndex: 11, CheckpointID: "migration-1"}, Runtime: ExternalFleetNomadJobProof{JobID: "hello-norn-mysql", HCLSHA256: strings.Repeat("e", 64), EvalID: "00000000-0000-4000-8000-000000000012", JobModifyIndex: 12, CheckpointID: "runtime-1"}, PlanID: "plan-1", ApplyRunID: "123", ApplyRunAttempt: "1", PlanSHA256: strings.Repeat("d", 64), RunnerAttemptID: "attempt-1", NonceEvidenceRef: "nonce-1"},
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
	return ExternalFleetDeploymentVerification{SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, AttestationURI: receipt.AttestationURI, SBOMURI: receipt.SBOMURI, Namespace: receipt.Fleet.Namespace, Migration: receipt.Fleet.Migration, Runtime: receipt.Fleet.Runtime, PlanID: receipt.Fleet.PlanID, ApplyRunID: receipt.Fleet.ApplyRunID, ApplyRunAttempt: receipt.Fleet.ApplyRunAttempt, PlanSHA256: receipt.Fleet.PlanSHA256, RunnerAttemptID: receipt.Fleet.RunnerAttemptID, NonceEvidenceRef: receipt.Fleet.NonceEvidenceRef, IngressNodeIDs: []string{"ingress-a", "ingress-b"}, PublicHTTPSVersion: "https://pilot.example.test/version", PrivateReadiness: ExternalFleetPrivateReadiness{Endpoint: "https://private.example.test/readyz", AllocationIDs: []string{"alloc-a", "alloc-b"}, CheckedAt: time.Now().UTC()}, Chronology: chronology, Regions: []ExternalFleetRegionProof{{Region: "global", NomadRegion: "global", EvalID: "00000000-0000-4000-8000-000000000012", DesiredWeight: 100, ActiveWeight: 100}}}
}

func TestExternalFleetReceiptValidationFailsClosed(t *testing.T) {
	receipt := externalReceiptForTest()
	if err := validateExternalFleetReceipt(receipt, externalConfigForTest(), receipt.App); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ExternalFleetDeploymentReceipt){
		"wrong schema":      func(value *ExternalFleetDeploymentReceipt) { value.SchemaVersion = "v0" },
		"foreign app":       func(value *ExternalFleetDeploymentReceipt) { value.App = "billing" },
		"mutable image":     func(value *ExternalFleetDeploymentReceipt) { value.Artifact = "ghcr.io/acme/hello-norn-mysql:latest" },
		"wrong runtime hcl": func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Runtime.HCLSHA256 = strings.Repeat("f", 64) },
		"credential evidence": func(value *ExternalFleetDeploymentReceipt) {
			value.AttestationURI = "https://token@example.test/evidence"
		},
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
			_ = json.NewEncoder(w).Encode(externalFleetEvidence{Repository: request.CI.Repository, Verification: verification, FixtureHCLSHA256: map[string]string{"migration": request.Config.MigrationHCLSHA256, "runtime": request.Config.RuntimeHCLSHA256}, Canonical: map[string]string{"prepare.tlsRouting": "sha256:prepare", "migration.migration": "sha256:migration", "runtime.update": "sha256:runtime", "readiness.consulNomad": "sha256:ready"}, PlanAttemptID: receipt.Fleet.RunnerAttemptID, CheckpointAttemptID: receipt.Fleet.RunnerAttemptID, NonceSHA256: nonce.sha256(), NonceWrittenAt: time.Now().Add(-time.Second), NonceReadAt: time.Now()})
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": receipt.SourceSHA, "allocation": "alloc-a", "region": "global"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	evidenceURL, _ := url.Parse(server.URL)
	token := func(t *testing.T) string {
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
	}(t)
	verifier := &ExternalFleetDeploymentLiveVerifier{evidenceURL: evidenceURL, publicURL: evidenceURL, evidenceTokenFile: token, githubTokenFile: token, httpClient: server.Client(), githubRun: externalFleetGitHubRunVerifierFunc(func(_ context.Context, gotToken string, gotCI CIIdentity) error {
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

func TestExternalFleetGitHubRunVerifierBindsExactAttemptAndWorkflow(t *testing.T) {
	client := &http.Client{Transport: externalFleetRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/repos/acme/norn-fleet/actions/runs/123" || request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected GitHub request: %s", request.URL)
		}
		body := `{"id":123,"run_attempt":2,"status":"completed","conclusion":"success","event":"workflow_dispatch","path":".github/workflows/apply.yml","repository":{"full_name":"acme/norn-fleet"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	identity := CIIdentity{Repository: "acme/norn-fleet", RunID: "123", RunAttempt: "2", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40)}
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
