package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

// ExecutionStore is the deliberately narrow persistence boundary used by
// operation executors. General API handlers continue to use *DB directly.
type ExecutionStore interface {
	RecoverExpiredOperations(context.Context) error
	ClaimNextOperation(context.Context, string, time.Duration, []string) (*model.Operation, OperationClaim, error)
	RenewOperationClaim(context.Context, OperationClaim, time.Duration) error
	DeferClaimedOperation(context.Context, OperationClaim, string, time.Time, map[string]interface{}) error
	RetryClaimedOperation(context.Context, OperationClaim, string, string, time.Time, map[string]interface{}) error
	FinishClaimedOperation(context.Context, OperationClaim, model.OperationStatus, string, map[string]interface{}) error
	AcquireAppOperationLock(context.Context, string) (AppOperationLock, bool, error)
}

// AppLockFencedExecutionStore can atomically bind terminalization to the
// current app-lock fence. Workers use it when available rather than treating a
// local context check as proof that no replacement holder exists.
type AppLockFencedExecutionStore interface {
	DeferClaimedOperationWithAppLock(context.Context, OperationClaim, AppOperationLock, string, time.Time, map[string]interface{}) error
	RetryClaimedOperationWithAppLock(context.Context, OperationClaim, AppOperationLock, string, string, time.Time, map[string]interface{}) error
	FinishClaimedOperationWithAppLock(context.Context, OperationClaim, AppOperationLock, model.OperationStatus, string, map[string]interface{}) error
}

// CronPauseRecoveryStore makes an unresolved cron pause or resume effect retryable only
// while its claimed-attempt budget remains. The final transition is fenced by
// the operation claim (and, where available, the app-lock fence) so an old
// worker cannot terminalize a successor's recovery.
//
// terminal reports that the retry budget was exhausted and the operation was
// recorded for manual recovery. Callers must leave the external-effect record
// intact: it is the evidence needed to reconcile the periodic job.
type CronPauseRecoveryStore interface {
	DeferOrFailCronPauseClaimedOperation(context.Context, OperationClaim, AppOperationLock, string, time.Time, map[string]interface{}) (terminal bool, err error)
}

// AppOperationLock is an app-scoped serialization lease. Callers must execute
// mutable work using Context and must Release it when that work ends. Context
// is canceled when the backend can no longer prove that this holder owns the
// lock; a successful acquisition is therefore never an unmonitored lease.
type AppOperationLock interface {
	Context() context.Context
	Fence() string
	Release()
}

// ErrAppOperationLockLost is the cancellation cause when a lease-backed lock
// loses its backend ownership before its caller releases it.
var ErrAppOperationLockLost = errors.New("app operation lock ownership lost")

// AppOperationLockHandle is the common implementation used by backends. A
// backend calls Fail when its ownership monitor can no longer renew or verify
// the lease. Release is idempotent so every error path can safely defer it.
type AppOperationLockHandle struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	release func()
	fence   string
	once    sync.Once
}

func NewAppOperationLock(ctx context.Context, release func()) *AppOperationLockHandle {
	return NewFencedAppOperationLock(ctx, "", release)
}

func NewFencedAppOperationLock(ctx context.Context, fence string, release func()) *AppOperationLockHandle {
	if release == nil {
		release = func() {}
	}
	lockCtx, cancel := context.WithCancelCause(ctx)
	return &AppOperationLockHandle{ctx: lockCtx, cancel: cancel, release: release, fence: fence}
}

func (l *AppOperationLockHandle) Fence() string {
	if l == nil {
		return ""
	}
	return l.fence
}

func (l *AppOperationLockHandle) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *AppOperationLockHandle) Fail(err error) {
	if l == nil {
		return
	}
	if err == nil {
		err = ErrAppOperationLockLost
	}
	l.cancel(err)
}

func (l *AppOperationLockHandle) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.cancel(nil)
		l.release()
	})
}

// OperationClaim is the immutable identity of one claimed execution. Attempts
// remain retry-budget accounting; Generation is the fencing token and changes
// on every successful claim.
type OperationClaim struct {
	operationID string
	ownerID     string
	generation  int64
}

func NewOperationClaim(operationID, ownerID string, generation int64) (OperationClaim, error) {
	claim := OperationClaim{operationID: operationID, ownerID: ownerID, generation: generation}
	if err := validateOperationClaim(claim); err != nil {
		return OperationClaim{}, err
	}
	return claim, nil
}

func (c OperationClaim) OperationID() string { return c.operationID }
func (c OperationClaim) OwnerID() string     { return c.ownerID }
func (c OperationClaim) Generation() int64   { return c.generation }

