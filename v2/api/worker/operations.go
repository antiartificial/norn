package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type OperationWorker struct {
	db       store.ExecutionStore
	pipeline interface {
		ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error)
	}
	id    string
	kinds []string
	lease time.Duration
	poll  time.Duration
}

func NewOperationWorker(db store.ExecutionStore, p *pipeline.Pipeline) *OperationWorker {
	return NewOperationWorkerForKinds(db, p, []string{
		"app.preflight", "app.deploy", "app.rollback", "app.deployment-reconcile", "app.restart", "app.snapshot",
		"app.snapshot-prune", "app.snapshot-restore", "app.migrate",
		"app.scale", "app.cron-pause", "app.cron-resume", "app.cron-trigger", "app.cron-trigger-reconcile",
		"app.canary-promote",
		pipeline.CatalogActivationKind, pipeline.DatabaseBaselineKind,
	})
}

// NewOperationWorkerForKinds binds a runtime to an explicit execution
// capability set. Backend-neutral runtimes use this to avoid claiming an
// operation whose aggregate they cannot execute.
func NewOperationWorkerForKinds(db store.ExecutionStore, p *pipeline.Pipeline, kinds []string) *OperationWorker {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return &OperationWorker{
		db:       db,
		pipeline: p,
		id:       fmt.Sprintf("%s:%d", host, os.Getpid()),
		kinds:    append([]string(nil), kinds...),
		lease:    90 * time.Second,
		poll:     2 * time.Second,
	}
}

func (w *OperationWorker) Run(ctx context.Context) {
	log.Printf("operation worker %s started", w.id)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("operation worker %s stopped", w.id)
			return
		case <-timer.C:
			if err := w.runOnce(ctx); err != nil {
				log.Printf("operation worker: %v", err)
			}
			timer.Reset(w.poll)
		}
	}
}

func (w *OperationWorker) runOnce(ctx context.Context) error {
	if err := w.db.RecoverExpiredOperations(ctx); err != nil {
		return fmt.Errorf("recover expired app operations: %w", err)
	}
	for {
		op, claim, err := w.db.ClaimNextOperation(ctx, w.id, w.lease, w.kinds)
		if err != nil {
			return err
		}
		if op == nil {
			return nil
		}
		w.handle(ctx, op, claim)
	}
}

func (w *OperationWorker) handle(ctx context.Context, op *model.Operation, claim store.OperationClaim) {
	log.Printf("operation worker: claimed %s %s app=%s attempt=%d/%d", op.ID, op.Kind, op.App, op.Attempts, op.MaxAttempts)
	// App-less operations (the database catalog) serialize on kind:ref,
	// which can never equal an app name (^[a-z0-9-]+$).
	lockKey := op.App
	if lockKey == "" {
		lockKey = op.Kind + ":" + op.Ref
	}
	appLock, locked, lockErr := w.db.AcquireAppOperationLock(ctx, lockKey)
	if lockErr != nil || !locked {
		message := "another mutable operation is active for this app"
		if lockErr != nil {
			message = "could not acquire the durable app operation lock: " + lockErr.Error()
		}
		delay := 5 * time.Second
		if err := w.db.DeferClaimedOperation(ctx, claim, message, time.Now().Add(delay), map[string]interface{}{"lockRetry": true}); err != nil {
			log.Printf("operation worker: defer locked operation %s: %v", op.ID, err)
		}
		return
	}
	defer appLock.Release()
	executionCtx, cancelExecution := context.WithCancel(appLock.Context())
	defer cancelExecution()
	renewalStop := make(chan struct{})
	renewalDone := make(chan error, 1)
	go func() { renewalDone <- w.renewLease(executionCtx, claim, cancelExecution, renewalStop) }()
	result, execErr := w.execute(executionCtx, op, claim)
	close(renewalStop)
	renewErr := <-renewalDone
	if renewErr != nil {
		// Do not guess whether a mutable downstream effect committed. The
		// expired-claim recovery path classifies safe retry versus review after
		// the executor has stopped and while this app lock is still held.
		log.Printf("operation worker: ownership renewal stopped %s: %v", op.ID, renewErr)
		return
	}
	if lockErr := appLock.Context().Err(); lockErr != nil {
		// A backend lease can expire while an executor is unwinding. Its result
		// cannot be terminalized because a newer holder may have started work.
		log.Printf("operation worker: app lock ownership stopped %s: %v", op.ID, context.Cause(appLock.Context()))
		return
	}
	if execErr != nil {
		if effect.IsDeferred(execErr) {
			message := fmt.Sprintf("external effect recovery pending: %v", execErr)
			metadata := deferredEffectMetadata(execErr)
			if op.Kind == "app.cron-pause" || op.Kind == "app.cron-resume" || op.Kind == "app.cron-trigger" {
				if terminal, deferErr := w.deferOrFailCronPauseClaimedOperation(ctx, claim, appLock, message, time.Now().Add(5*time.Second), metadata); deferErr != nil {
					log.Printf("operation worker: defer unresolved cron effect %s: %v", op.ID, deferErr)
				} else if terminal {
					log.Printf("operation worker: cron effect retry budget exhausted %s", op.ID)
				}
				return
			}
			if deferErr := w.deferClaimedOperation(ctx, claim, appLock, message, time.Now().Add(5*time.Second), metadata); deferErr != nil {
				log.Printf("operation worker: defer unresolved effect %s: %v", op.ID, deferErr)
			}
			return
		}
		w.recordFailure(ctx, op, claim, appLock, execErr)
		return
	}
	if result == nil {
		w.recordFailure(ctx, op, claim, appLock, fmt.Errorf("operation executor returned no result"))
		return
	}
	if result.Finished() {
		if _, fenced := w.db.(store.AppLockFencedExecutionStore); fenced {
			// The V3 adapter can fence its own terminal CAS, but this result was
			// committed by a separate effect boundary. Until that boundary accepts
			// the app-lock fence in its own transaction, publishing would make an
			// unfenced mutable execution visible.
			log.Printf("operation worker: refusing unfenced pre-finished result %s", op.ID)
			return
		}
		// Committed atomically with the effect (catalog activation or
		// PostgreSQL deployment completion).
		result.Publish(ctx)
		return
	}
	if finishErr := w.finishClaimedOperation(ctx, claim, appLock, result.Status, result.Message, result.Metadata); finishErr != nil {
		log.Printf("operation worker: finish %s: %v", op.ID, finishErr)
		return
	}
	result.Publish(ctx)
}

