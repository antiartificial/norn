package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ReleaseCandidate binds a Norn deployment to the CI identity that selected
// its immutable source and artifact. It is evidence, not caller authority.
type ReleaseCandidate struct {
	Provider     string `json:"provider"`
	Repository   string `json:"repository"`
	RepositoryID string `json:"repositoryId"`
	OwnerID      string `json:"ownerId"`
	// RepositoryVisibility is copied from the verified GitHub OIDC assertion by
	// Norn. It is evidence used to select public versus private Sigstore trust;
	// callers cannot authorize it through release JSON.
	RepositoryVisibility string `json:"repositoryVisibility,omitempty"`
	RunID                string `json:"runId"`
	RunAttempt           string `json:"runAttempt"`
	WorkflowRef          string `json:"workflowRef"`
	WorkflowSHA          string `json:"workflowSha"`
	// SignerWorkflow* identify the SHA-pinned reusable workflow that signed
	// the release attestations. Workflow* remain the caller identity.
	SignerWorkflowRef string                     `json:"signerWorkflowRef,omitempty"`
	SignerWorkflowSHA string                     `json:"signerWorkflowSha,omitempty"`
	Ref               string                     `json:"ref"`
	Attestation       ReleaseAttestationIdentity `json:"attestation"`
}

type ReleaseAttestationIdentity struct {
	// Mode is set only by the server from verified CI visibility. It prevents a
	// private source from being verified against the public trust root.
	Mode string `json:"mode"`
	// Verifier is a server-derived display label only; it contains no identity,
	// endpoint, token, or credential material.
	Verifier      string `json:"verifier,omitempty"`
	ProvenanceURI string `json:"provenanceUri,omitempty"`
	SBOMURI       string `json:"sbomUri,omitempty"`
	Issuer        string `json:"issuer"`
	SubjectDigest string `json:"subjectDigest"`
	MaterialSHA   string `json:"materialSha"`
	// Bundle carries portable Norn-signed provenance and SPDX statements for
	// ordinary private repositories. GitHub-backed trust adapters leave it nil.
	Bundle *ReleaseAttestationBundle `json:"bundle,omitempty"`
}

const NornPrivateAttestationSchema = "norn.private-release-attestation/v1"

// ReleaseAttestationBundle is embedded in the staging qualification so the
// production control plane can verify private evidence without GitHub API or
// transparency-log access.
type ReleaseAttestationBundle struct {
	SchemaVersion string       `json:"schemaVersion"`
	KeyID         string       `json:"keyId"`
	Provenance    DSSEEnvelope `json:"provenance"`
	SBOM          DSSEEnvelope `json:"sbom"`
}

type DSSESignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}
type DSSEEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []DSSESignature `json:"signatures"`
}

// ReleaseQualification is a portable, immutable attestation emitted by a
// staging control plane after a successful deployment. Production verifies the
// signature using a separately configured trusted staging key before it queues
// the exact source/artifact pair.
type ReleaseQualification struct {
	SchemaVersion string           `json:"schemaVersion"`
	ID            string           `json:"id"`
	App           string           `json:"app"`
	Environment   string           `json:"environment"`
	DeploymentID  string           `json:"deploymentId"`
	SourceSHA     string           `json:"sourceSha"`
	Artifact      string           `json:"artifact"`
	IssuedAt      time.Time        `json:"issuedAt"`
	ExpiresAt     time.Time        `json:"expiresAt"`
	KeyID         string           `json:"keyId"`
	Signature     string           `json:"signature"`
	Candidate     ReleaseCandidate `json:"candidate,omitempty"`
	DSSE          DSSEEnvelope     `json:"dsse,omitempty"`
}

// ValidateReleaseSpecBinding binds CI evidence to the server-owned app spec.
// It is intentionally usable by both the API queue boundary and a worker: a
// release must not execute if its source or artifact namespace is changed
// after it was accepted into the operation queue.
func ValidateReleaseSpecBinding(spec *InfraSpec, candidate ReleaseCandidate, artifact, registryURL string) error {
	if spec == nil || spec.Repo == nil {
		return fmt.Errorf("release app has no server-owned repository source")
	}
	sourceRepository, ok := CanonicalGitHubRepository(spec.Repo.URL)
	if !ok {
		return fmt.Errorf("release app repository must be a canonical github.com HTTPS or SSH URL")
	}
	if sourceRepository != strings.ToLower(strings.TrimSpace(candidate.Repository)) {
		return fmt.Errorf("release candidate repository does not match the app source repository")
	}
	wantRepository, ok := ReleaseArtifactRepository(spec, registryURL)
	if !ok {
		return fmt.Errorf("release app has no server-authorized artifact repository")
	}
	gotRepository, ok := OCIRepository(artifact)
	if !ok || gotRepository != wantRepository {
		return fmt.Errorf("release artifact repository does not match the app's authorized repository")
	}
	return nil
}

// CanonicalGitHubRepository accepts only direct github.com HTTPS or SSH
// repository URLs. It removes the transport-specific .git suffix while
// retaining the owner/repository identity used by GitHub OIDC evidence.
func CanonicalGitHubRepository(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "git+"))
	if strings.HasPrefix(raw, "git@github.com:") {
		return canonicalGitHubRepositoryPath(strings.TrimPrefix(raw, "git@github.com:"))
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || (parsed.Scheme != "https" && parsed.Scheme != "ssh") {
		return "", false
	}
	if parsed.Scheme == "https" && (parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
		return "", false
	}
	return canonicalGitHubRepositoryPath(parsed.Path)
}

func canonicalGitHubRepositoryPath(path string) (string, bool) {
	path = strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(path), true
}

// ReleaseArtifactRepository derives the sole allowed OCI repository from the
// server-owned InfraSpec. Dockerfile builds use the control-plane registry and
// app ID; pinned build images retain their explicit repository.
func ReleaseArtifactRepository(spec *InfraSpec, registryURL string) (string, bool) {
	if spec != nil && spec.Build != nil && strings.TrimSpace(spec.Build.Image) != "" {
		return OCIRepository(spec.Build.Image)
	}
	registryURL = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(registryURL)), "/")
	if registryURL == "" || spec == nil || strings.TrimSpace(spec.App) == "" {
		return "", false
	}
	return registryURL + "/" + strings.ToLower(strings.TrimSpace(spec.App)), true
}

// OCIRepository drops a content digest or mutable tag without accepting an
// arbitrary URL. Callers separately require a content-addressed artifact.
func OCIRepository(reference string) (string, bool) {
	value := strings.TrimSpace(reference)
	if value == "" || strings.ContainsAny(value, "?#") {
		return "", false
	}
	if before, _, found := strings.Cut(value, "@"); found {
		value = before
	}
	lastSlash := strings.LastIndex(value, "/")
	if lastSlash < 0 || lastSlash == len(value)-1 {
		return "", false
	}
	if colon := strings.LastIndex(value, ":"); colon > lastSlash {
		value = value[:colon]
	}
	value = strings.TrimSuffix(strings.ToLower(value), "/")
	if value == "" || strings.Contains(value, "//") {
		return "", false
	}
	return value, true
}
