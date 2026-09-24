package worker

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type executionStoreFake struct {
	renew       func(context.Context, store.OperationClaim, time.Duration) error
	deferClaim  func(context.Context, store.OperationClaim, string, time.Time, map[string]interface{}) error
	retry       func(context.Context, store.OperationClaim, string, string, time.Time, map[string]interface{}) error
	finish      func(context.Context, store.OperationClaim, model.OperationStatus, string, map[string]interface{}) error
	lock        func(context.Context, string) (store.AppOperationLock, bool, error)
	finishCalls atomic.Int32
	deferCalls  atomic.Int32
}

type appLockFencedExecutionStoreFake struct {
	*executionStoreFake
	deferWithLock  func(context.Context, store.OperationClaim, store.AppOperationLock, string, time.Time, map[string]interface{}) error
	retryWithLock  func(context.Context, store.OperationClaim, store.AppOperationLock, string, string, time.Time, map[string]interface{}) error
	finishWithLock func(context.Context, store.OperationClaim, store.AppOperationLock, model.OperationStatus, string, map[string]interface{}) error
}

func (f *appLockFencedExecutionStoreFake) DeferClaimedOperationWithAppLock(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, message string, next time.Time, metadata map[string]interface{}) error {
	if f.deferWithLock != nil {
		return f.deferWithLock(ctx, claim, lock, message, next, metadata)
	}
	return f.executionStoreFake.DeferClaimedOperation(ctx, claim, message, next, metadata)
}
func (f *appLockFencedExecutionStoreFake) RetryClaimedOperationWithAppLock(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, message, last string, next time.Time, metadata map[string]interface{}) error {
	if f.retryWithLock != nil {
		return f.retryWithLock(ctx, claim, lock, message, last, next, metadata)
	}
	return f.executionStoreFake.RetryClaimedOperation(ctx, claim, message, last, next, metadata)
}
func (f *appLockFencedExecutionStoreFake) FinishClaimedOperationWithAppLock(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if f.finishWithLock != nil {
		return f.finishWithLock(ctx, claim, lock, status, message, metadata)
	}
	return f.executionStoreFake.FinishClaimedOperation(ctx, claim, status, message, metadata)
}

func (f *executionStoreFake) RecoverExpiredOperations(context.Context) error { return nil }
func (f *executionStoreFake) ClaimNextOperation(context.Context, string, time.Duration, []string) (*model.Operation, store.OperationClaim, error) {
	return nil, store.OperationClaim{}, nil
}
func (f *executionStoreFake) RenewOperationClaim(ctx context.Context, claim store.OperationClaim, lease time.Duration) error {
	if f.renew != nil {
		return f.renew(ctx, claim, lease)
	}
	return nil
}

func (f *executionStoreFake) DeferClaimedOperation(ctx context.Context, claim store.OperationClaim, message string, next time.Time, metadata map[string]interface{}) error {
	f.deferCalls.Add(1)
	if f.deferClaim != nil {
		return f.deferClaim(ctx, claim, message, next, metadata)
	}
	return nil
}
func (f *executionStoreFake) RetryClaimedOperation(ctx context.Context, claim store.OperationClaim, message, lastError string, next time.Time, metadata map[string]interface{}) error {
	if f.retry != nil {
		return f.retry(ctx, claim, message, lastError, next, metadata)
	}
	return nil
}
func (f *executionStoreFake) FinishClaimedOperation(ctx context.Context, claim store.OperationClaim, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	f.finishCalls.Add(1)
	if f.finish != nil {
		return f.finish(ctx, claim, status, message, metadata)
	}
	return nil
}
func (f *executionStoreFake) AcquireAppOperationLock(ctx context.Context, app string) (store.AppOperationLock, bool, error) {
	if f.lock != nil {
		return f.lock(ctx, app)
	}
	return store.NewAppOperationLock(ctx, nil), true, nil
}

type operationExecutorFunc func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error)

func (f operationExecutorFunc) ExecuteOperation(ctx context.Context, op *model.Operation, claim store.OperationClaim) (*pipeline.OperationResult, error) {
	return f(ctx, op, claim)
}

type maintenanceExecutorFunc func(context.Context, *model.Operation) (map[string]interface{}, error)

func (f maintenanceExecutorFunc) Execute(ctx context.Context, op *model.Operation) (map[string]interface{}, error) {
	return f(ctx, op)
}

