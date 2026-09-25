package store

import (
	"context"
	"errors"
	"fmt"

	"norn/v2/api/nomad"

	"github.com/jackc/pgx/v5"
)

const functionInvocationCleanupLeaseSQL = `5 minutes`

var ErrFunctionInvocationCleanupOwnershipLost = errors.New("function invocation cleanup ownership lost")

// FunctionInvocationCleanup is the public, exact variable identity held by a
// cleanup lease. It intentionally cannot carry invocation request material.
type FunctionInvocationCleanup struct {
	OperationID string
	Variable    nomad.FunctionInvocationVariableIdentity
	Token       string
}

// ClaimFunctionInvocationCleanup returns one lease-fenced cleanup intent only
// after its exact function receipt is terminal, its variable creation was
// attempted, and the receipt's immutable archive evidence is durable. The
// table deliberately carries no envelope, request body, or private bytes.
func (db *DB) ClaimFunctionInvocationCleanup(ctx context.Context) (*FunctionInvocationCleanup, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("function invocation cleanup store is unavailable")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Materialize only eligible public bindings. A receipt with no attempted
	// variable has nothing to clean; an unverified archive leaves its variable
	// intact until the evidence boundary is durable.
	if _, err := tx.Exec(ctx, `
		INSERT INTO function_invocation_cleanup_intents (operation_id, variable_path, owner_marker)
		SELECT o.id, a.target, o.id
		FROM operations o
		JOIN function_invocation_effect_attempts a
		  ON a.operation_id = o.id AND a.stage = 'variable' AND a.state = 'attempted'
		WHERE o.kind = $1
		  AND o.status IN ('succeeded', 'failed', 'canceled')
		  AND o.finished_at IS NOT NULL
		  AND o.saga_id <> ''
		  AND EXISTS (
			SELECT 1 FROM evidence_archive_intents e
			WHERE e.operation_id = o.id AND e.subject_kind = 'saga'
			  AND e.subject_id = o.saga_id AND e.state IN ('verified', 'pruned')
		  )
		ON CONFLICT (operation_id) DO NOTHING
	`, PrivateInvocationOperationKind); err != nil {
		return nil, err
	}

	var intent FunctionInvocationCleanup
	err = tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT operation_id
			FROM function_invocation_cleanup_intents
			WHERE state = 'pending' OR (state = 'claimed' AND lease_until <= clock_timestamp())
			ORDER BY created_at, operation_id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE function_invocation_cleanup_intents i
		SET state = 'claimed', claim_token = 'fic-' || gen_random_uuid()::text,
			claimed_at = clock_timestamp(), lease_until = clock_timestamp() + INTERVAL '`+functionInvocationCleanupLeaseSQL+`',
			updated_at = clock_timestamp()
		FROM candidate c
		WHERE i.operation_id = c.operation_id
		RETURNING i.operation_id, i.variable_path, i.owner_marker, i.claim_token
	`).Scan(&intent.OperationID, &intent.Variable.Path, &intent.Variable.OwnerMarker, &intent.Token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &intent, nil
}

// CompleteFunctionInvocationVariableCleanup records an exact remote absence
// or checked deletion. It accepts only the current lease token and the
// original operation/path/owner tuple, so a late worker cannot acknowledge a
// successor claim or an unrelated variable.
func (db *DB) CompleteFunctionInvocationVariableCleanup(ctx context.Context, intent FunctionInvocationCleanup) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("function invocation cleanup store is unavailable")
	}
	if intent.OperationID == "" || intent.Token == "" || intent.Variable.Path == "" || intent.Variable.OwnerMarker != intent.OperationID {
		return ErrFunctionInvocationCleanupOwnershipLost
	}
	result, err := db.Pool.Exec(ctx, `
		UPDATE function_invocation_cleanup_intents
		SET state = 'completed', claim_token = '', lease_until = NULL,
			completed_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE operation_id = $1 AND variable_path = $2 AND owner_marker = $3
		  AND state = 'claimed' AND claim_token = $4 AND lease_until > clock_timestamp()
	`, intent.OperationID, intent.Variable.Path, intent.Variable.OwnerMarker, intent.Token)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrFunctionInvocationCleanupOwnershipLost
	}
	return nil
}