func (c OperationClaim) valid() bool {
	return strings.TrimSpace(c.operationID) != "" && strings.TrimSpace(c.ownerID) != "" && c.generation > 0
}

var ErrOperationOwnershipLost = errors.New("operation ownership lost")
var ErrOperationRetryUnsafe = errors.New("operation retry is unsafe after mutable execution")

type OperationOwnershipLostError struct {
	Claim OperationClaim
}

func (e *OperationOwnershipLostError) Error() string {
	return fmt.Sprintf("operation %s ownership lost (owner %s generation %d)", e.Claim.OperationID(), e.Claim.OwnerID(), e.Claim.Generation())
}

func (e *OperationOwnershipLostError) Unwrap() error { return ErrOperationOwnershipLost }

type OperationRetryUnsafeError struct {
	Claim OperationClaim
}

func (e *OperationRetryUnsafeError) Error() string {
	return fmt.Sprintf("operation %s cannot be retried after a mutable stage", e.Claim.OperationID())
}

func (e *OperationRetryUnsafeError) Unwrap() error { return ErrOperationRetryUnsafe }

func ownershipLost(claim OperationClaim) error { return &OperationOwnershipLostError{Claim: claim} }

func validateOperationClaim(claim OperationClaim) error {
	if !claim.valid() {
		return fmt.Errorf("operation claim is incomplete")
	}
	return nil
}

// AcquireAppOperationLock serializes mutable app operations across API
// replicas. PostgreSQL advisory locks are session-scoped, so the returned
// release function must always be called to return the pinned connection.
func (db *DB) AcquireAppOperationLock(ctx context.Context, app string) (AppOperationLock, bool, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(app) == "" {
		return nil, false, fmt.Errorf("app operation lock is unavailable")
	}
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "norn:app:"+app).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	return NewAppOperationLock(ctx, func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "norn:app:"+app)
		conn.Release()
	}), true, nil
}

type OperationFilter struct {
	App       string
	Kind      string
	Ref       string
	Status    string
	ExcludeID string
	Active    bool
	Limit     int
}

type OperationMetric struct {
	Kind            string
	Status          model.OperationStatus
	Count           int64
	DurationSeconds float64
	LastStartedUnix float64
}

func (db *DB) InsertOperation(ctx context.Context, op *model.Operation) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt)
	return err
}

func prepareOperation(op *model.Operation) ([]byte, []byte, error) {
	if op == nil {
		return nil, nil, fmt.Errorf("operation is required")
	}
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Status == "" {
		op.Status = model.OperationQueued
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now()
	}
	if op.MaxAttempts <= 0 {
		op.MaxAttempts = 1
	}
	if op.NextAttemptAt.IsZero() {
		op.NextAttemptAt = op.StartedAt
	}
	payload, err := json.Marshal(op.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode operation payload: %w", err)
	}
	metadata, err := json.Marshal(op.Metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("encode operation metadata: %w", err)
	}
	return payload, metadata, nil
}

