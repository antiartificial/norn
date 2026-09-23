package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/effect"
)

const (
	buildTestStage      = "build.test"
	maxTestFailureBytes = 8 << 10
)

// BuildTestEffectStore is the durable side the build.test step needs beyond
// the executor: the control authority and the effect currently holding a
// resource, so a blocked successor recovers that exact execution.
type BuildTestEffectStore interface {
	Authority(context.Context) (string, error)
	effect.RecoveryStore
}

// BuildTestEffects is the supervised build.test configuration. Every field is
// required; startup constructs it only for the explicit supervised mode.
type BuildTestEffects struct {
	Executor   *effect.Executor
	Store      BuildTestEffectStore
	Supervisor string
	// Descriptor returns the secret-free persisted launch descriptor.
	Descriptor func(effect.LaunchMaterial) (json.RawMessage, error)
	// Environment is the complete command environment; nothing is inherited
	// from the control-plane process.
	Environment []string
	Timeout     time.Duration
}

// runSupervisedTest executes build.test as one fenced external effect.
//
// The input digest binds the command, environment, timeout and pinned source
// commit rather than this claim's temporary checkout, so a later claim of the
// same operation finds the same effect: a completed result is reused and an
// unresolved launch is queried, never relaunched. When a different effect
// holds the app's build.test resource (another operation, or this operation
// with unpinned source), the step drives recovery of that exact execution and
// proceeds only if trusted evidence released the gate.
func (p *Pipeline) runSupervisedTest(ctx context.Context, st *state) error {
	config := p.BuildTestEffects
	if config.Executor == nil || config.Store == nil || config.Descriptor == nil || config.Supervisor == "" || config.Timeout <= 0 {
		return fmt.Errorf("supervised build.test is misconfigured")
	}
	claim := st.claim
	if claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 {
		return fmt.Errorf("supervised build.test requires the operation claim")
	}
	if st.sourceIdentity == "" {
		return fmt.Errorf("supervised build.test requires the operation's recorded source identity")
	}
	authority, err := config.Store.Authority(ctx)
	if err != nil {
		return &effect.PendingError{Resource: buildTestResource(st), Reason: "control authority is unavailable", Cause: err}
	}
	material := effect.LaunchMaterial{
		Argv: []string{"sh", "-c", st.spec.Build.Test}, Directory: st.workDir,
		Environment: append([]string(nil), config.Environment...), Subject: buildTestSubject(st), Timeout: config.Timeout,
	}
	payload, err := config.Descriptor(material)
	if err != nil {
		return fmt.Errorf("build.test launch descriptor: %w", err)
	}
	reservation := effect.Reservation{
		Authority: authority, Resource: buildTestResource(st),
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          buildTestStage, Supervisor: config.Supervisor, LaunchPayload: payload,
	}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return err
	}
	reservation.SupervisorExecutionID = buildTestExecutionID(reservation)
	request := effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material}

	result, err := config.Executor.Execute(ctx, request)
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := config.Store.UnresolvedForResource(ctx, authority, reservation.Resource)
		if lookupErr != nil {
			return &effect.PendingError{Resource: reservation.Resource, Reason: "blocking effect lookup failed", Cause: lookupErr}
		}
		if found {
			if _, recoverErr := config.Executor.Recover(ctx, blocking); recoverErr != nil && !isResolutionRecorded(recoverErr) {
				return recoverErr
			}
		}
		// Recovery completed or resolved the blocking effect; exactly one
		// successor wins the next reservation (the store's unique gate).
		result, err = config.Executor.Execute(ctx, request)
	}
	if err != nil {
		return err
	}
	if result.Outcome == effect.OutcomeSucceeded {
		return nil
	}
	exit := -1
	if result.ExitCode != nil {
		exit = *result.ExitCode
	}
	return fmt.Errorf("tests failed (exit %d): %s", exit, tail(result.Output, maxTestFailureBytes))
}

func buildTestResource(st *state) string {
	return "app/" + st.spec.App + "/" + buildTestStage
}

// buildTestSubject is the operation's recorded source identity. Every claim
// of the operation derives the same subject, for clean, dirty and non-git
// sources alike; a claim whose source differs fails at the source checkpoint
// before any test can be authorized.
func buildTestSubject(st *state) string {
	return "source:" + st.sourceIdentity
}

// buildTestExecutionID is unique per operation, input and claim generation.
// The generation lets a successor reserve after a verified resolution
// tombstoned an earlier execution ID; an existing unresolved or completed
// record is always found by operation and input digest, never by this ID.
func buildTestExecutionID(reservation effect.Reservation) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", reservation.Authority, reservation.OperationClaim.OperationID, reservation.InputDigest, reservation.OperationClaim.Generation)))
	return "build-test-" + hex.EncodeToString(digest[:16])
}

func isResolutionRecorded(err error) bool {
	var pending *effect.PendingError
	return errors.As(err, &pending) && pending.Reason == effect.ResolutionRecordedReason
}

func tail(output []byte, limit int) string {
	if len(output) <= limit {
		return string(output)
	}
	return "…" + string(output[len(output)-limit:])
}
