// Package fleet owns Norn's read-only understanding of the norn-fleet
// repository contract. It deliberately contains no cloud-provider clients.
package fleet

const (
	APIVersion = "norn.dev/fleet/v1"
	Kind       = "Cluster"
)

type Document struct {
	APIVersion string              `yaml:"apiVersion" json:"apiVersion"`
	Kind       string              `yaml:"kind" json:"kind"`
	Metadata   Metadata            `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Cluster    Cluster             `yaml:"cluster" json:"cluster"`
	NodePools  map[string]NodePool `yaml:"nodePools" json:"nodePools"`
}

type Metadata struct {
	Repository  string `yaml:"repository,omitempty" json:"repository,omitempty"`
	Environment string `yaml:"environment,omitempty" json:"environment,omitempty"`
	WorkflowURL string `yaml:"workflowURL,omitempty" json:"workflowUrl,omitempty"`
}

type Cluster struct {
	Name     string `yaml:"name" json:"name"`
	Provider string `yaml:"provider" json:"provider"`
	Region   string `yaml:"region" json:"region"`
}

type NodePool struct {
	Size        string            `yaml:"size" json:"size"`
	Min         int               `yaml:"min" json:"min"`
	Desired     int               `yaml:"desired" json:"desired"`
	Max         int               `yaml:"max" json:"max"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Replacement Replacement       `yaml:"replacement,omitempty" json:"replacement,omitempty"`
}

type Replacement struct {
	Strategy                string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	RequireCapacityHeadroom bool   `yaml:"requireCapacityHeadroom,omitempty" json:"requireCapacityHeadroom,omitempty"`
	DrainTimeout            string `yaml:"drainTimeout,omitempty" json:"drainTimeout,omitempty"`
	RequireReadiness        bool   `yaml:"requireReadiness,omitempty" json:"requireReadiness,omitempty"`
}

type Finding struct {
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Field       string `json:"field"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type ValidationReport struct {
	SchemaVersion string    `json:"schemaVersion"`
	DocumentKind  string    `json:"documentKind"`
	Name          string    `json:"name,omitempty"`
	Valid         bool      `json:"valid"`
	Findings      []Finding `json:"findings"`
}

type Inventory struct {
	SchemaVersion string              `json:"schemaVersion"`
	Configured    bool                `json:"configured"`
	Source        string              `json:"source,omitempty"`
	Digest        string              `json:"digest,omitempty"`
	Document      *Document           `json:"document,omitempty"`
	Validation    *ValidationReport   `json:"validation,omitempty"`
	NodePools     map[string]NodePool `json:"nodePools"`
}

type PlanRequest struct {
	Desired  *int   `json:"desired,omitempty"`
	Size     string `json:"size,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type CapacityPlan struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	Cluster       string    `json:"cluster"`
	Pool          string    `json:"pool"`
	Current       NodePool  `json:"current"`
	Proposed      NodePool  `json:"proposed"`
	Action        string    `json:"action"`
	Strategy      string    `json:"strategy"`
	Reason        string    `json:"reason,omitempty"`
	SourceDigest  string    `json:"sourceDigest"`
	WorkflowURL   string    `json:"workflowUrl,omitempty"`
	Findings      []Finding `json:"findings"`
	Digest        string    `json:"digest"`
	Signature     string    `json:"signature,omitempty"`
}

const ReconciliationSchemaVersion = "norn.fleet-reconciliation/v1"

// ReconciliationRequest is a durable, secret-free checkpoint emitted by the
// protected infrastructure runner. Checkpoints are append-only so a restarted
// runner can discover the last proven phase without trusting its local disk.
type ReconciliationRequest struct {
	SchemaVersion  string `json:"schemaVersion"`
	AttemptID      string `json:"attemptId,omitempty"`
	Phase          string `json:"phase"`
	Status         string `json:"status"`
	CommitSHA      string `json:"commitSha"`
	PlanSHA256     string `json:"planSha256"`
	StateSerial    int64  `json:"stateSerial,omitempty"`
	EvidenceDigest string `json:"evidenceDigest"`
	Message        string `json:"message,omitempty"`
}

type RunnerAttemptStartRequest struct {
	SchemaVersion           string `json:"schemaVersion"`
	RunnerAttemptID         string `json:"runnerAttemptId"`
	CommitSHA               string `json:"commitSha"`
	PlanSHA256              string `json:"planSha256"`
	WorkflowURL             string `json:"workflowUrl,omitempty"`
	HeartbeatTimeoutSeconds int    `json:"heartbeatTimeoutSeconds,omitempty"`
}

type RunnerHeartbeatRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	Phase         string `json:"phase"`
	Sequence      int64  `json:"sequence"`
	Revision      int64  `json:"revision"`
	Message       string `json:"message,omitempty"`
}

type RunnerAdvanceRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ExpectedPhase string `json:"expectedPhase"`
	Revision      int64  `json:"revision"`
}

type RunnerRetryRequest struct {
	SchemaVersion   string `json:"schemaVersion"`
	Revision        int64  `json:"revision"`
	RunnerAttemptID string `json:"runnerAttemptId"`
	WorkflowURL     string `json:"workflowUrl,omitempty"`
	Reason          string `json:"reason"`
}

type RunnerCancelRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	Revision      int64  `json:"revision"`
	Reason        string `json:"reason"`
}