// InsertRollbackOperation commits the rollback deployment, its regional
// checkpoints, and the durable operation receipt as one unit. This prevents an
// API interruption or idempotency race from leaving an orphaned deployment.
func (db *DB) InsertRollbackOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	if db == nil || db.Pool == nil || deployment == nil || op == nil {
		return fmt.Errorf("rollback operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	changes, err := json.Marshal(deployment.SourceChanges)
	if err != nil {
		return fmt.Errorf("encode rollback source changes: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deployments
		(id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		deployment.ID, deployment.App, deployment.CommitSHA, deployment.ImageTag, deployment.SpecDigest, deployment.Environment, deployment.SagaID, deployment.Status,
		deployment.SourceKind, deployment.SourceRef, deployment.SourceDirty, changes, deployment.StartedAt); err != nil {
		return err
	}
	for _, region := range regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions
			(deployment_id, region, nomad_region, status, desired_weight, active_weight)
			VALUES ($1, $2, $3, $4, $5, 0)`, deployment.ID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations
		(id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())`,
		op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata,
		op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertDeploymentOperation commits a queued deployment, its regional intent,
// and its durable operation together. Release promotion relies on this boundary
// so an idempotent request can never leave an orphaned deployment record.
func (db *DB) InsertDeploymentOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	if db == nil || db.Pool == nil || deployment == nil || op == nil {
		return fmt.Errorf("deployment operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(deployment.SourceChanges)
	if err != nil {
		return fmt.Errorf("encode deployment source changes: %w", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO deployments
		(id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		deployment.ID, deployment.App, deployment.CommitSHA, deployment.ImageTag, deployment.SpecDigest, deployment.Environment, deployment.SagaID, deployment.Status,
		deployment.SourceKind, deployment.SourceRef, deployment.SourceDirty, changes, deployment.StartedAt); err != nil {
		return err
	}
	for _, region := range regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions
			(deployment_id, region, nomad_region, status, desired_weight, active_weight)
			VALUES ($1, $2, $3, $4, $5, 0)`, deployment.ID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations
		(id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())`,
		op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata,
		op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetReleaseOperationByDeploymentID returns the durable request that created a
// release deployment. Qualification reads its candidate from this immutable
// operation record rather than accepting a caller-supplied provenance claim.
func (db *DB) GetReleaseOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("operation store is unavailable")
	}
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE payload->>'deploymentId'=$1 AND kind='app.deploy' ORDER BY started_at DESC LIMIT 1`, deploymentID).Scan(&id); err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}

func (db *DB) GetPromotionOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("operation store is unavailable")
	}
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE payload->>'deploymentId'=$1 AND kind='app.deploy' AND status='succeeded' AND metadata ? 'promotionQualification' LIMIT 1`, deploymentID).Scan(&id); err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}

// InsertCompletedOperation persists a terminal, planning-only operation in a
// single statement. Callers must not use InsertOperation followed by
// FinishOperation for receipts that are already complete: a failure between
// those statements leaves an ambiguous terminal record without finished_at.
func (db *DB) InsertCompletedOperation(ctx context.Context, op *model.Operation) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("operation store is unavailable")
	}
	if op == nil {
		return fmt.Errorf("operation is required")
	}
	if !op.Status.Terminal() {
		return fmt.Errorf("completed operation must have a terminal status")
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now().UTC()
	}
	if op.FinishedAt == nil {
		finished := op.StartedAt
		op.FinishedAt = &finished
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now(), $16)
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt, op.FinishedAt)
	return err
}

// FinishReservedFleetGitHubOperation turns a pre-external, signed Fleet
// GitHub reservation into its terminal receipt. The accepted payload is the
// immutable protected-plan intent; the recovered GitHub result is attached as
// completion metadata. A retry may only return the same result, which keeps a
// competing or ambiguous remote response from rewriting the reservation.
func (db *DB) FinishReservedFleetGitHubOperation(ctx context.Context, operationID, planID, kind string, status model.OperationStatus, message string, completion map[string]interface{}) (*model.Operation, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("operation store is unavailable")
	}
	if operationID == "" || planID == "" || (kind != "fleet.github.pull-request" && kind != "fleet.github.apply-dispatch") {
		return nil, fmt.Errorf("fleet GitHub reservation is incomplete")
	}
	if !status.Terminal() || completion == nil {
		return nil, fmt.Errorf("fleet GitHub completion is incomplete")
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		return nil, err
	}
	var completed bool
	err = db.Pool.QueryRow(ctx, `
		UPDATE operations
		SET status=$1, message=$2,
		    payload=payload || ($3::jsonb->'result'),
		    metadata=metadata || jsonb_build_object('fleetGitHubCompletion', $3::jsonb),
		    updated_at=now(), finished_at=now()
		WHERE id=$4 AND kind=$5 AND ref=$6 AND saga_id='' AND status='queued'
		RETURNING true
	`, status, message, encoded, operationID, kind, planID).Scan(&completed)
	if err == nil && completed {
		return db.GetOperation(ctx, operationID)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var same bool
	err = db.Pool.QueryRow(ctx, `SELECT metadata->'fleetGitHubCompletion' = $1::jsonb
		FROM operations WHERE id=$2 AND kind=$3 AND ref=$4 AND saga_id='' AND status=$5`, encoded, operationID, kind, planID, status).Scan(&same)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("fleet GitHub reservation %s is not completable", operationID)
	}
	if err != nil {
		return nil, err
	}
	if !same {
		return nil, fmt.Errorf("fleet GitHub reservation %s has a different completed result", operationID)
	}
	return db.GetOperation(ctx, operationID)
}

// CheckOperationClaim reports ErrOperationOwnershipLost unless the claim is
// still current by the database's wall clock. It neither extends nor
// changes the claim; callers use it right before an external write that no
// database transaction can fence.
func (db *DB) CheckOperationClaim(ctx context.Context, claim OperationClaim) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	var held bool
	err := db.Pool.QueryRow(ctx, `SELECT true FROM operations WHERE id = $1 AND status = 'running' AND locked_by = $2
		AND lock_generation = $3 AND locked_until > clock_timestamp()`, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownershipLost(claim)
	}
	return err
}