func deferredEffectMetadata(err error) map[string]interface{} {
	metadata := map[string]interface{}{"externalEffectRecoveryPending": true}
	var pending *effect.PendingError
	if errors.As(err, &pending) {
		if pending.EffectID != "" {
			metadata["effectId"] = pending.EffectID
		}
		if pending.Resource != "" {
			metadata["effectResource"] = pending.Resource
		}
		if pending.Reason != "" {
			metadata["effectRecoveryReason"] = pending.Reason
		}
	}
	return metadata
}

func (w *OperationWorker) execute(ctx context.Context, op *model.Operation, claim store.OperationClaim) (result *pipeline.OperationResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("operation worker: panic in %s: %v\n%s", op.ID, recovered, debug.Stack())
			result = nil
			err = fmt.Errorf("operation executor panic; inspect server logs")
		}
	}()
	return w.pipeline.ExecuteOperation(ctx, op, claim)
}

func (w *OperationWorker) recordFailure(ctx context.Context, op *model.Operation, claim store.OperationClaim, appLock store.AppOperationLock, err error) {
	message := fmt.Sprintf("%s failed: %v", op.Kind, err)
	if op.Attempts < op.MaxAttempts && (op.Kind == "app.preflight" || op.Kind == "app.deploy") {
		delay := retryDelay(op.Attempts)
		if retryErr := w.retryClaimedOperation(ctx, claim, appLock, message, err.Error(), time.Now().Add(delay), map[string]interface{}{
			"retryDelaySeconds": int(delay.Seconds()),
		}); retryErr != nil {
			if !errors.Is(retryErr, store.ErrOperationRetryUnsafe) {
				log.Printf("operation worker: retry %s: %v", op.ID, retryErr)
				return
			}
			log.Printf("operation worker: retry refused for %s: %v", op.ID, retryErr)
		} else {
			return
		}
	}
	metadata := map[string]interface{}{}
	if op.Kind != "app.preflight" {
		metadata["manualRecoveryRequired"] = true
	}
	if finishErr := w.finishClaimedOperation(ctx, claim, appLock, model.OperationFailed, message, metadata); finishErr != nil {
		log.Printf("operation worker: finish failed %s: %v", op.ID, finishErr)
	}
}

func (w *OperationWorker) deferClaimedOperation(ctx context.Context, claim store.OperationClaim, appLock store.AppOperationLock, message string, next time.Time, metadata map[string]interface{}) error {
	if fenced, ok := w.db.(store.AppLockFencedExecutionStore); ok {
		return fenced.DeferClaimedOperationWithAppLock(ctx, claim, appLock, message, next, metadata)
	}
	return w.db.DeferClaimedOperation(ctx, claim, message, next, metadata)
}

func (w *OperationWorker) deferOrFailCronPauseClaimedOperation(ctx context.Context, claim store.OperationClaim, appLock store.AppOperationLock, message string, next time.Time, metadata map[string]interface{}) (bool, error) {
	if recovery, ok := w.db.(store.CronPauseRecoveryStore); ok {
		return recovery.DeferOrFailCronPauseClaimedOperation(ctx, claim, appLock, message, next, metadata)
	}
	// Every durable production store implements the dedicated path. A legacy
	// adapter cannot safely refund an ambiguous cron-pause effect, so preserve
	// its evidence in a claim-fenced manual-review receipt.
	metadata["manualRecoveryRequired"] = true
	metadata["retryBudgetExhausted"] = true
	return true, w.finishClaimedOperation(ctx, claim, appLock, model.OperationFailed, "cron pause effect recovery path is unavailable; manual recovery is required: "+message, metadata)
}

func (w *OperationWorker) retryClaimedOperation(ctx context.Context, claim store.OperationClaim, appLock store.AppOperationLock, message, lastError string, next time.Time, metadata map[string]interface{}) error {
	if fenced, ok := w.db.(store.AppLockFencedExecutionStore); ok {
		return fenced.RetryClaimedOperationWithAppLock(ctx, claim, appLock, message, lastError, next, metadata)
	}
	return w.db.RetryClaimedOperation(ctx, claim, message, lastError, next, metadata)
}

func (w *OperationWorker) finishClaimedOperation(ctx context.Context, claim store.OperationClaim, appLock store.AppOperationLock, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if fenced, ok := w.db.(store.AppLockFencedExecutionStore); ok {
		return fenced.FinishClaimedOperationWithAppLock(ctx, claim, appLock, status, message, metadata)
	}
	return w.db.FinishClaimedOperation(ctx, claim, status, message, metadata)
}

func (w *OperationWorker) renewLease(ctx context.Context, claim store.OperationClaim, cancelExecution context.CancelFunc, stop <-chan struct{}) error {
	ticker := time.NewTicker(w.lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stop:
			return nil
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, w.lease/3)
			err := w.db.RenewOperationClaim(renewCtx, claim, w.lease)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				cancelExecution()
				return err
			}
		}
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 15 * time.Second
	}
	if attempt == 2 {
		return 45 * time.Second
	}
	return 2 * time.Minute
}
