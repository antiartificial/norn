package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

const (
	OperationRequestFingerprintVersion = "norn.operation-request/v1"
	OperationAcceptanceEnvelopeSchema  = "norn.operation-acceptance/v1"
	OperationReplayContractVersion     = "norn.operation-replay/v1"
)

// OperationStore is the authority boundary for accepting durable work. It
// deliberately does not expose PostgreSQL transactions or caller-controlled
// signatures.
type OperationStore interface {
	Authority(context.Context) (string, error)
	Accept(context.Context, OperationAcceptance) (AcceptedOperation, error)
	Resolve(context.Context, OperationRequestIdentity, RequestFingerprint) (AcceptedOperation, error)
}

// OperationIdentityResolver is the authenticated retry seam for producers
// whose durable target is selected by the control plane. Callers must validate
// stable caller inputs before disclosing the resolved operation.
type OperationIdentityResolver interface {
	ResolveIdentity(context.Context, OperationRequestIdentity) (AcceptedOperation, error)
}

type OperationActor struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// OperationRequestIdentity is the complete replay namespace. Credential IDs
// are evidence about one authentication attempt and intentionally do not form
// part of this stable identity.
type OperationRequestIdentity struct {
	Authority string         `json:"authority"`
	Actor     OperationActor `json:"actor"`
	Kind      string         `json:"kind"`
	Resource  string         `json:"resource"`
	Key       string         `json:"key"`
}

type RequestFingerprint struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type AcceptanceAuditContext struct {
	RequestReceiptID string   `json:"requestReceiptId,omitempty"`
	RequestID        string   `json:"requestId,omitempty"`
	CredentialID     string   `json:"credentialId,omitempty"`
	DeviceID         string   `json:"deviceId,omitempty"`
	Source           string   `json:"source"`
	Scopes           []string `json:"scopes,omitempty"`
}

type OperationAdmissionPolicy struct {
	OneActiveMutablePerApp bool `json:"oneActiveMutablePerApp,omitempty"`
}

// FleetReconciliationAdmission carries server-verified attempt ownership into
// the PostgreSQL transaction that appends reconciliation evidence.
type FleetReconciliationAdmission struct {
	PlanID               string `json:"planId"`
	AttemptID            string `json:"attemptId,omitempty"`
	RequireActiveAttempt bool   `json:"requireActiveAttempt,omitempty"`
	RunnerAttemptID      string `json:"runnerAttemptId,omitempty"`
	WorkflowURL          string `json:"workflowUrl,omitempty"`
}

// FleetRunnerAttemptAdmission carries the verified protected-dispatch and
// workload identity evidence needed to create or recover a runner attempt in
// the same transaction as its signed acceptance. DispatchNonceSHA256 is the
// only nonce representation permitted at this boundary.
type FleetRunnerAttemptAdmission struct {
	PlanID                  string `json:"planId"`
	AttemptID               string `json:"attemptId"`
	ExpectedPredecessorID   string `json:"expectedPredecessorId,omitempty"`
	RunnerAttemptID         string `json:"runnerAttemptId"`
	CommitSHA               string `json:"commitSha"`
	PlanSHA256              string `json:"planSha256"`
	WorkflowURL             string `json:"workflowUrl"`
	DispatchNonceSHA256     string `json:"dispatchNonceSha256"`
	SourceDispatchRunID     string `json:"sourceDispatchRunId"`
	Resume                  bool   `json:"resume"`
	HeartbeatTimeoutSeconds int    `json:"heartbeatTimeoutSeconds"`
	WorkloadIntent          string `json:"workloadIntent"`
	WorkloadRunID           string `json:"workloadRunId"`
	WorkloadSHA             string `json:"workloadSha"`
}