// WithOperationClaimFence runs publish while holding the claimed operation
// row. Claim expiry or replacement therefore cannot pass between the final
// ownership check and a node-local publication. The callback must stay small:
// it is deliberately limited to the irreversible publication boundary.
//
// The transaction has no durable writes, so it is always rolled back after
// publish. In particular, it must not Commit with the caller's context after
// publication: cancellation at that point would make a completed os.Link look
// failed even though the public pair is already visible.
func (db *DB) WithOperationClaimFence(ctx context.Context, claim OperationClaim, publish func() error) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if publish == nil {
		return fmt.Errorf("operation claim publication callback is required")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback only releases the SELECT FOR UPDATE lock. Use an independent
	// context so cancellation after the public Link cannot change its outcome.
	defer func() { _ = tx.Rollback(context.Background()) }()
	var held bool
	err = tx.QueryRow(ctx, `SELECT true FROM operations WHERE id = $1 AND status = 'running' AND locked_by = $2
		AND lock_generation = $3 AND locked_until > clock_timestamp() FOR UPDATE`, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownershipLost(claim)
	}
	if err != nil {
		return err
	}
	return publish()
}

func (db *DB) FinishClaimedOperation(ctx context.Context, claim OperationClaim, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if !status.Terminal() {
		return fmt.Errorf("operation completion status %q is not terminal", status)
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	// A successful deployment reconciliation must atomically repair the
	// deployment projection, its regions, and its terminal receipt. That is
	// CompleteDeploymentReconciliation's transaction; this generic terminal
	// path may record a failed reconciliation but cannot forge a success.
	// The terminal transition and its evidence archive intent (outbox) are
	// one statement: an operation with a saga cannot become terminal without
	// a pending intent. The intent seals nothing; the archiver fixes the
	// cutoff later, after late publication events.
	var finished int
	err := db.Pool.QueryRow(ctx, `
		WITH finished AS (
			UPDATE operations
			SET status = $1, message = $2, metadata = metadata || $3::jsonb,
			    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
			WHERE id = $4 AND status = 'running' AND locked_by = $5
			  AND lock_generation = $6 AND locked_until > now()
			  AND (kind <> 'app.deployment-reconcile' OR $1 <> 'succeeded')
			  AND (kind <> 'app.cron-trigger-reconcile' OR $1 <> 'succeeded')
			  AND (kind <> 'app.snapshot' OR
				($1 = 'succeeded' AND EXISTS (
					SELECT 1 FROM snapshot_publication_intents spi
					WHERE spi.operation_id = operations.id AND spi.state = 'published'
				)) OR
				($1 <> 'succeeded' AND NOT EXISTS (
					SELECT 1 FROM snapshot_publication_intents spi
					WHERE spi.operation_id = operations.id AND spi.state IN ('prepared', 'published')
				))
			  )
			RETURNING id, saga_id, app
		), outbox AS (
			INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
			SELECT 'ei-' || gen_random_uuid()::text, 'saga', saga_id, app, id, 1, 'pending' FROM finished WHERE saga_id <> ''
			ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING
		)
		SELECT count(*) FROM finished
	`, status, message, data, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&finished)
	if err != nil {
		return err
	}
	if finished != 1 {
		return ownershipLost(claim)
	}
	return nil
}

// DeferClaimedOperation returns an operation to the queue without consuming an
// execution attempt. It is used when another replica holds the per-app lock;
// no application work has started in that case.
// DeferClaimedOperation releases only the operation claim and preserves its
// retry budget. It is valid both before work begins and after an external
// effect starts when that effect remains fenced by a durable effect
// reservation; callers must not use it to imply that downstream work stopped.
func (db *DB) DeferClaimedOperation(ctx context.Context, claim OperationClaim, message string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	result, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'queued', message = $1, next_attempt_at = $2,
		    metadata = metadata || $3::jsonb, attempts = GREATEST(attempts - 1, 0),
		    locked_by = '', locked_until = NULL, updated_at = now()
		WHERE id = $4 AND status = 'running' AND locked_by = $5
		  AND lock_generation = $6 AND locked_until > now()
	`, message, nextAttemptAt, data, claim.OperationID(), claim.OwnerID(), claim.Generation())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ownershipLost(claim)
	}
	return nil
}

// DeferOrFailCronPauseClaimedOperation requeues an unresolved cron pause or resume
// effect without refunding the claim that performed the recovery check. Once
// MaxAttempts is reached it atomically records a failed/manual-review receipt
// and its evidence archive intent. It deliberately does not touch
// operation_effects: that durable effect evidence is required for a later
// operator reconciliation.
func (db *DB) DeferOrFailCronPauseClaimedOperation(ctx context.Context, claim OperationClaim, _ AppOperationLock, message string, nextAttemptAt time.Time, metadata map[string]interface{}) (bool, error) {
	if err := validateOperationClaim(claim); err != nil {
		return false, err
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	var terminal bool
	err := db.Pool.QueryRow(ctx, `
		WITH owned AS MATERIALIZED (
			SELECT id, attempts, max_attempts
			FROM operations
			WHERE id = $3 AND kind IN ('app.cron-pause', 'app.cron-resume', 'app.cron-trigger') AND status = 'running'
			  AND locked_by = $4 AND lock_generation = $5 AND locked_until > now()
		), deferred AS (
			UPDATE operations
			SET status = 'queued', message = $1, last_error = $1, next_attempt_at = $2,
			    metadata = metadata || $6::jsonb, locked_by = '', locked_until = NULL, updated_at = now()
			WHERE id IN (SELECT id FROM owned WHERE attempts < max_attempts)
			RETURNING false AS terminal, id, saga_id, app
		), exhausted AS (
			UPDATE operations
			SET status = 'failed',
			    message = 'cron effect recovery retry budget exhausted; manual recovery is required: ' || $1,
			    last_error = $1,
			    metadata = metadata || $6::jsonb || '{"manualRecoveryRequired":true,"externalEffectRecoveryPending":true,"retryBudgetExhausted":true}'::jsonb,
			    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
			WHERE id IN (SELECT id FROM owned WHERE attempts >= max_attempts)
			RETURNING true AS terminal, id, saga_id, app
		), changed AS (
			SELECT * FROM deferred UNION ALL SELECT * FROM exhausted
		), outbox AS (
			INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
			SELECT 'ei-' || gen_random_uuid()::text, 'saga', saga_id, app, id, 1, 'pending'
			FROM changed WHERE terminal AND saga_id <> ''
			ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING
		)
		SELECT terminal FROM changed
	`, message, nextAttemptAt, claim.OperationID(), claim.OwnerID(), claim.Generation(), data).Scan(&terminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ownershipLost(claim)
	}
	if err != nil {
		return false, err
	}
	return terminal, nil
}

func (db *DB) RetryClaimedOperation(ctx context.Context, claim OperationClaim, message, lastError string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	var owned, retryable, retried bool
	err := db.Pool.QueryRow(ctx, `
		/* operation-retry-cas */
		WITH owned AS MATERIALIZED (
			SELECT id, kind, payload
			FROM operations
			WHERE id = $5 AND status = 'running' AND locked_by = $6
			  AND lock_generation = $7 AND locked_until > now()
		), retryable AS (
			SELECT id FROM owned
			WHERE kind = 'app.preflight'
			   OR (kind = 'app.deploy' AND NOT EXISTS (
				SELECT 1 FROM deployment_steps ds
				WHERE ds.deployment_id = owned.payload->>'deploymentId'
				  AND (ds.step NOT IN ('clone', 'admission', 'build', 'artifact-admission', 'test')
				       OR ds.kind = 'mutable')
			   ))
		), updated AS (
			UPDATE operations
			SET status = 'queued', message = $1, last_error = $2,
			    next_attempt_at = $3, metadata = metadata || $4::jsonb,
			    locked_by = '', locked_until = NULL, updated_at = now()
			WHERE id IN (SELECT id FROM retryable)
			  AND status = 'running' AND locked_by = $6
			  AND lock_generation = $7 AND locked_until > now()
			RETURNING id
		)
		SELECT EXISTS(SELECT 1 FROM owned), EXISTS(SELECT 1 FROM retryable), EXISTS(SELECT 1 FROM updated)
	`, message, lastError, nextAttemptAt, data, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&owned, &retryable, &retried)
	if err != nil {
		return err
	}
	if !owned {
		return ownershipLost(claim)
	}
	if retryable && !retried {
		return ownershipLost(claim)
	}
	if !retried {
		return &OperationRetryUnsafeError{Claim: claim}
	}
	return nil
}

func (db *DB) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration, kinds []string) (*model.Operation, OperationClaim, error) {
	if strings.TrimSpace(workerID) == "" || lease <= 0 {
		return nil, OperationClaim{}, fmt.Errorf("operation claim owner and lease are required")
	}
	// Serialize a claim with fence acquisition. A plain EXISTS read in the
	// claim statement would admit a concurrent claim from an older snapshot
	// after the fence's UPDATE commits.
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, OperationClaim{}, err
	}
	defer tx.Rollback(context.Background())
	var fenceSingleton bool
	if err := tx.QueryRow(ctx, `SELECT singleton FROM runtime_mutation_fence WHERE singleton=true FOR SHARE`).Scan(&fenceSingleton); err != nil {
		return nil, OperationClaim{}, err
	}
	if !fenceSingleton {
		return nil, OperationClaim{}, ErrRuntimeMutationFenceHeld
	}
	args := []interface{}{workerID, lease.Microseconds()}
	kindClause := ""
	if len(kinds) > 0 {
		holders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			args = append(args, kind)
			holders = append(holders, fmt.Sprintf("$%d", len(args)))
		}
		kindClause = "AND kind IN (" + strings.Join(holders, ", ") + ")"
	}
	query := fmt.Sprintf(`
		WITH candidate AS (
			SELECT id
			FROM operations
			WHERE status = 'queued'
			  AND next_attempt_at <= now()
			  AND attempts < max_attempts
			  AND (locked_until IS NULL OR locked_until < now())
			  AND (NOT acceptance_required OR EXISTS (
				SELECT 1 FROM operation_acceptance_intents ai WHERE ai.operation_id = operations.id
			  ))
			  AND (kind NOT IN ('app.deploy','app.rollback','app.restart','app.scale','app.canary-promote','app.cron-pause','app.cron-resume','app.cron-schedule','app.cron-trigger','app.cron-trigger-reconcile','app.function-invoke','host.assure')
			       OR EXISTS (SELECT 1 FROM runtime_mutation_fence WHERE singleton=true AND active=false))
			  %s
			ORDER BY started_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE operations o
		SET status = 'running',
		    attempts = attempts + 1,
		    lock_generation = lock_generation + 1,
		    locked_by = $1,
		    locked_until = now() + ($2::bigint * interval '1 microsecond'),
		    updated_at = now()
		FROM candidate
		WHERE o.id = candidate.id
		RETURNING o.id, o.kind, o.app, o.saga_id, o.ref, o.status, o.risk, o.source, o.message, o.payload, o.metadata,
		          o.attempts, o.max_attempts, o.locked_by, o.lock_generation, o.locked_until, o.next_attempt_at, o.last_error,
		          o.started_at, o.updated_at, o.finished_at
	`, kindClause)

	var op model.Operation
	var payload, metadata []byte
	err = tx.QueryRow(ctx, query, args...).Scan(
		&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &payload, &metadata,
		&op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockGeneration, &op.LockedUntil, &op.NextAttemptAt, &op.LastError,
		&op.StartedAt, &op.UpdatedAt, &op.FinishedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, OperationClaim{}, nil
	}
	if err != nil {
		return nil, OperationClaim{}, err
	}
	if err := decodeOperationFields(&op, payload, metadata); err != nil {
		return nil, OperationClaim{}, err
	}
	claim, err := NewOperationClaim(op.ID, op.LockedBy, op.LockGeneration)
	if err != nil {
		return nil, OperationClaim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, OperationClaim{}, err
	}
	return &op, claim, nil
}

func (db *DB) ListOperations(ctx context.Context, filter OperationFilter) ([]model.Operation, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	clauses := []string{}
	args := []interface{}{}
	add := func(clause string, value interface{}) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.App != "" {
		add("app = $%d", filter.App)
	}
	if filter.Kind != "" {
		add("kind = $%d", filter.Kind)
	}
	if filter.Ref != "" {
		add("ref = $%d", filter.Ref)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.ExcludeID != "" {
		add("id != $%d", filter.ExcludeID)
	}
	if filter.Active {
		clauses = append(clauses, "status IN ('queued', 'running')")
	}

	query := `SELECT id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, locked_by, lock_generation, locked_until, next_attempt_at, last_error, started_at, updated_at, finished_at FROM operations`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", len(args))

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Operation
	for rows.Next() {
		var op model.Operation
		var payload, metadata []byte
		if err := rows.Scan(&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &payload, &metadata, &op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockGeneration, &op.LockedUntil, &op.NextAttemptAt, &op.LastError, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt); err != nil {
			return nil, err
		}
		if err := decodeOperationFields(&op, payload, metadata); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (db *DB) GetOperation(ctx context.Context, id string) (*model.Operation, error) {
	var op model.Operation
	var payload, metadata []byte
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata,
		       attempts, max_attempts, locked_by, lock_generation, locked_until, next_attempt_at, last_error,
		       started_at, updated_at, finished_at
		FROM operations WHERE id = $1
	`, id).Scan(
		&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message,
		&payload, &metadata, &op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockGeneration, &op.LockedUntil,
		&op.NextAttemptAt, &op.LastError, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := decodeOperationFields(&op, payload, metadata); err != nil {
		return nil, err
	}
	return &op, nil
}

func decodeOperationFields(op *model.Operation, payload, metadata []byte) error {
	if err := json.Unmarshal(payload, &op.Payload); err != nil {
		return fmt.Errorf("decode operation %s payload: %w", op.ID, err)
	}
	if err := json.Unmarshal(metadata, &op.Metadata); err != nil {
		return fmt.Errorf("decode operation %s metadata: %w", op.ID, err)
	}
	return nil
}

func (db *DB) GetOperationByIdempotencyKey(ctx context.Context, key string) (*model.Operation, error) {
	var id string
	err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE metadata->>'idempotencyKey' = $1`, key).Scan(&id)
	if err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}

func (db *DB) GetOperationByPromotionQualificationID(ctx context.Context, qualificationID string) (*model.Operation, error) {
	var id string
	err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE kind = 'app.deploy' AND metadata->'promotionQualification'->>'id' = $1`, qualificationID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}

func (db *DB) CancelQueuedOperation(ctx context.Context, id, requestedBy string) (*model.Operation, bool, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'canceled', message = 'operation canceled before execution',
		    metadata = metadata || jsonb_build_object('canceledBy', $1::text),
		    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		WHERE id = $2 AND status = 'queued'
	`, requestedBy, id)
	if err != nil {
		return nil, false, err
	}
	op, getErr := db.GetOperation(ctx, id)
	return op, result.RowsAffected() == 1, getErr
}

func (db *DB) RenewOperationClaim(ctx context.Context, claim OperationClaim, lease time.Duration) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if lease <= 0 {
		return fmt.Errorf("operation renewal lease must be positive")
	}
	result, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET locked_until = now() + ($1::bigint * interval '1 microsecond'), updated_at = now()
		WHERE id = $2 AND status = 'running' AND locked_by = $3
		  AND lock_generation = $4 AND locked_until > now()
	`, lease.Microseconds(), claim.OperationID(), claim.OwnerID(), claim.Generation())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ownershipLost(claim)
	}
	return nil
}

