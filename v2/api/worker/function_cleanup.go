package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// FunctionInvocationCleanup is the store-owned, lease-fenced public intent.
type FunctionInvocationCleanup = store.FunctionInvocationCleanup

// FunctionInvocationCleanupStore owns eligibility, retry scheduling, and the
// final transaction that records removal and retires private material when
// replay and key-retention policy permit it.
type FunctionInvocationCleanupStore interface {
	ClaimFunctionInvocationCleanup(context.Context) (*FunctionInvocationCleanup, error)
	CompleteFunctionInvocationVariableCleanup(context.Context, FunctionInvocationCleanup) error
}

// FunctionInvocationCleanupRemote permits only an exact read and checked
// delete. Job purge remains separate because Nomad deregistration provides no
// modify-index compare-and-swap guard.
type FunctionInvocationCleanupRemote interface {
	LookupFunctionInvocationVariable(context.Context, string, nomad.FunctionInvocationVariableIdentity) (nomad.FunctionInvocationVariableObservation, error)
	DeleteFunctionInvocationVariable(context.Context, string, nomad.FunctionInvocationVariableIdentity, uint64) error
}

var ErrFunctionInvocationCleanupConflict = errors.New("function invocation cleanup ownership conflicts with remote variable")

// FunctionInvocationCleanupConsumer handles at most one intent per call. It
// acknowledges only exact absence or a successful checked deletion.
type FunctionInvocationCleanupConsumer struct {
	Store  FunctionInvocationCleanupStore
	Remote FunctionInvocationCleanupRemote
}

// Run polls independently of invocation execution. A failed or ambiguous
// deletion leaves the durable intent for another fenced attempt.
func (c *FunctionInvocationCleanupConsumer) Run(ctx context.Context, poll time.Duration) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("function invocation cleanup: %v", err)
			}
			timer.Reset(poll)
		}
	}
}

func (c *FunctionInvocationCleanupConsumer) RunOnce(ctx context.Context) error {
	if c == nil || c.Store == nil || c.Remote == nil || ctx == nil || ctx.Err() != nil {
		return fmt.Errorf("function invocation cleanup consumer is unavailable")
	}
	intent, err := c.Store.ClaimFunctionInvocationCleanup(ctx)
	if err != nil || intent == nil {
		return err
	}
	if strings.TrimSpace(intent.OperationID) == "" || strings.TrimSpace(intent.Token) == "" || intent.Variable.OwnerMarker != intent.OperationID {
		return fmt.Errorf("function invocation cleanup intent is invalid")
	}
	observed, err := c.Remote.LookupFunctionInvocationVariable(ctx, "global", intent.Variable)
	if err != nil || observed.State == nomad.FunctionInvocationVariableIndeterminate {
		if err != nil {
			return err
		}
		return nomad.ErrFunctionVariableLookupIndeterminate
	}
	if observed.State == nomad.FunctionInvocationVariableFound {
		if observed.Path != intent.Variable.Path || observed.OwnerMarker != intent.Variable.OwnerMarker || observed.ModifyIndex == 0 {
			return ErrFunctionInvocationCleanupConflict
		}
		if err := c.Remote.DeleteFunctionInvocationVariable(ctx, "global", intent.Variable, observed.ModifyIndex); err != nil {
			return err
		}
	} else if observed.State != nomad.FunctionInvocationVariableNotFound {
		return nomad.ErrFunctionVariableLookupIndeterminate
	}
	return c.Store.CompleteFunctionInvocationVariableCleanup(ctx, *intent)
}

var _ FunctionInvocationCleanupRemote = (*nomad.Client)(nil)
