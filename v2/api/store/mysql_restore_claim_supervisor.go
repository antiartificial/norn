package store

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// mysqlRestoreClaimSupervisor keeps the private restore executor's operation
// claim alive while its SQL client is running. A restore has no retry-safe
// point after its durable intent enters executing, so a failed renewal cancels
// the SQL context and is returned to the caller. The caller must not publish a
// successful restore receipt after Stop reports that failure.
//
// Renewal is deliberately bounded: every renewal has at most one third of the
// lease to complete, and the first renewal succeeds before external work may
// start. This leaves two renewal intervals for cancellation and cleanup before
// a normally healthy claim expires.
type mysqlRestoreClaimSupervisor struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	lease    time.Duration
	interval time.Duration
	renew    func(context.Context, time.Duration) error

	stop     chan struct{}
	done     chan error
	stopOnce sync.Once
	mu       sync.RWMutex
	failure  error
}

func newMySQLRestoreClaimSupervisor(parent context.Context, lease time.Duration, renew func(context.Context, time.Duration) error) (*mysqlRestoreClaimSupervisor, error) {
	if lease <= 0 {
		return nil, fmt.Errorf("MySQL restore claim lease must be positive")
	}
	if renew == nil {
		return nil, fmt.Errorf("MySQL restore claim renewer is required")
	}
	ctx, cancel := context.WithCancelCause(parent)
	return &mysqlRestoreClaimSupervisor{
		ctx:      ctx,
		cancel:   cancel,
		lease:    lease,
		interval: maxDuration(lease/3, time.Nanosecond),
		renew:    renew,
		stop:     make(chan struct{}),
		done:     make(chan error, 1),
	}, nil
}

func (s *mysqlRestoreClaimSupervisor) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Failure reports a renewal loss without stopping the supervisor. It lets the
// terminal receipt remain fenced by the still-running supervisor: callers must
// check it immediately before attempting FinishClaimedMySQLRestore, then stop
// only after that terminal transaction returns.
func (s *mysqlRestoreClaimSupervisor) Failure() error {
	if s == nil {
		return ErrMySQLRestoreFence
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failure
}

// Start must complete before the caller starts MySQL. It extends a claim that
// may have spent most of its original lease waiting to enter this private lane.
func (s *mysqlRestoreClaimSupervisor) Start() error {
	if s == nil {
		return fmt.Errorf("MySQL restore claim supervisor is unavailable")
	}
	if err := s.renewOnce(); err != nil {
		return err
	}
	go s.run()
	return nil
}

// Stop waits for an in-flight bounded renewal. A non-nil result means ownership
// was lost while external SQL may have been in flight; callers must leave the
// durable intent for inspection and must never record success.
func (s *mysqlRestoreClaimSupervisor) Stop() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	return <-s.done
}

func (s *mysqlRestoreClaimSupervisor) run() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			s.done <- nil
			return
		case <-s.ctx.Done():
			if cause := context.Cause(s.ctx); cause != nil && cause != context.Canceled && cause != context.DeadlineExceeded {
				s.done <- cause
			} else {
				s.done <- nil
			}
			return
		case <-ticker.C:
			if err := s.renewOnce(); err != nil {
				s.done <- err
				return
			}
		}
	}
}

func (s *mysqlRestoreClaimSupervisor) renewOnce() error {
	renewCtx, cancel := context.WithTimeout(s.ctx, s.interval)
	err := s.renew(renewCtx, s.lease)
	cancel()
	if err != nil {
		s.mu.Lock()
		s.failure = err
		s.mu.Unlock()
		s.cancel(err)
	}
	return err
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