type eventStoreFake struct {
	mu     sync.Mutex
	events []string
}

func operationClaim(t *testing.T, operationID, ownerID string, generation int64) store.OperationClaim {
	t.Helper()
	claim, err := store.NewOperationClaim(operationID, ownerID, generation)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func (f *eventStoreFake) AppendHubEvent(_ context.Context, event *hub.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event.Type)
	return nil
}

func (f *eventStoreFake) has(eventType string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.events {
		if got == eventType {
			return true
		}
	}
	return false
}

func TestOperationRenewalErrorCancelsExecutorBeforeUnlockAndSuppressesSuccess(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.deploy", App: "atlas", Attempts: 1, MaxAttempts: 2}
	executorReturned := atomic.Bool{}
	released := atomic.Bool{}
	fake := &executionStoreFake{
		renew: func(context.Context, store.OperationClaim, time.Duration) error {
			return errors.New("database unavailable")
		},
		lock: func(ctx context.Context, _ string) (store.AppOperationLock, bool, error) {
			return store.NewAppOperationLock(ctx, func() {
				if !executorReturned.Load() {
					t.Error("app lock released before executor returned")
				}
				released.Store(true)
			}), true, nil
		},
	}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: 30 * time.Millisecond, pipeline: operationExecutorFunc(func(ctx context.Context, _ *model.Operation, _ store.OperationClaim) (*pipeline.OperationResult, error) {
		<-ctx.Done()
		executorReturned.Store(true)
		return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "must not commit"}, nil
	})}
	w.handle(context.Background(), op, claim)
	if !executorReturned.Load() || !released.Load() {
		t.Fatalf("executorReturned=%v released=%v", executorReturned.Load(), released.Load())
	}
	if fake.finishCalls.Load() != 0 {
		t.Fatalf("finish calls=%d, want 0", fake.finishCalls.Load())
	}
}

func TestOperationAppLockLossSuppressesTerminalization(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.deploy", App: "atlas", Attempts: 1, MaxAttempts: 2}
	var appLock *store.AppOperationLockHandle
	fake := &executionStoreFake{
		lock: func(ctx context.Context, _ string) (store.AppOperationLock, bool, error) {
			appLock = store.NewAppOperationLock(ctx, nil)
			return appLock, true, nil
		},
		finish: func(context.Context, store.OperationClaim, model.OperationStatus, string, map[string]interface{}) error {
			t.Fatal("lost app lock must suppress terminalization")
			return nil
		},
	}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: time.Second, pipeline: operationExecutorFunc(func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
		appLock.Fail(store.ErrAppOperationLockLost)
		return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "must not commit"}, nil
	})}
	w.handle(context.Background(), op, claim)
	if fake.finishCalls.Load() != 0 {
		t.Fatalf("finish calls=%d, want 0", fake.finishCalls.Load())
	}
}

func TestOperationDeferredEffectUsesAppLockFenceAtStateTransition(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.deploy", App: "atlas", Attempts: 1, MaxAttempts: 2}
	base := &executionStoreFake{lock: func(ctx context.Context, _ string) (store.AppOperationLock, bool, error) {
		return store.NewFencedAppOperationLock(ctx, "replaced-fence", nil), true, nil
	}}
	deferCalls := 0
	fake := &appLockFencedExecutionStoreFake{executionStoreFake: base, deferWithLock: func(_ context.Context, got store.OperationClaim, lock store.AppOperationLock, _ string, _ time.Time, _ map[string]interface{}) error {
		deferCalls++
		if got != claim || lock.Fence() != "replaced-fence" {
			t.Fatalf("defer claim/fence=%+v/%q", got, lock.Fence())
		}
		// Model an etcd compare that loses to a replacement app lock after the
		// worker has already observed a healthy local lock context.
		return store.ErrOperationOwnershipLost
	}}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: time.Second, pipeline: operationExecutorFunc(func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
		return nil, &effect.PendingError{EffectID: "effect", Resource: "app/atlas/scale/web", Reason: "pending"}
	})}
	w.handle(context.Background(), op, claim)
	if deferCalls != 1 || base.deferCalls.Load() != 0 {
		t.Fatalf("fenced defers=%d unfenced defers=%d", deferCalls, base.deferCalls.Load())
	}
}

