package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
)

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
		HCLSHA256: strings.Repeat("c", 64), AttestationURI: "https://evidence.example.test/attestation", SBOMURI: "https://evidence.example.test/sbom",
		Fleet: ExternalFleetExecutionProof{Namespace: "norn-pilot", JobID: "hello-norn-mysql", NomadSubmissionID: "submission-1", ApplyRunID: "123", ApplyRunAttempt: "1", PlanSHA256: strings.Repeat("d", 64), RunnerAttemptID: "attempt-1", CheckpointID: "runtime-1", NonceEvidenceRef: "nonce-1"},
		Chronology: []ExternalFleetChronologyStep{
			{Phase: "prepare", OccurredAt: now, EvidenceRef: "https://evidence.example.test/prepare"},
			{Phase: "migration", OccurredAt: now.Add(time.Second), EvidenceRef: "https://evidence.example.test/migration"},
			{Phase: "runtime", OccurredAt: now.Add(2 * time.Second), EvidenceRef: "https://evidence.example.test/runtime"},
			{Phase: "exercise", OccurredAt: now.Add(3 * time.Second), EvidenceRef: "https://evidence.example.test/exercise"},
		},
	}
}

func externalConfigForTest() ExternalFleetAdmissionConfig {
	return ExternalFleetAdmissionConfig{App: "hello-norn-mysql", Namespace: "norn-pilot", JobID: "hello-norn-mysql", HCLSHA256: strings.Repeat("c", 64)}
}

func verifiedExternalReceipt(receipt ExternalFleetDeploymentReceipt) ExternalFleetDeploymentVerification {
	chronology := append([]ExternalFleetChronologyStep(nil), receipt.Chronology...)
	return ExternalFleetDeploymentVerification{SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, AttestationURI: receipt.AttestationURI, SBOMURI: receipt.SBOMURI, HCLSHA256: receipt.HCLSHA256, Namespace: receipt.Fleet.Namespace, JobID: receipt.Fleet.JobID, NomadSubmissionID: receipt.Fleet.NomadSubmissionID, ApplyRunID: receipt.Fleet.ApplyRunID, ApplyRunAttempt: receipt.Fleet.ApplyRunAttempt, PlanSHA256: receipt.Fleet.PlanSHA256, RunnerAttemptID: receipt.Fleet.RunnerAttemptID, CheckpointID: receipt.Fleet.CheckpointID, NonceEvidenceRef: receipt.Fleet.NonceEvidenceRef, IngressNodeIDs: []string{"ingress-a", "ingress-b"}, PublicHTTPSVersion: "https://pilot.example.test/version", PublicHTTPSReady: "https://pilot.example.test/ready", Chronology: chronology}
}

func TestExternalFleetReceiptValidationFailsClosed(t *testing.T) {
	receipt := externalReceiptForTest()
	if err := validateExternalFleetReceipt(receipt, externalConfigForTest(), receipt.App); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ExternalFleetDeploymentReceipt){
		"wrong schema":  func(value *ExternalFleetDeploymentReceipt) { value.SchemaVersion = "v0" },
		"foreign app":   func(value *ExternalFleetDeploymentReceipt) { value.App = "billing" },
		"mutable image": func(value *ExternalFleetDeploymentReceipt) { value.Artifact = "ghcr.io/acme/hello-norn-mysql:latest" },
		"wrong hcl":     func(value *ExternalFleetDeploymentReceipt) { value.HCLSHA256 = strings.Repeat("e", 64) },
		"credential evidence": func(value *ExternalFleetDeploymentReceipt) {
			value.AttestationURI = "https://token@example.test/evidence"
		},
		"foreign namespace":   func(value *ExternalFleetDeploymentReceipt) { value.Fleet.Namespace = "other" },
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
		"non HTTPS readiness": func(value *ExternalFleetDeploymentVerification) {
			value.PublicHTTPSReady = "http://pilot.example.test/ready"
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
	h := &Handler{cfg: &config.Config{Environment: "staging", ExternalFleetAdmissionApp: "hello-norn-mysql", ExternalFleetAdmissionNamespace: "norn-pilot", ExternalFleetAdmissionJobID: "hello-norn-mysql", ExternalFleetAdmissionHCLSHA256: strings.Repeat("c", 64)}}
	if _, err := h.externalFleetAdmissionConfig("hello-norn-mysql"); err != nil {
		t.Fatalf("complete exact staging config rejected: %v", err)
	}
	for _, mutate := range []func(*config.Config){
		func(value *config.Config) { value.Environment = "production" },
		func(value *config.Config) { value.ExternalFleetAdmissionApp = "other" },
		func(value *config.Config) { value.ExternalFleetAdmissionHCLSHA256 = "short" },
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
