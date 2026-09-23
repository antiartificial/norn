package effect

import (
	"context"
	"fmt"
	"strings"
)

type Executor struct {
	Store      Store
	Supervisor Supervisor
	Verifier   EvidenceVerifier
}

func (e *Executor) Execute(ctx context.Context, request ExecuteRequest) (ExecuteResult, error) {
	if e == nil || e.Store == nil || e.Supervisor == nil || e.Verifier == nil {
		return ExecuteResult{}, fmt.Errorf("external effect executor is unavailable")
	}
	if err := validateReservation(request.Reservation); err != nil {
		return ExecuteResult{}, err
	}
	if err := e.Supervisor.Prepare(ctx, request.Reservation); err != nil {
		// No reservation or launch happened, but a refused namespace (missing or
		// inconsistent history, revoked ID) is unknown state, not a command
		// failure: defer instead of terminalizing the operation.
		return ExecuteResult{}, &PendingError{Resource: request.Reservation.Resource, Reason: "supervisor namespace could not be prepared", Cause: err}
	}

	reserved, err := e.Store.Reserve(ctx, request.Reservation)
	if err != nil {
		return ExecuteResult{}, err
	}
	record := reserved.Record
	if err := validateRecord(record, request.Reservation); err != nil {
		return ExecuteResult{}, err
	}
	if record.Lifecycle == LifecycleCompleted {
		if record.Completion == nil {
			return ExecuteResult{}, pending(record, "completed effect has no durable completion", nil)
		}
		output, err := e.retrieveResult(ctx, record)
		if err != nil {
			return ExecuteResult{}, pending(record, "completed effect result is unavailable or invalid", err)
		}
		return resultFromCompletion(record, output, true), nil
	}
	if record.Lifecycle == LifecycleResolved {
		return ExecuteResult{}, pending(record, "effect was resolved; wait for a successor reservation", nil)
	}

	if reserved.Created {
		identity, launchErr := e.Supervisor.Launch(ctx, record.Reservation, request.LaunchMaterial)
		if launchErr != nil {
			return ExecuteResult{}, pending(record, "launch outcome is unknown", launchErr)
		}
		if err := validateExecutionIdentity(identity, record.Reservation); err != nil {
			return ExecuteResult{}, pending(record, "supervisor returned an invalid execution identity", err)
		}
		if err := e.Store.MarkLaunched(ctx, record.Token, identity); err != nil {
			return ExecuteResult{}, pending(record, "could not durably acknowledge launch", err)
		}
		record.Lifecycle = LifecycleLaunched
		record.Execution = identity
	}
	return e.observeRecord(ctx, record)
}

// Recover drives an existing durable record to a verified terminal state using
// only its stored reservation and the original supervisor execution identity.
// It never launches. A successor blocked by the record uses it so the gate is
// released only by trusted evidence about that exact execution.
func (e *Executor) Recover(ctx context.Context, record Record) (ExecuteResult, error) {
	if e == nil || e.Store == nil || e.Supervisor == nil || e.Verifier == nil {
		return ExecuteResult{}, fmt.Errorf("external effect executor is unavailable")
	}
	if strings.TrimSpace(record.Token.EffectID) == "" || record.Token.Generation <= 0 {
		return ExecuteResult{}, fmt.Errorf("effect recovery requires a durable token")
	}
	if err := validateReservation(record.Reservation); err != nil {
		return ExecuteResult{}, pending(record, "stored reservation is invalid", err)
	}
	switch record.Lifecycle {
	case LifecycleCompleted:
		if record.Completion == nil {
			return ExecuteResult{}, pending(record, "completed effect has no durable completion", nil)
		}
		output, err := e.retrieveResult(ctx, record)
		if err != nil {
			return ExecuteResult{}, pending(record, "completed effect result is unavailable or invalid", err)
		}
		return resultFromCompletion(record, output, true), nil
	case LifecycleResolved:
		return ExecuteResult{}, pending(record, "effect was resolved; wait for a successor reservation", nil)
	case LifecycleReserved, LifecycleLaunched:
		return e.observeRecord(ctx, record)
	default:
		return ExecuteResult{}, pending(record, "effect lifecycle is unsupported", nil)
	}
}