type OperationAcceptance struct {
	Identity            OperationRequestIdentity      `json:"identity"`
	Fingerprint         RequestFingerprint            `json:"fingerprint"`
	Operation           model.Operation               `json:"operation"`
	Deployment          *model.Deployment             `json:"deployment,omitempty"`
	Regions             []model.ResolvedRegion        `json:"regions,omitempty"`
	Audit               AcceptanceAuditContext        `json:"audit"`
	Admission           OperationAdmissionPolicy      `json:"admission,omitempty"`
	FleetReconciliation *FleetReconciliationAdmission `json:"fleetReconciliation,omitempty"`
	FleetRunnerAttempt  *FleetRunnerAttemptAdmission  `json:"fleetRunnerAttempt,omitempty"`
	// Semantics carries endpoint-specific execution-affecting policy and
	// qualification inputs not represented by the operation/deployment fields.
	// It is covered by the canonical request fingerprint.
	Semantics                  map[string]interface{} `json:"semantics,omitempty"`
	acceptedFleetRunnerAttempt *fleet.RunnerAttempt   `json:"-"`
}

type AcceptanceSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

// AcceptanceSigner owns signing secrets and retained verification keys. Verify
// must select the key by signature KeyID; Resolve never substitutes the current
// signing key for the key that originally signed the record.
type AcceptanceSigner interface {
	Sign(context.Context, []byte) (AcceptanceSignature, error)
	Verify(context.Context, AcceptanceSignature, []byte) error
}

type SignedAcceptanceIntent struct {
	ID                    string                 `json:"id"`
	Schema                string                 `json:"schema"`
	RequestIdentityID     string                 `json:"requestIdentityId"`
	OperationID           string                 `json:"operationId"`
	DeploymentID          string                 `json:"deploymentId,omitempty"`
	AcceptedAt            time.Time              `json:"acceptedAt"`
	CanonicalBytes        []byte                 `json:"canonicalBytes"`
	CanonicalDigest       string                 `json:"canonicalDigest"`
	RequestCanonicalBytes []byte                 `json:"requestCanonicalBytes"`
	Signature             AcceptanceSignature    `json:"signature"`
	Fingerprint           RequestFingerprint     `json:"fingerprint"`
	Audit                 AcceptanceAuditContext `json:"audit"`
}

type AcceptedOperation struct {
	Operation          model.Operation        `json:"operation"`
	Deployment         *model.Deployment      `json:"deployment,omitempty"`
	Regions            []model.ResolvedRegion `json:"regions,omitempty"`
	RequestIdentityID  string                 `json:"requestIdentityId"`
	AcceptanceIntentID string                 `json:"acceptanceIntentId"`
	Replayed           bool                   `json:"replayed"`
	Intent             SignedAcceptanceIntent `json:"intent"`
	FleetRunnerAttempt *fleet.RunnerAttempt   `json:"fleetRunnerAttempt,omitempty"`
}

type AcceptancePolicy struct {
	ExpectedAuthority   string
	MaxIdentityKeyBytes int
	MaxCanonicalBytes   int
	ResolveTimeout      time.Duration
	// ReplayTTL opts newly accepted identities into the versioned replay
	// expiry contract. Zero preserves the historical indefinite replay
	// behavior, including for every identity accepted before the policy is set.
	ReplayTTL time.Duration
}

var (
	ErrAcceptanceInvalid            = errors.New("operation acceptance is invalid")
	ErrAcceptanceConflict           = errors.New("operation request identity conflict")
	ErrAcceptanceExpired            = errors.New("operation request identity replay window expired")
	ErrAcceptanceNotFound           = errors.New("operation acceptance not found")
	ErrAcceptanceIndeterminate      = errors.New("operation acceptance outcome is indeterminate")
	ErrAcceptanceSignature          = errors.New("operation acceptance signature is invalid")
	ErrAcceptanceAuthority          = errors.New("control authority is unavailable or mismatched")
	ErrAcceptanceAdmission          = errors.New("operation admission rejected")
	ErrLegacyReplayAmbiguous        = errors.New("legacy operation replay identity is ambiguous")
	ErrFleetReconciliationAdmission = errors.New("fleet reconciliation admission rejected")
	ErrFleetRunnerAttemptAdmission  = errors.New("fleet runner attempt admission rejected")
)

