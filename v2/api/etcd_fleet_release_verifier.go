package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"norn/v2/api/config"
	"norn/v2/api/githubattestation"
	"norn/v2/api/pipeline"
	"norn/v2/api/privateattestation"
)

// newEtcdFleetReleaseVerifier uses the same artifact policy as the PG release
// lane. It is created only when the opt-in staging route is enabled; missing
// private trust material fails startup before any release can be accepted.
func newEtcdFleetReleaseVerifier(cfg *config.Config) (*pipeline.Pipeline, error) {
	if err := validateEtcdFleetReleasePolicy(cfg); err != nil {
		return nil, err
	}
	p := &pipeline.Pipeline{RegistryURL: cfg.RegistryURL, Production: cfg.Production(),
		ArtifactSigningPublicKey: cfg.ArtifactSigningPublicKey, ArtifactDenySeverities: cfg.ArtifactDenySeverities,
		CosignPath: cfg.CosignPath, TrivyPath: cfg.TrivyPath, ReleaseAdmissionMode: cfg.ReleaseAdmissionMode,
		ReleaseEnvironment: cfg.EnvironmentID(), ReleaseAttestationTrustMode: cfg.ReleaseAttestationTrustMode,
		ReleaseRegistryAuthFile: cfg.ReleaseAttestationRegistryAuthFile, ReleaseRegistryNodePullReady: cfg.ReleaseRegistryNodePullReady,
		ReleaseAttestationIssuer: cfg.ReleaseAttestationIssuer, ReleaseAttestationRepositories: cfg.ReleaseAttestationRepositories,
		ReleaseAttestationWorkflowRefs: cfg.ReleaseAttestationWorkflowRefs, ReleaseRequireSBOM: cfg.ReleaseRequireSBOM}
	switch cfg.ReleaseAttestationTrustMode {
	case "github-private":
		verifier, err := githubattestation.New(githubattestation.Config{
			AppID: cfg.ReleaseAttestationGitHubAppID, InstallationID: cfg.ReleaseAttestationGitHubInstallationID,
			PrivateKeyFile: cfg.ReleaseAttestationGitHubPrivateKeyFile, RegistryAuthFile: cfg.ReleaseAttestationRegistryAuthFile,
			APIBaseURL: cfg.ReleaseAttestationGitHubAPIBaseURL, GHPath: cfg.ReleaseAttestationGHPath,
		}, nil)
		if err != nil {
			return nil, fmt.Errorf("etcd Fleet private GitHub release verifier: %w", err)
		}
		p.VerifyPrivateKeylessAttestations = verifier.Verify
	case "norn-signed-private":
		verifier, err := privateattestation.NewVerifier(cfg.ReleasePrivateTrustedSigningKeys)
		if err != nil {
			return nil, fmt.Errorf("etcd Fleet Norn private release verifier: %w", err)
		}
		p.VerifyNornPrivateAttestations = verifier.Verify
	case "github-public":
	default:
		return nil, fmt.Errorf("unsupported etcd Fleet release trust mode")
	}
	return p, nil
}

func validateEtcdFleetReleasePolicy(cfg *config.Config) error {
	if cfg == nil || cfg.EnvironmentID() != "staging" || strings.TrimSpace(cfg.RegistryURL) == "" || cfg.OperationReplayTTL > 0 ||
		strings.TrimSpace(cfg.GitHubActionsOIDCAudience) == "" || strings.TrimSpace(cfg.GitHubActionsDefaultBranch) == "" ||
		len(cfg.GitHubActionsReleaseBindings) == 0 || len(cfg.GitHubActionsAllowedWorkflowRefs) == 0 ||
		len(cfg.GitHubActionsAllowedRefs) == 0 || len(cfg.GitHubActionsAllowedEvents) == 0 ||
		len(cfg.GitHubActionsAllowedEnvironments) == 0 || len(cfg.ArtifactDenySeverities) == 0 ||
		strings.TrimSpace(cfg.CosignPath) == "" || strings.TrimSpace(cfg.TrivyPath) == "" {
		return fmt.Errorf("etcd Fleet staging release policy is incomplete")
	}
	if cfg.Production() && (!filepath.IsAbs(cfg.CosignPath) || !filepath.IsAbs(cfg.TrivyPath)) {
		return fmt.Errorf("etcd Fleet release verifier commands must be absolute in the production profile")
	}
	if (cfg.ReleaseAttestationTrustMode == "github-private" || cfg.ReleaseAttestationTrustMode == "norn-signed-private") && !cfg.ReleaseRegistryNodePullReady {
		return fmt.Errorf("etcd Fleet private release requires provisioned scheduler registry pull access")
	}
	switch cfg.ReleaseAdmissionMode {
	case "keyed":
		if cfg.ReleaseAttestationTrustMode != "github-public" || strings.TrimSpace(cfg.ArtifactSigningPublicKey) == "" {
			return fmt.Errorf("etcd Fleet keyed release requires a public source and signing key")
		}
	case "keyless", "attested":
		if cfg.ReleaseAttestationIssuer != "https://token.actions.githubusercontent.com" ||
			len(cfg.ReleaseAttestationRepositories) == 0 || len(cfg.ReleaseAttestationWorkflowRefs) == 0 || !cfg.ReleaseRequireSBOM {
			return fmt.Errorf("etcd Fleet attestation policy is incomplete")
		}
		if (cfg.ReleaseAdmissionMode == "keyless" && cfg.ReleaseAttestationTrustMode == "norn-signed-private") ||
			(cfg.ReleaseAdmissionMode == "attested" && cfg.ReleaseAttestationTrustMode != "norn-signed-private") {
			return fmt.Errorf("etcd Fleet release mode and trust root differ")
		}
	default:
		return fmt.Errorf("etcd Fleet release admission mode is unsupported")
	}
	return nil
}