func (e *Executor) observeRecord(ctx context.Context, record Record) (ExecuteResult, error) {
	identity := record.Execution
	if strings.TrimSpace(identity.SupervisorExecutionID) == "" {
		identity = ExecutionIdentity{
			Supervisor:            record.Reservation.Supervisor,
			SupervisorExecutionID: record.Reservation.SupervisorExecutionID,
		}
	}
	observation, err := e.Supervisor.Query(ctx, record.Reservation, identity)
	if err != nil {
		return ExecuteResult{}, pending(record, "supervisor query failed", err)
	}
	if err := validateObservation(observation, record.Reservation); err != nil {
		return ExecuteResult{}, pending(record, "supervisor observation does not match the reservation", err)
	}

	switch observation.Phase {
	case SupervisorRunning, SupervisorUnknown:
		return ExecuteResult{}, pending(record, "supervisor has not produced a terminal verified outcome", nil)
	case SupervisorSucceeded, SupervisorFailed, SupervisorStopped, SupervisorNotFound:
		return e.resolveObservation(ctx, record, observation)
	default:
		return ExecuteResult{}, pending(record, "supervisor returned an unsupported phase", nil)
	}
}

func (e *Executor) resolveObservation(ctx context.Context, record Record, observation Observation) (ExecuteResult, error) {
	verification, err := e.Verifier.Verify(ctx, record, observation)
	if err != nil {
		return ExecuteResult{}, pending(record, "effect evidence could not be verified", err)
	}
	if err := validateVerification(record, observation, verification); err != nil {
		return ExecuteResult{}, pending(record, "effect evidence is not bound to this execution", err)
	}

	switch observation.Phase {
	case SupervisorSucceeded:
		if verification.Decision != VerificationSucceeded {
			return ExecuteResult{}, pending(record, "success evidence did not verify success", nil)
		}
		if err := validateResult(observation.Output, verification); err != nil {
			return ExecuteResult{}, pending(record, "success result does not match verified evidence", err)
		}
		completion := Completion{Outcome: OutcomeSucceeded, ExitCode: observation.ExitCode, Verification: verification}
		if err := e.Store.Complete(ctx, record.Token, completion); err != nil {
			return ExecuteResult{}, pending(record, "stale or failed completion acknowledgement", err)
		}
		return resultFromCompletion(Record{Token: record.Token, Completion: &completion}, observation.Output, false), nil
	case SupervisorFailed:
		if verification.Decision != VerificationFailed && verification.Decision != VerificationFailedRepeatSafe {
			return ExecuteResult{}, pending(record, "failed execution has no verified final outcome", nil)
		}
		if verification.ResultDigest != "" {
			if err := validateResult(observation.Output, verification); err != nil {
				return ExecuteResult{}, pending(record, "failed result does not match verified evidence", err)
			}
		}
		completion := Completion{Outcome: OutcomeFailed, ExitCode: observation.ExitCode, Verification: verification}
		if err := e.Store.Complete(ctx, record.Token, completion); err != nil {
			return ExecuteResult{}, pending(record, "stale or failed completion acknowledgement", err)
		}
		output := observation.Output
		if verification.ResultDigest == "" {
			output = nil
		}
		return resultFromCompletion(Record{Token: record.Token, Completion: &completion}, output, false), nil
	case SupervisorStopped:
		if verification.Decision != VerificationStoppedRepeatSafe {
			return ExecuteResult{}, pending(record, "termination is not verified safe to repeat", nil)
		}
	case SupervisorNotFound:
		if verification.Decision != VerificationNeverLaunched {
			return ExecuteResult{}, pending(record, "not-found does not prove the execution never launched", nil)
		}
	}

	revoked, err := e.Supervisor.Revoke(ctx, record.Reservation, observation.Identity)
	if err != nil {
		return ExecuteResult{}, pending(record, "supervisor execution could not be durably revoked", err)
	}
	if err := validateObservation(revoked, record.Reservation); err != nil {
		return ExecuteResult{}, pending(record, "revocation evidence does not match the reservation", err)
	}
	verification, err = e.Verifier.Verify(ctx, record, revoked)
	if err != nil {
		return ExecuteResult{}, pending(record, "revocation evidence could not be verified", err)
	}
	if err := validateVerification(record, revoked, verification); err != nil {
		return ExecuteResult{}, pending(record, "revocation evidence is not bound to this execution", err)
	}
	if (revoked.Phase == SupervisorNotFound && verification.Decision != VerificationNeverLaunched) ||
		(revoked.Phase == SupervisorStopped && verification.Decision != VerificationStoppedRepeatSafe) ||
		(revoked.Phase != SupervisorNotFound && revoked.Phase != SupervisorStopped) {
		return ExecuteResult{}, pending(record, "supervisor revocation did not establish a repeat-safe tombstone", nil)
	}

	resolution := Resolution{Decision: verification.Decision, Verification: verification}
	if err := e.Store.Resolve(ctx, record.Token, resolution); err != nil {
		return ExecuteResult{}, pending(record, "stale or failed resolution acknowledgement", err)
	}
	return ExecuteResult{}, pending(record, ResolutionRecordedReason, nil)
}