// RecoverExpiredOperations classifies only expired (or explicit legacy
// ownerless) running work, then reconciles deployments from their owning
// operation outcome in the same transaction. Live owners and safely requeued
// operations are preserved.
func (db *DB) RecoverExpiredOperations(ctx context.Context) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := recoverExpiredPreparedMySQLRestores(ctx, tx); err != nil {
		return err
	}
	// A MySQL restore becomes permanently ambiguous as soon as its intent is
	// executing. Claim expiry must therefore terminalize the operation and mark
	// the intent for inspection in one transaction. It must never enter the
	// generic retry path.
	if _, err = tx.Exec(ctx, `
		WITH ambiguous AS (
			UPDATE mysql_restore_intents i
			SET state = 'needs-inspection'
			FROM operations o
			WHERE i.operation_id = o.id
			  AND i.state = 'executing'
			  AND o.kind = 'database.mysql-restore'
			  AND o.status = 'running'
			  AND (o.locked_until IS NULL OR o.locked_until < now())
			RETURNING i.operation_id, i.acceptance_intent_id
		)
		UPDATE operations o
		SET status = 'failed',
		    message = 'MySQL restore ownership expired after execution began; inspect signed target and artifact evidence',
		    last_error = 'MySQL restore executor lease expired',
		    metadata = o.metadata || jsonb_build_object(
				'manualRecoveryRequired', true,
				'mysqlRestoreState', 'needs-inspection',
				'acceptanceIntentId', ambiguous.acceptance_intent_id),
		    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		FROM ambiguous
		WHERE o.id = ambiguous.operation_id
	`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE operations
		SET status = 'queued',
		    message = 'operation recovered after API restart and queued for a safe retry',
		    metadata = metadata || '{"recoveredAfterRestart":true}'::jsonb,
		    locked_by = '',
		    locked_until = NULL,
		    next_attempt_at = now(),
		    updated_at = now()
		WHERE status = 'running'
		  AND kind LIKE 'app.%'
		  AND (locked_until IS NULL OR locked_until < now())
		  AND (attempts < max_attempts OR kind IN ('app.restart', 'app.canary-promote') OR
			(kind = 'app.snapshot' AND EXISTS (
				SELECT 1 FROM snapshot_publication_intents spi
				WHERE spi.operation_id = operations.id AND spi.state IN ('prepared', 'published')
			)))
		  AND (
		    kind IN ('app.preflight', 'app.restart', 'app.canary-promote', 'app.cron-pause', 'app.cron-resume', 'app.cron-trigger', 'app.cron-trigger-reconcile', 'app.function-invoke')
		    OR (kind = 'app.snapshot' AND EXISTS (
		      SELECT 1 FROM snapshot_publication_intents spi
		      WHERE spi.operation_id = operations.id AND spi.state IN ('prepared', 'published')
		    ))
		    OR (kind = 'app.deploy' AND NOT EXISTS (
		      SELECT 1
		      FROM deployment_steps ds
		      WHERE ds.deployment_id = operations.payload->>'deploymentId'
		        AND (ds.step NOT IN ('clone', 'admission', 'build', 'artifact-admission', 'test')
		             OR ds.kind = 'mutable')
		    ))
		  )
	`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		WITH failed AS (
		UPDATE operations
		SET status = 'failed',
		    message = CASE
		      WHEN kind = 'app.deploy' THEN 'deploy interrupted after mutable stage; manual review required before retry'
		      WHEN kind = 'app.snapshot-prune' THEN 'snapshot pruning was interrupted; inspect retained files before retrying'
		      WHEN kind = 'app.snapshot-restore' THEN 'snapshot restore was interrupted; verify database integrity before retrying'
		      WHEN kind = 'app.migrate' THEN 'schema migration was interrupted; inspect migration and database state before retrying'
		      WHEN kind = 'app.rollback' THEN 'rollback was interrupted; inspect regional deployment state before retrying'
		      ELSE 'operation interrupted after a non-retryable stage; manual review required'
		    END,
		    last_error = 'operation executor lease expired',
		    metadata = metadata || '{"manualRecoveryRequired":true}'::jsonb,
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = now(),
		    finished_at = now()
		WHERE status = 'running'
		  AND kind LIKE 'app.%'
		  AND (locked_until IS NULL OR locked_until < now())
		RETURNING id,saga_id,app
		), archive_intents AS (
			INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
			SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending'
			FROM failed WHERE saga_id <> ''
			ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING
		)
		SELECT count(*) FROM failed
	`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE operations
		SET status = 'failed',
		    message = 'maintenance executor stopped before recording completion; inspect host state before retrying',
		    last_error = 'maintenance executor lease expired',
		    metadata = metadata || '{"manualRecoveryRequired":true}'::jsonb,
		    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		WHERE status = 'running'
		  AND (kind LIKE 'platform.%' OR kind LIKE 'host.%')
		  AND (locked_until IS NULL OR locked_until < now())
	`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		WITH terminal AS (
			SELECT d.id
			FROM deployments d
			JOIN operations o
			  ON o.saga_id = d.saga_id
			 AND o.kind IN ('app.deploy', 'app.rollback')
			 AND (o.payload->>'deploymentId' = d.id OR o.metadata->>'deploymentId' = d.id)
			WHERE d.status NOT IN ('deployed', 'failed')
			  AND o.status IN ('failed', 'canceled')
		), failed_regions AS (
			UPDATE deployment_regions
			SET status='failed', active_weight=0,
			    last_error=CASE WHEN last_error='' THEN 'owning operation ended without deployment completion' ELSE last_error END,
			    updated_at=now()
			WHERE deployment_id IN (SELECT id FROM terminal)
		)
		UPDATE deployments SET status='failed', finished_at=now()
		WHERE id IN (SELECT id FROM terminal)
	`); err != nil {
		return err
	}
	// A deployment without an authoritative app.deploy/app.rollback owner is
	// preserved. Legacy creation was not atomic, so passive recovery cannot
	// distinguish an abandoned row from one between deployment and operation
	// insertion. It remains visible for explicit operator reconciliation.
	return tx.Commit(ctx)
}

func (db *DB) OperationMetrics(ctx context.Context) ([]OperationMetric, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT
			kind,
			status,
			COUNT(*)::bigint,
			COALESCE(SUM(EXTRACT(EPOCH FROM (finished_at - started_at))) FILTER (WHERE finished_at IS NOT NULL), 0)::float8,
			COALESCE(EXTRACT(EPOCH FROM MAX(started_at)), 0)::float8
		FROM operations
		GROUP BY kind, status
		ORDER BY kind, status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OperationMetric
	for rows.Next() {
		var metric OperationMetric
		if err := rows.Scan(&metric.Kind, &metric.Status, &metric.Count, &metric.DurationSeconds, &metric.LastStartedUnix); err != nil {
			return nil, err
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}
