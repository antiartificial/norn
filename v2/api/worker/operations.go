package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type OperationWorker struct {
	db       *store.DB
	pipeline *pipeline.Pipeline
	id       string
	kinds    []string
	lease    time.Duration
	poll     time.Duration
}

func NewOperationWorker(db *store.DB, p *pipeline.Pipeline) *OperationWorker {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return &OperationWorker{
		db:       db,
		pipeline: p,
		id:       fmt.Sprintf("%s:%d", host, os.Getpid()),
		kinds: []string{
			"app.preflight", "app.deploy", "app.rollback", "app.snapshot",
			"app.snapshot-prune", "app.snapshot-restore", "app.migrate",
		},
		lease: 90 * time.Second,
		poll:  2 * time.Second,
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
	if err := w.db.RecoverInFlightOperations(ctx); err != nil {
		return fmt.Errorf("recover expired app operations: %w", err)
	}
	for {
		op, err := w.db.ClaimNextOperation(ctx, w.id, w.lease, w.kinds)
		if err != nil {
			return err
		}
		if op == nil {
			return nil
		}
		w.handle(ctx, op)
	}
}

func (w *OperationWorker) handle(ctx context.Context, op *model.Operation) {
	log.Printf("operation worker: claimed %s %s app=%s attempt=%d/%d", op.ID, op.Kind, op.App, op.Attempts, op.MaxAttempts)
	release, locked, lockErr := w.db.AcquireAppOperationLock(ctx, op.App)
	if lockErr != nil || !locked {
		message := "another mutable operation is active for this app"
		if lockErr != nil {
			message = "could not acquire the durable app operation lock: " + lockErr.Error()
		}
		delay := 5 * time.Second
		if err := w.db.DeferClaimedOperation(ctx, op.ID, message, time.Now().Add(delay), map[string]interface{}{"lockRetry": true}); err != nil {
			log.Printf("operation worker: defer locked operation %s: %v", op.ID, err)
		}
		return
	}
	defer release()
	executionCtx, stopRenewal := context.WithCancel(ctx)
	defer stopRenewal()
	go w.renewLease(executionCtx, op.ID)
	err := w.pipeline.ExecuteOperation(executionCtx, op)
	if err == nil {
		return
	}

	message := fmt.Sprintf("%s failed: %v", op.Kind, err)
	if op.Attempts < op.MaxAttempts {
		delay := retryDelay(op.Attempts)
		if retryErr := w.db.RetryOperation(ctx, op.ID, message, err.Error(), time.Now().Add(delay), map[string]interface{}{
			"retryDelaySeconds": int(delay.Seconds()),
		}); retryErr != nil {
			log.Printf("operation worker: retry %s: %v", op.ID, retryErr)
		}
		return
	}
	if finishErr := w.db.FinishOperation(ctx, op.ID, model.OperationFailed, message, map[string]interface{}{}); finishErr != nil {
		log.Printf("operation worker: finish failed %s: %v", op.ID, finishErr)
	}
}

func (w *OperationWorker) renewLease(ctx context.Context, operationID string) {
	ticker := time.NewTicker(w.lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.db.RenewOperationLease(ctx, operationID, w.id, time.Now().Add(w.lease)); err != nil {
				log.Printf("operation worker: renew lease %s: %v", operationID, err)
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
