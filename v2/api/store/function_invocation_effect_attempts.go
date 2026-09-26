package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// FunctionInvocationEffectAttemptStage names the two irreversible Nomad
// calls in a function invocation. The stored target is the public Nomad path
// or job ID; private request bytes never enter this record.
type FunctionInvocationEffectAttemptStage string

const (
	FunctionInvocationVariableAttempt FunctionInvocationEffectAttemptStage = "variable"
	FunctionInvocationJobAttempt      FunctionInvocationEffectAttemptStage = "job"
)

type FunctionInvocationEffectAttempt struct {
	OperationID     string
	Stage           FunctionInvocationEffectAttemptStage
	Target          string
	InputDigest     string
	ClaimGeneration int64
	Attempted       bool
	// MarkedNow is true only for the caller that committed recorded ->
	// attempted. Only that caller may issue the corresponding remote call.
	// Recovery rereads always return false.
	MarkedNow   bool
	CreatedAt   time.Time
	AttemptedAt *time.Time
}

// FunctionInvocationEffectAttemptStore is the durable pre-call boundary for
// function workers. Implementations store only the public Nomad binding.
type FunctionInvocationEffectAttemptStore interface {
	RecordFunctionInvocationEffectStage(context.Context, OperationClaim, FunctionInvocationEffectAttemptStage, string, string) (FunctionInvocationEffectAttempt, error)
	MarkFunctionInvocationEffectAttempt(context.Context, OperationClaim, FunctionInvocationEffectAttemptStage, string, string) (FunctionInvocationEffectAttempt, error)
	LoadFunctionInvocationEffectAttempt(context.Context, string, FunctionInvocationEffectAttemptStage) (*FunctionInvocationEffectAttempt, error)
}

