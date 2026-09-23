package effect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Lifecycle string

const (
	LifecycleReserved  Lifecycle = "reserved"
	LifecycleLaunched  Lifecycle = "launched"
	LifecycleCompleted Lifecycle = "completed"
	LifecycleResolved  Lifecycle = "resolved"
)

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

type SupervisorPhase string

const (
	SupervisorRunning   SupervisorPhase = "running"
	SupervisorSucceeded SupervisorPhase = "succeeded"
	SupervisorFailed    SupervisorPhase = "failed"
	SupervisorStopped   SupervisorPhase = "stopped"
	SupervisorNotFound  SupervisorPhase = "not-found"
	SupervisorUnknown   SupervisorPhase = "unknown"
)

type VerificationDecision string

const (
	VerificationSucceeded         VerificationDecision = "succeeded"
	VerificationFailedRepeatSafe  VerificationDecision = "failed-repeat-safe"
	VerificationNeverLaunched     VerificationDecision = "never-launched"
	VerificationStoppedRepeatSafe VerificationDecision = "stopped-repeat-safe"
	// VerificationFailed records a contained execution that exited on its own
	// (including a runner-enforced timeout) with a final failed outcome. It
	// releases the resource gate because nothing remains running, but it is not
	// a repeat-safety claim: the same effect is never relaunched, and a later
	// replay of the same operation input reuses this recorded failure.
	VerificationFailed VerificationDecision = "failed"
)

// OperationClaim identifies the operation lease that is authorized to reserve
// an external effect. Store implementations must validate it against the live
// operation row in the same transaction as the reservation.
type OperationClaim struct {
	OperationID string
	OwnerID     string
	Generation  int64
}

type Reservation struct {
	Authority             string
	Resource              string
	OperationClaim        OperationClaim
	Stage                 string
	InputDigest           string
	Supervisor            string
	SupervisorExecutionID string
	LaunchPayload         json.RawMessage
}

type Token struct {
	EffectID   string
	Generation int64
}

type ExecutionIdentity struct {
	Supervisor            string
	SupervisorExecutionID string
	RuntimeInstanceID     string
}

type RawEvidence struct {
	Source    string
	Reference string
	Payload   json.RawMessage
}

type Observation struct {
	Identity ExecutionIdentity
	Phase    SupervisorPhase
	ExitCode *int
	Output   []byte
	Evidence RawEvidence
}

// Verification is produced only by the configured EvidenceVerifier trust
// boundary. The executor never accepts a caller-provided repeat-safe boolean.
type Verification struct {
	Decision              VerificationDecision
	InputDigest           string
	ResultDigest          string
	ResultReference       string
	SupervisorExecutionID string
	RuntimeInstanceID     string
	EvidenceSource        string
	EvidenceReference     string
	ObservedAt            time.Time
}

type Completion struct {
	Outcome      Outcome
	ExitCode     *int
	Verification Verification
}

type Resolution struct {
	Decision     VerificationDecision
	Verification Verification
}

type Record struct {
	Token       Token
	Reservation Reservation
	Lifecycle   Lifecycle
	Execution   ExecutionIdentity
	Completion  *Completion
}

type ReservationResult struct {
	Record  Record
	Created bool
}

type ExecuteRequest struct {
	Reservation Reservation
	// LaunchMaterial exists only for the initial supervisor handoff. It is not
	// stored in the control database; the persisted LaunchPayload must contain
	// a secret-free descriptor that cryptographically binds this material.
	LaunchMaterial LaunchMaterial
}

type LaunchMaterial struct {
	Argv        []string
	Directory   string
	Environment []string
	// Subject is a stable, secret-free identity of the input the command acts
	// on (for build.test, the pinned source commit). It is bound into the
	// persisted descriptor instead of the per-claim working directory, so a
	// later claim of the same operation derives the same input digest.
	Subject string
	// Timeout bounds the supervised command; the runner kills the command's
	// process group when it elapses and records a final failed outcome.
	Timeout time.Duration
}

type ExecuteResult struct {
	EffectID        string
	Outcome         Outcome
	ExitCode        *int
	Output          []byte
	ResultDigest    string
	ResultReference string
	Reused          bool
}

func DigestInput(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// ComputeInputDigest binds the external launch payload to its resource, stage,
// and supervisor namespace. JSON is normalized before hashing so semantically
// identical objects have one digest while a changed command cannot reuse an
// earlier reservation by copying its digest field.
func ComputeInputDigest(value Reservation) (string, error) {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(value.LaunchPayload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode effect launch payload: %w", err)
	}
	normalized, err := json.Marshal(struct {
		Resource   string
		Stage      string
		Supervisor string
		Payload    any
	}{
		Resource:   value.Resource,
		Stage:      value.Stage,
		Supervisor: value.Supervisor,
		Payload:    payload,
	})
	if err != nil {
		return "", fmt.Errorf("encode canonical effect input: %w", err)
	}
	return DigestInput(normalized), nil
}

func validateReservation(value Reservation) error {
	switch {
	case strings.TrimSpace(value.Authority) == "":
		return fmt.Errorf("effect authority is required")
	case strings.TrimSpace(value.Resource) == "":
		return fmt.Errorf("effect resource is required")
	case strings.TrimSpace(value.OperationClaim.OperationID) == "":
		return fmt.Errorf("effect operation id is required")
	case strings.TrimSpace(value.OperationClaim.OwnerID) == "":
		return fmt.Errorf("effect operation owner is required")
	case value.OperationClaim.Generation <= 0:
		return fmt.Errorf("effect operation claim generation must be positive")
	case strings.TrimSpace(value.Stage) == "":
		return fmt.Errorf("effect stage is required")
	case strings.TrimSpace(value.Supervisor) == "":
		return fmt.Errorf("effect supervisor is required")
	case strings.TrimSpace(value.SupervisorExecutionID) == "":
		return fmt.Errorf("effect supervisor execution id is required")
	case len(value.LaunchPayload) == 0 || !json.Valid(value.LaunchPayload):
		return fmt.Errorf("effect launch payload must be valid JSON")
	}
	expected, err := ComputeInputDigest(value)
	if err != nil {
		return err
	}
	if value.InputDigest != expected {
		return fmt.Errorf("effect input digest does not match canonical launch input")
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