type AcceptanceValidationError struct{ Reason string }

func (e *AcceptanceValidationError) Error() string {
	return "invalid operation acceptance: " + e.Reason
}
func (e *AcceptanceValidationError) Unwrap() error { return ErrAcceptanceInvalid }

type AcceptanceConflictError struct{ Identity OperationRequestIdentity }

func (e *AcceptanceConflictError) Error() string {
	return fmt.Sprintf("operation request identity conflicts for %s/%s", e.Identity.Kind, e.Identity.Resource)
}
func (e *AcceptanceConflictError) Unwrap() error { return ErrAcceptanceConflict }

type AcceptanceExpiredError struct {
	Identity  OperationRequestIdentity
	ExpiresAt time.Time
}

func (e *AcceptanceExpiredError) Error() string {
	return fmt.Sprintf("operation request identity replay window expired for %s/%s", e.Identity.Kind, e.Identity.Resource)
}
func (e *AcceptanceExpiredError) Unwrap() error { return ErrAcceptanceExpired }

type AcceptanceNotFoundError struct{ Identity OperationRequestIdentity }

func (e *AcceptanceNotFoundError) Error() string {
	return fmt.Sprintf("operation acceptance not found for %s/%s", e.Identity.Kind, e.Identity.Resource)
}
func (e *AcceptanceNotFoundError) Unwrap() error { return ErrAcceptanceNotFound }

type AcceptanceIndeterminateError struct{ Err error }

func (e *AcceptanceIndeterminateError) Error() string {
	if e.Err == nil {
		return "operation acceptance outcome is indeterminate; retry with the same request identity"
	}
	return "operation acceptance outcome is indeterminate; retry with the same request identity: " + e.Err.Error()
}
func (e *AcceptanceIndeterminateError) Unwrap() error { return ErrAcceptanceIndeterminate }

type AcceptanceSignatureError struct{ Err error }

func (e *AcceptanceSignatureError) Error() string {
	if e.Err == nil {
		return "operation acceptance signature is invalid"
	}
	return "operation acceptance signature is invalid: " + e.Err.Error()
}
func (e *AcceptanceSignatureError) Unwrap() error { return ErrAcceptanceSignature }

type AcceptanceAuthorityError struct{ Expected, Actual string }

func (e *AcceptanceAuthorityError) Error() string {
	if e.Actual == "" {
		return "control authority is unavailable"
	}
	return fmt.Sprintf("control authority mismatch: expected %q, database has %q", e.Expected, e.Actual)
}
func (e *AcceptanceAuthorityError) Unwrap() error { return ErrAcceptanceAuthority }

type AcceptanceAdmissionError struct{ App string }

func (e *AcceptanceAdmissionError) Error() string {
	return fmt.Sprintf("an active mutable operation already exists for app %q", e.App)
}
func (e *AcceptanceAdmissionError) Unwrap() error { return ErrAcceptanceAdmission }

type LegacyReplayAmbiguousError struct{ Kind, Resource string }

func (e *LegacyReplayAmbiguousError) Error() string {
	return fmt.Sprintf("legacy replay identity is ambiguous for %s/%s", e.Kind, e.Resource)
}
func (e *LegacyReplayAmbiguousError) Unwrap() error { return ErrLegacyReplayAmbiguous }

type FleetReconciliationAdmissionError struct {
	Code   string
	Reason string
}

func (e *FleetReconciliationAdmissionError) Error() string {
	if e.Reason == "" {
		return "fleet reconciliation admission rejected"
	}
	return e.Reason
}
func (e *FleetReconciliationAdmissionError) Unwrap() error { return ErrFleetReconciliationAdmission }

type FleetRunnerAttemptAdmissionError struct {
	Code   string
	Reason string
}

func (e *FleetRunnerAttemptAdmissionError) Error() string {
	if e.Reason == "" {
		return "fleet runner attempt admission rejected"
	}
	return e.Reason
}
func (e *FleetRunnerAttemptAdmissionError) Unwrap() error { return ErrFleetRunnerAttemptAdmission }