var (
	ErrFunctionInvocationEffectConflict = errors.New("function invocation effect stage conflicts with durable record")
	ErrFunctionInvocationEffectMissing  = errors.New("function invocation effect stage is not durably recorded")
	functionInvocationAttemptDigest     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func validFunctionInvocationEffectAttemptStage(stage FunctionInvocationEffectAttemptStage) bool {
	return stage == FunctionInvocationVariableAttempt || stage == FunctionInvocationJobAttempt
}

func validateFunctionInvocationEffectAttempt(stage FunctionInvocationEffectAttemptStage, target, inputDigest string) error {
	if !validFunctionInvocationEffectAttemptStage(stage) || strings.TrimSpace(target) == "" || len(target) > 512 || strings.ContainsAny(target, "\r\n\x00") || !functionInvocationAttemptDigest.MatchString(inputDigest) {
		return fmt.Errorf("function invocation effect attempt is invalid")
	}
	return nil
}

// RecordFunctionInvocationEffectStage writes the immutable public binding
// before a remote lookup. A retry must provide the same stage, target, and
// input digest; it re-reads the original record instead of replacing it.
func (db *DB) RecordFunctionInvocationEffectStage(ctx context.Context, claim OperationClaim, stage FunctionInvocationEffectAttemptStage, target, inputDigest string) (FunctionInvocationEffectAttempt, error) {
	if db == nil || db.Pool == nil {
		return FunctionInvocationEffectAttempt{}, fmt.Errorf("function invocation effect attempt store is unavailable")
	}
	if err := validateOperationClaim(claim); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if err := validateFunctionInvocationEffectAttempt(stage, target, inputDigest); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	defer tx.Rollback(ctx)
	if err := checkOperationClaimLocked(ctx, tx, claim); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO function_invocation_effect_attempts
			(operation_id, stage, target, input_digest, claim_generation, state)
		VALUES ($1,$2,$3,$4,$5,'recorded')
		ON CONFLICT (operation_id, stage) DO NOTHING
	`, claim.OperationID(), stage, target, inputDigest, claim.Generation()); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	stored, err := loadFunctionInvocationEffectAttempt(ctx, tx, claim.OperationID(), stage, true)
	if err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if stored.Target != target || stored.InputDigest != inputDigest {
		return stored, ErrFunctionInvocationEffectConflict
	}
	return stored, nil
}

// MarkFunctionInvocationEffectAttempt atomically compares the recorded public
// binding and advances it to attempted while holding the live operation row.
// Call it directly before the matching Nomad create or register request. Once
// attempted, repeat calls only return the durable row and never authorize a
// second remote attempt.
func (db *DB) MarkFunctionInvocationEffectAttempt(ctx context.Context, claim OperationClaim, stage FunctionInvocationEffectAttemptStage, target, inputDigest string) (FunctionInvocationEffectAttempt, error) {
	if db == nil || db.Pool == nil {
		return FunctionInvocationEffectAttempt{}, fmt.Errorf("function invocation effect attempt store is unavailable")
	}
	if err := validateOperationClaim(claim); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if err := validateFunctionInvocationEffectAttempt(stage, target, inputDigest); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	defer tx.Rollback(ctx)
	if err := checkOperationClaimLocked(ctx, tx, claim); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	stored, err := loadFunctionInvocationEffectAttempt(ctx, tx, claim.OperationID(), stage, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return FunctionInvocationEffectAttempt{}, ErrFunctionInvocationEffectMissing
	}
	if err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	if stored.Target != target || stored.InputDigest != inputDigest {
		return stored, ErrFunctionInvocationEffectConflict
	}
	markedNow := false
	if !stored.Attempted {
		if _, err := tx.Exec(ctx, `
			UPDATE function_invocation_effect_attempts
			SET state='attempted', claim_generation=$1, attempted_at=clock_timestamp(), updated_at=clock_timestamp()
			WHERE operation_id=$2 AND stage=$3 AND state='recorded' AND target=$4 AND input_digest=$5
		`, claim.Generation(), claim.OperationID(), stage, target, inputDigest); err != nil {
			return FunctionInvocationEffectAttempt{}, err
		}
		stored, err = loadFunctionInvocationEffectAttempt(ctx, tx, claim.OperationID(), stage, true)
		if err != nil {
			return FunctionInvocationEffectAttempt{}, err
		}
		markedNow = true
	}
	if err := tx.Commit(ctx); err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	stored.MarkedNow = markedNow
	return stored, nil
}

// LoadFunctionInvocationEffectAttempt is the recovery read. It returns nil
// when no public stage has been recorded and exposes no private material.
func (db *DB) LoadFunctionInvocationEffectAttempt(ctx context.Context, operationID string, stage FunctionInvocationEffectAttemptStage) (*FunctionInvocationEffectAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("function invocation effect attempt store is unavailable")
	}
	if strings.TrimSpace(operationID) == "" || !validFunctionInvocationEffectAttemptStage(stage) {
		return nil, fmt.Errorf("function invocation effect attempt lookup is invalid")
	}
	stored, err := loadFunctionInvocationEffectAttempt(ctx, db.Pool, operationID, stage, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &stored, nil
}

func checkOperationClaimLocked(ctx context.Context, tx pgx.Tx, claim OperationClaim) error {
	var kind, status, owner string
	var generation int64
	var lockedUntil *time.Time
	err := tx.QueryRow(ctx, `SELECT kind, status, locked_by, lock_generation, locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).Scan(&kind, &status, &owner, &generation, &lockedUntil)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var databaseNow time.Time
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow)
	}
	if err != nil || kind != PrivateInvocationOperationKind || status != "running" || owner != claim.OwnerID() || generation != claim.Generation() || lockedUntil == nil || !lockedUntil.After(databaseNow) {
		return ownershipLost(claim)
	}
	return nil
}

func loadFunctionInvocationEffectAttempt(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID string, stage FunctionInvocationEffectAttemptStage, lock bool) (FunctionInvocationEffectAttempt, error) {
	query := `SELECT operation_id, stage, target, input_digest, claim_generation, state, created_at, attempted_at FROM function_invocation_effect_attempts WHERE operation_id=$1 AND stage=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var stored FunctionInvocationEffectAttempt
	var state string
	err := queryer.QueryRow(ctx, query, operationID, stage).Scan(&stored.OperationID, &stored.Stage, &stored.Target, &stored.InputDigest, &stored.ClaimGeneration, &state, &stored.CreatedAt, &stored.AttemptedAt)
	if err != nil {
		return FunctionInvocationEffectAttempt{}, err
	}
	stored.Attempted = state == "attempted"
	return stored, nil
}