func validateRecord(record Record, request Reservation) error {
	if strings.TrimSpace(record.Token.EffectID) == "" || record.Token.Generation <= 0 {
		return fmt.Errorf("effect store returned an invalid token")
	}
	if record.Reservation.Authority != request.Authority || record.Reservation.Resource != request.Resource ||
		record.Reservation.OperationClaim.OperationID != request.OperationClaim.OperationID ||
		record.Reservation.Stage != request.Stage || record.Reservation.InputDigest != request.InputDigest ||
		record.Reservation.Supervisor != request.Supervisor {
		return fmt.Errorf("effect store returned a reservation for different work")
	}
	return nil
}

func validateExecutionIdentity(identity ExecutionIdentity, reservation Reservation) error {
	if identity.Supervisor != reservation.Supervisor || identity.SupervisorExecutionID != reservation.SupervisorExecutionID {
		return fmt.Errorf("supervisor execution identity mismatch")
	}
	if strings.TrimSpace(identity.RuntimeInstanceID) == "" {
		return fmt.Errorf("runtime instance identity is required")
	}
	return nil
}

func validateObservation(observation Observation, reservation Reservation) error {
	if observation.Identity.Supervisor != reservation.Supervisor || observation.Identity.SupervisorExecutionID != reservation.SupervisorExecutionID {
		return fmt.Errorf("observation execution identity mismatch")
	}
	return nil
}

func validateVerification(record Record, observation Observation, verification Verification) error {
	if verification.InputDigest != record.Reservation.InputDigest || verification.SupervisorExecutionID != record.Reservation.SupervisorExecutionID {
		return fmt.Errorf("verification identity or input digest mismatch")
	}
	if verification.RuntimeInstanceID != observation.Identity.RuntimeInstanceID {
		return fmt.Errorf("verification runtime instance mismatch")
	}
	if record.Execution.RuntimeInstanceID != "" && observation.Identity.RuntimeInstanceID != record.Execution.RuntimeInstanceID {
		return fmt.Errorf("observation runtime instance does not match the durable launch identity")
	}
	if strings.TrimSpace(verification.EvidenceSource) == "" || strings.TrimSpace(verification.EvidenceReference) == "" || verification.ObservedAt.IsZero() {
		return fmt.Errorf("verified evidence provenance is incomplete")
	}
	if verification.Decision == VerificationSucceeded && !validDigest(verification.ResultDigest) {
		return fmt.Errorf("successful effect result digest is required")
	}
	if verification.Decision == VerificationSucceeded && strings.TrimSpace(verification.ResultReference) == "" {
		return fmt.Errorf("successful effect result reference is required")
	}
	return nil
}

func (e *Executor) retrieveResult(ctx context.Context, record Record) ([]byte, error) {
	if record.Completion == nil {
		return nil, nil
	}
	verification := record.Completion.Verification
	if record.Completion.Outcome != OutcomeSucceeded && verification.ResultDigest == "" {
		return nil, nil
	}
	output, err := e.Supervisor.RetrieveResult(ctx, record.Reservation, record.Execution, verification.ResultReference)
	if err != nil {
		return nil, err
	}
	if err := validateResult(output, verification); err != nil {
		return nil, err
	}
	return output, nil
}

func validateResult(output []byte, verification Verification) error {
	if DigestInput(output) != verification.ResultDigest {
		return fmt.Errorf("result digest mismatch")
	}
	return nil
}

func resultFromCompletion(record Record, output []byte, reused bool) ExecuteResult {
	completion := record.Completion
	if completion == nil {
		return ExecuteResult{}
	}
	return ExecuteResult{
		EffectID:        record.Token.EffectID,
		Outcome:         completion.Outcome,
		ExitCode:        completion.ExitCode,
		Output:          output,
		ResultDigest:    completion.Verification.ResultDigest,
		ResultReference: completion.Verification.ResultReference,
		Reused:          reused,
	}
}

func pending(record Record, reason string, cause error) error {
	return &PendingError{EffectID: record.Token.EffectID, Resource: record.Reservation.Resource, Reason: reason, Cause: cause}
}
