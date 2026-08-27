package model

import "time"

type OperationStatus string

const (
	OperationQueued    OperationStatus = "queued"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
	OperationCanceled  OperationStatus = "canceled"
)

type Operation struct {
	ID            string                 `json:"id"`
	Kind          string                 `json:"kind"`
	App           string                 `json:"app,omitempty"`
	SagaID        string                 `json:"sagaId,omitempty"`
	Ref           string                 `json:"ref,omitempty"`
	Status        OperationStatus        `json:"status"`
	Risk          string                 `json:"risk,omitempty"`
	Source        string                 `json:"source,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Payload       map[string]interface{} `json:"payload,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Attempts      int                    `json:"attempts,omitempty"`
	MaxAttempts   int                    `json:"maxAttempts,omitempty"`
	LockedBy      string                 `json:"lockedBy,omitempty"`
	LockedUntil   *time.Time             `json:"lockedUntil,omitempty"`
	NextAttemptAt time.Time              `json:"nextAttemptAt,omitempty"`
	LastError     string                 `json:"lastError,omitempty"`
	StartedAt     time.Time              `json:"startedAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
	FinishedAt    *time.Time             `json:"finishedAt,omitempty"`
	Receipt       *OperationReceipt      `json:"receipt,omitempty"`
}

func (o Operation) Active() bool {
	return o.Status == OperationQueued || o.Status == OperationRunning
}

type OperationReceipt struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Kind          string                    `json:"kind"`
	Outcome       OperationStatus           `json:"outcome"`
	Summary       string                    `json:"summary"`
	StartedAt     time.Time                 `json:"startedAt"`
	FinishedAt    *time.Time                `json:"finishedAt,omitempty"`
	Platform      *PlatformOperationReceipt `json:"platform,omitempty"`
	Host          *HostAssuranceReceipt     `json:"host,omitempty"`
	App           *AppOperationReceipt      `json:"app,omitempty"`
	Fleet         *FleetOperationReceipt    `json:"fleet,omitempty"`
}

type PlatformOperationReceipt struct {
	Ref             string `json:"ref,omitempty"`
	SHA             string `json:"sha,omitempty"`
	Mode            string `json:"mode,omitempty"`
	DrainMode       string `json:"drainMode,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"outputTruncated,omitempty"`
}

type HostAssuranceReceipt struct {
	ExitCode        *int   `json:"exitCode,omitempty"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"outputTruncated,omitempty"`
}

type AppOperationReceipt struct {
	App                string   `json:"app"`
	Ref                string   `json:"ref,omitempty"`
	DeploymentID       string   `json:"deploymentId,omitempty"`
	CommitSHA          string   `json:"commitSha,omitempty"`
	ImageTag           string   `json:"imageTag,omitempty"`
	Step               string   `json:"step,omitempty"`
	Snapshot           string   `json:"snapshot,omitempty"`
	PreRestoreSnapshot string   `json:"preRestoreSnapshot,omitempty"`
	Database           string   `json:"database,omitempty"`
	Keep               *int     `json:"keep,omitempty"`
	Pruned             []string `json:"pruned,omitempty"`
}

type FleetOperationReceipt struct {
	PlanID         string `json:"planId"`
	Cluster        string `json:"cluster,omitempty"`
	Pool           string `json:"pool,omitempty"`
	Action         string `json:"action,omitempty"`
	SourceDigest   string `json:"sourceDigest,omitempty"`
	PlanDigest     string `json:"planDigest,omitempty"`
	Signature      string `json:"signature,omitempty"`
	WorkflowURL    string `json:"workflowUrl,omitempty"`
	Phase          string `json:"phase,omitempty"`
	CommitSHA      string `json:"commitSha,omitempty"`
	PlanSHA256     string `json:"planSha256,omitempty"`
	StateSerial    int64  `json:"stateSerial,omitempty"`
	EvidenceDigest string `json:"evidenceDigest,omitempty"`
}

func (o *Operation) AttachReceipt() {
	if o == nil || !o.Status.Terminal() {
		return
	}
	receipt := &OperationReceipt{
		SchemaVersion: "norn.operation-receipt/v1", Kind: o.Kind, Outcome: o.Status,
		Summary: o.Message, StartedAt: o.StartedAt, FinishedAt: o.FinishedAt,
	}
	switch {
	case len(o.Kind) >= 9 && o.Kind[:9] == "platform.":
		receipt.Platform = &PlatformOperationReceipt{
			Ref: stringValue(o.Payload, "ref"), SHA: stringValue(o.Payload, "sha"),
			Mode: stringValue(o.Payload, "mode"), DrainMode: stringValue(o.Payload, "drainMode"),
			ExitCode: intPointer(o.Metadata, "exitCode"), Output: stringValue(o.Metadata, "output"),
			OutputTruncated: boolValue(o.Metadata, "outputTruncated"),
		}
	case o.Kind == "host.assure":
		receipt.Host = &HostAssuranceReceipt{
			ExitCode: intPointer(o.Metadata, "exitCode"), Output: stringValue(o.Metadata, "output"),
			OutputTruncated: boolValue(o.Metadata, "outputTruncated"),
		}
	case len(o.Kind) >= 4 && o.Kind[:4] == "app.":
		receipt.App = &AppOperationReceipt{
			App: o.App, Ref: o.Ref, DeploymentID: stringValue(o.Metadata, "deploymentId"),
			CommitSHA: stringValue(o.Metadata, "commitSha"), ImageTag: stringValue(o.Metadata, "imageTag"),
			Step: stringValue(o.Metadata, "step"), Snapshot: stringValue(o.Metadata, "snapshot"),
			PreRestoreSnapshot: stringValue(o.Metadata, "preRestoreSnapshot"), Database: stringValue(o.Metadata, "database"),
			Keep: intPointer(o.Metadata, "keep"), Pruned: stringSliceValue(o.Metadata, "pruned"),
		}
	case o.Kind == "fleet.capacity-plan":
		receipt.Fleet = &FleetOperationReceipt{
			PlanID: stringValue(o.Metadata, "planId"), Cluster: stringValue(o.Payload, "cluster"),
			Pool: stringValue(o.Payload, "pool"), Action: stringValue(o.Payload, "action"),
			SourceDigest: stringValue(o.Payload, "sourceDigest"), PlanDigest: stringValue(o.Metadata, "planDigest"),
			Signature: stringValue(o.Metadata, "signature"), WorkflowURL: stringValue(o.Payload, "workflowUrl"),
		}
	case o.Kind == "fleet.reconciliation":
		receipt.Fleet = &FleetOperationReceipt{
			PlanID: o.Ref, Phase: stringValue(o.Payload, "phase"), CommitSHA: stringValue(o.Payload, "commitSha"),
			PlanSHA256: stringValue(o.Payload, "planSha256"), StateSerial: int64Value(o.Payload, "stateSerial"),
			EvidenceDigest: stringValue(o.Payload, "evidenceDigest"),
		}
	}
	o.Receipt = receipt
}

func stringSliceValue(values map[string]interface{}, key string) []string {
	switch raw := values[key].(type) {
	case []string:
		return raw
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (s OperationStatus) Terminal() bool {
	return s == OperationSucceeded || s == OperationFailed || s == OperationCanceled
}

func stringValue(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}

func boolValue(values map[string]interface{}, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func intPointer(values map[string]interface{}, key string) *int {
	switch value := values[key].(type) {
	case int:
		return &value
	case float64:
		converted := int(value)
		return &converted
	default:
		return nil
	}
}

func int64Value(values map[string]interface{}, key string) int64 {
	switch value := values[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