func TestOperationFailureRetryAndFinishUseAppLockFenceAtStateTransition(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.deploy", App: "atlas", Attempts: 1, MaxAttempts: 2}
	base := &executionStoreFake{lock: func(ctx context.Context, _ string) (store.AppOperationLock, bool, error) {
		return store.NewFencedAppOperationLock(ctx, "replaced-fence", nil), true, nil
	}}
	retryCalls, finishCalls := 0, 0
	fake := &appLockFencedExecutionStoreFake{executionStoreFake: base,
		retryWithLock: func(_ context.Context, _ store.OperationClaim, lock store.AppOperationLock, _ string, _ string, _ time.Time, _ map[string]interface{}) error {
			retryCalls++
			if lock.Fence() != "replaced-fence" {
				t.Fatalf("retry fence=%q", lock.Fence())
			}
			return store.ErrOperationRetryUnsafe
		},
		finishWithLock: func(_ context.Context, _ store.OperationClaim, lock store.AppOperationLock, status model.OperationStatus, _ string, _ map[string]interface{}) error {
			finishCalls++
			if lock.Fence() != "replaced-fence" || status != model.OperationFailed {
				t.Fatalf("finish fence/status=%q/%q", lock.Fence(), status)
			}
			return store.ErrOperationOwnershipLost
		},
	}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: time.Second, pipeline: operationExecutorFunc(func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
		return nil, errors.New("executor failed")
	})}
	w.handle(context.Background(), op, claim)
	if retryCalls != 1 || finishCalls != 1 || base.finishCalls.Load() != 0 {
		t.Fatalf("fenced retry/finish=%d/%d unfenced finish=%d", retryCalls, finishCalls, base.finishCalls.Load())
	}
}

func TestOperationNormalStopWaitsForInflightRenewalThenFinishes(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.preflight", App: "atlas", Attempts: 1, MaxAttempts: 2}
	renewStarted := make(chan struct{})
	releaseRenewal := make(chan struct{})
	fake := &executionStoreFake{renew: func(ctx context.Context, _ store.OperationClaim, _ time.Duration) error {
		close(renewStarted)
		select {
		case <-releaseRenewal:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: 300 * time.Millisecond, pipeline: operationExecutorFunc(func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
		<-renewStarted
		return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "complete"}, nil
	})}
	done := make(chan struct{})
	go func() { defer close(done); w.handle(context.Background(), op, claim) }()
	select {
	case <-done:
		t.Fatal("handle returned before in-flight renewal completed")
	case <-time.After(15 * time.Millisecond):
	}
	close(releaseRenewal)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handle did not finish")
	}
	if fake.finishCalls.Load() != 1 {
		t.Fatalf("finish calls=%d, want 1", fake.finishCalls.Load())
	}
}

func TestOperationDefersUnresolvedEffectWithoutRetryOrTerminalFailure(t *testing.T) {
	claim := operationClaim(t, "operation", "worker", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "app.deploy", App: "atlas", Attempts: 1, MaxAttempts: 2}
	retryCalls := atomic.Int32{}
	metadataChecked := atomic.Bool{}
	fake := &executionStoreFake{
		deferClaim: func(_ context.Context, got store.OperationClaim, message string, next time.Time, metadata map[string]interface{}) error {
			if got != claim || !metadata["externalEffectRecoveryPending"].(bool) || next.Before(time.Now()) || message == "" {
				t.Fatalf("unexpected effect deferral claim=%+v message=%q next=%s metadata=%v", got, message, next, metadata)
			}
			metadataChecked.Store(true)
			return nil
		},
		retry: func(context.Context, store.OperationClaim, string, string, time.Time, map[string]interface{}) error {
			retryCalls.Add(1)
			return nil
		},
	}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: time.Minute, pipeline: operationExecutorFunc(func(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
		return nil, &effect.PendingError{EffectID: "effect-1", Resource: "app/atlas/test", Reason: "supervisor outcome unknown"}
	})}
	w.handle(context.Background(), op, claim)
	if fake.deferCalls.Load() != 1 || retryCalls.Load() != 0 || fake.finishCalls.Load() != 0 || !metadataChecked.Load() {
		t.Fatalf("defer=%d retry=%d finish=%d metadataChecked=%v", fake.deferCalls.Load(), retryCalls.Load(), fake.finishCalls.Load(), metadataChecked.Load())
	}
}

