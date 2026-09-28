package main

import (
	"fmt"

	"norn/v2/api/config"
	"norn/v2/api/githubattestation"
	"norn/v2/api/pipeline"
	"norn/v2/api/privateattestation"
)

// newEtcdFleetReleaseVerifier uses the same artifact policy as the PG release
// lane. It is created only when the opt-in staging route is enabled; missing
// private trust material fails startup before any release can be accepted.
func newEtcdFleetReleaseVerifier(cfg *config.Config) (*pipeline.Pipeline, error) {
	if cfg == nil || cfg.EnvironmentID() != "staging" || !releasePipelineConfigured(cfg) || cfg.RegistryURL == "" || cfg.OperationReplayTTL > 0 {
		return nil, fmt.Errorf("etcd Fleet staging release policy is incomplete")
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
