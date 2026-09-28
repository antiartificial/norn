package main

import (
	"testing"
	"time"

	"norn/v2/api/config"
)

func TestEtcdFleetReleaseVerifierPreflightUsesActualStagingPolicy(t *testing.T) {
	cfg := &config.Config{Environment: "staging", EnvironmentExplicit: true, RegistryURL: "ghcr.io/acme",
		GitHubActionsOIDCAudience: "norn-staging", GitHubActionsDefaultBranch: "main",
		GitHubActionsReleaseBindings:     []string{"demo=acme/demo@11@22"},
		GitHubActionsAllowedWorkflowRefs: []string{"acme/norn/.github/workflows/release.yml@sha"},
		GitHubActionsAllowedRefs:         []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"},
		GitHubActionsAllowedEnvironments: []string{"staging"},
		ArtifactDenySeverities:           []string{"HIGH"}, CosignPath: "cosign", TrivyPath: "trivy",
		ReleaseAdmissionMode: "keyed", ReleaseAttestationTrustMode: "github-public", ArtifactSigningPublicKey: "public-key"}
	if verifier, err := newEtcdFleetReleaseVerifier(cfg); err != nil || verifier == nil {
		t.Fatalf("staging verifier requires unrelated qualification signer: verifier=%v err=%v", verifier, err)
	}
	cfg.OperationReplayTTL = time.Minute
	if _, err := newEtcdFleetReleaseVerifier(cfg); err == nil {
		t.Fatal("expiring replay identity enabled a mutable deployment")
	}
	cfg.OperationReplayTTL = 0
	cfg.ArtifactSigningPublicKey = ""
	if _, err := newEtcdFleetReleaseVerifier(cfg); err == nil {
		t.Fatal("keyed release started without a signing key")
	}
	cfg.ArtifactSigningPublicKey = "public-key"
	cfg.ReleaseAdmissionMode = "keyless"
	if _, err := newEtcdFleetReleaseVerifier(cfg); err == nil {
		t.Fatal("keyless release started without attestation policy")
	}
	cfg.ReleaseAttestationIssuer = "https://token.actions.githubusercontent.com"
	cfg.ReleaseAttestationRepositories = []string{"acme/demo"}
	cfg.ReleaseAttestationWorkflowRefs = []string{"acme/norn/.github/workflows/release.yml@sha"}
	cfg.ReleaseRequireSBOM = true
	if verifier, err := newEtcdFleetReleaseVerifier(cfg); err != nil || verifier == nil {
		t.Fatalf("complete keyless staging verifier=%v err=%v", verifier, err)
	}
}