func TestMaintenanceRenewalErrorCancelsAndDoesNotEmitCompletion(t *testing.T) {
	claim := operationClaim(t, "maintenance", "host-agent", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "platform.upgrade"}
	fake := &executionStoreFake{renew: func(context.Context, store.OperationClaim, time.Duration) error {
		return errors.New("database unavailable")
	}}
	events := &eventStoreFake{}
	executorReturned := atomic.Bool{}
	w := &MaintenanceWorker{db: fake, events: events, id: claim.OwnerID(), lease: 30 * time.Millisecond, executor: maintenanceExecutorFunc(func(ctx context.Context, _ *model.Operation) (map[string]interface{}, error) {
		<-ctx.Done()
		executorReturned.Store(true)
		return map[string]interface{}{"exitCode": 0}, nil
	})}
	w.handle(context.Background(), op, claim)
	if !executorReturned.Load() {
		t.Fatal("maintenance executor was not canceled and joined")
	}
	if fake.finishCalls.Load() != 0 || events.has("maintenance.completed") {
		t.Fatalf("finish=%d completedEvent=%v", fake.finishCalls.Load(), events.has("maintenance.completed"))
	}
}

func TestMaintenanceOwnershipLostAtFinishSuppressesCompletionEvent(t *testing.T) {
	claim := operationClaim(t, "maintenance", "host-agent", 1)
	op := &model.Operation{ID: claim.OperationID(), Kind: "platform.smoke"}
	fake := &executionStoreFake{finish: func(context.Context, store.OperationClaim, model.OperationStatus, string, map[string]interface{}) error {
		return &store.OperationOwnershipLostError{Claim: claim}
	}}
	events := &eventStoreFake{}
	w := &MaintenanceWorker{db: fake, events: events, id: claim.OwnerID(), lease: time.Minute, executor: maintenanceExecutorFunc(func(context.Context, *model.Operation) (map[string]interface{}, error) {
		return map[string]interface{}{"exitCode": 0}, nil
	})}
	w.handle(context.Background(), op, claim)
	if events.has("maintenance.completed") {
		t.Fatal("maintenance completion event emitted after ownership loss")
	}
}

func TestOperationRenewalDatabaseErrorCancelsExecutorWithPostgres(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	primary, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(primary.Close)
	broken, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(primary); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	op := &model.Operation{ID: "worker-db-error-" + uuid.NewString(), Kind: "app.preflight", App: "atlas-" + uuid.NewString(), SagaID: "saga-" + uuid.NewString(), Status: model.OperationQueued, Attempts: 0, MaxAttempts: 2, StartedAt: now, NextAttemptAt: now}
	if err := primary.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = primary.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id=$1`, op.ID) })
	claimed, claim, err := primary.ClaimNextOperation(ctx, "pg-error-worker", 120*time.Millisecond, []string{"app.preflight"})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	broken.Close()
	fake := &executionStoreFake{
		renew: func(ctx context.Context, claim store.OperationClaim, lease time.Duration) error {
			return broken.RenewOperationClaim(ctx, claim, lease)
		},
		lock: primary.AcquireAppOperationLock,
		finish: func(context.Context, store.OperationClaim, model.OperationStatus, string, map[string]interface{}) error {
			t.Fatal("renewal database error must suppress terminalization")
			return nil
		},
	}
	executorCanceled := atomic.Bool{}
	w := &OperationWorker{db: fake, id: claim.OwnerID(), lease: 120 * time.Millisecond, pipeline: operationExecutorFunc(func(ctx context.Context, _ *model.Operation, _ store.OperationClaim) (*pipeline.OperationResult, error) {
		<-ctx.Done()
		executorCanceled.Store(true)
		return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "must not commit"}, nil
	})}
	w.handle(ctx, claimed, claim)
	if !executorCanceled.Load() || fake.finishCalls.Load() != 0 {
		t.Fatalf("executorCanceled=%v finishCalls=%d", executorCanceled.Load(), fake.finishCalls.Load())
	}
	current, err := primary.GetOperation(ctx, op.ID)
	if err != nil || current.Status != model.OperationRunning {
		t.Fatalf("operation after renewal error=%+v err=%v", current, err)
	}
}
