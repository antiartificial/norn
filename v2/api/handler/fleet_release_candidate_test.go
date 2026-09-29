package handler

import (
	"strings"
	"testing"

	"norn/v2/api/model"
)

func TestBindFleetStagingReleaseCandidateUsesVerifiedCIOnly(t *testing.T) {
	sha := strings.Repeat("a", 40)
	workflowSHA := strings.Repeat("c", 40)
	signerSHA := strings.Repeat("d", 40)
	artifact := "ghcr.io/acme/demo@sha256:" + strings.Repeat("b", 64)
	principal := AccessPrincipal{Source: AccessPrincipalSourceManagedToken, App: "demo", Environment: "staging",
		Scopes: []string{ScopeReleaseStage}, CI: &CIIdentity{Provider: "github-actions", Repository: "acme/demo",
			RepositoryID: "11", RepositoryOwnerID: "22", RepositoryVisibility: "public", RunID: "33", RunAttempt: "1",
			WorkflowRef: "acme/demo/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: workflowSHA,
			JobWorkflowRef: "acme/norn/.github/workflows/norn-app-release.yml@" + signerSHA, JobWorkflowSHA: signerSHA,
			Ref: "refs/heads/main", RefProtected: true, EventName: "push", Environment: "staging", Intent: "stage", SHA: sha}}
	supplied := model.ReleaseCandidate{Provider: "attacker", Repository: "attacker/other"}
	supplied.Attestation.ProvenanceURI = "https://example.test/provenance"
	candidate, err := BindFleetStagingReleaseCandidate(principal, "demo", "main", sha, artifact, "github-public", supplied)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Provider != "github-actions" || candidate.Repository != "acme/demo" || candidate.Attestation.MaterialSHA != sha ||
		candidate.Attestation.ProvenanceURI != supplied.Attestation.ProvenanceURI || candidate.Attestation.SubjectDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("candidate was not derived from verified CI: %+v", candidate)
	}
	checks := []struct {
		name   string
		change func(*AccessPrincipal)
	}{
		{"wrong app", func(p *AccessPrincipal) { p.App = "other" }},
		{"wrong environment", func(p *AccessPrincipal) { p.Environment = "production" }},
		{"wrong intent", func(p *AccessPrincipal) { p.CI.Intent = "attest" }},
		{"unprotected ref", func(p *AccessPrincipal) { p.CI.RefProtected = false }},
		{"wrong source", func(p *AccessPrincipal) { p.CI.SHA = strings.Repeat("e", 40) }},
		{"not managed", func(p *AccessPrincipal) { p.Source = AccessPrincipalSourceSharedAPI }},
		{"no stage scope", func(p *AccessPrincipal) { p.Scopes = []string{ScopeAPIRead} }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			copy := principal
			ci := *principal.CI
			copy.CI = &ci
			check.change(&copy)
			if _, err := BindFleetStagingReleaseCandidate(copy, "demo", "main", sha, artifact, "github-public", supplied); err == nil {
				t.Fatal("unbound CI identity admitted Fleet release")
			}
		})
	}
}
