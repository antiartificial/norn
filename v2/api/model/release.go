package model

import "time"

// ReleaseCandidate binds a Norn deployment to the CI identity that selected
// its immutable source and artifact. It is evidence, not caller authority.
type ReleaseCandidate struct {
	Provider     string `json:"provider"`
	Repository   string `json:"repository"`
	RepositoryID string `json:"repositoryId"`
	OwnerID      string `json:"ownerId"`
	RunID        string `json:"runId"`
	RunAttempt   string `json:"runAttempt"`
	WorkflowRef  string `json:"workflowRef"`
	WorkflowSHA  string `json:"workflowSha"`
	// SignerWorkflow* identify the SHA-pinned reusable workflow that signed
	// the release attestations. Workflow* remain the caller identity.
	SignerWorkflowRef string                     `json:"signerWorkflowRef,omitempty"`
	SignerWorkflowSHA string                     `json:"signerWorkflowSha,omitempty"`
	Ref               string                     `json:"ref"`
	Attestation       ReleaseAttestationIdentity `json:"attestation"`
}

type ReleaseAttestationIdentity struct {
	ProvenanceURI string `json:"provenanceUri,omitempty"`
	SBOMURI       string `json:"sbomUri,omitempty"`
	Issuer        string `json:"issuer"`
	SubjectDigest string `json:"subjectDigest"`
	MaterialSHA   string `json:"materialSha"`
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
