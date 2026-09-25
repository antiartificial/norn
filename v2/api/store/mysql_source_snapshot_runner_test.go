package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMySQLSourceQuiescenceRenewsBeforeStopAndLock(t *testing.T) {
	var renews atomic.Int32
	renewedAgain := make(chan struct{})
	var stopped atomic.Bool
	var locked atomic.Bool
	err := runClaimedMySQLSourceQuiescence(context.Background(), 90*time.Millisecond,
		func(context.Context, time.Duration) error {
			if renews.Add(1) == 2 {
				close(renewedAgain)
			}
			return nil
		},
		func(ctx context.Context) error {
			if renews.Load() < 1 || ctx.Err() != nil {
				t.Fatal("source stop began without live renewed claim")
			}
			stopped.Store(true)
			select {
			case <-renewedAgain:
				return nil
			case <-time.After(time.Second):
				return errors.New("claim was not renewed during source stop")
			}
		},
		func(ctx context.Context) error {
			if !stopped.Load() || renews.Load() < 2 || ctx.Err() != nil {
				t.Fatal("source account lock began before stop proof or without claim renewal")
			}
			locked.Store(true)
			return nil
		})
	if err != nil || !locked.Load() {
		t.Fatalf("quiescence did not finish under renewed claim: err=%v locked=%v", err, locked.Load())
	}
}

func TestMySQLSourceQuiescenceCancelsAfterRenewalLossWithoutLock(t *testing.T) {
	renewalFailure := errors.New("disposable renewal failure")
	var renews atomic.Int32
	var lockCalls atomic.Int32
	err := runClaimedMySQLSourceQuiescence(context.Background(), 45*time.Millisecond,
		func(context.Context, time.Duration) error {
			if renews.Add(1) > 1 {
				return renewalFailure
			}
			return nil
		},
		func(ctx context.Context) error {
			<-ctx.Done()
			// Even if an external stop reports success after ownership was lost, the
			// runner must not proceed to the MySQL account mutation.
			return nil
		},
		func(context.Context) error { lockCalls.Add(1); return nil })
	if !errors.Is(err, ErrMySQLSourceClaimLost) || !errors.Is(err, renewalFailure) || lockCalls.Load() != 0 {
		t.Fatalf("lease loss crossed source lock boundary: err=%v renewals=%d lockCalls=%d", err, renews.Load(), lockCalls.Load())
	}
}

func TestMySQLSourceQuiescenceRejectsInitialRenewalLoss(t *testing.T) {
	var stops atomic.Int32
	var locks atomic.Int32
	err := runClaimedMySQLSourceQuiescence(context.Background(), time.Second,
		func(context.Context, time.Duration) error { return errors.New("claim stolen") },
		func(context.Context) error { stops.Add(1); return nil },
		func(context.Context) error { locks.Add(1); return nil })
	if !errors.Is(err, ErrMySQLSourceClaimLost) || stops.Load() != 0 || locks.Load() != 0 {
		t.Fatalf("initial renewal failure reached effects: err=%v stops=%d locks=%d", err, stops.Load(), locks.Load())
	}
}

func TestMySQLSourceQuiescenceReportsLossAfterLock(t *testing.T) {
	renewalFailure := errors.New("renewal lost during MySQL lock")
	var renews atomic.Int32
	err := runClaimedMySQLSourceQuiescence(context.Background(), 45*time.Millisecond,
		func(context.Context, time.Duration) error {
			if renews.Add(1) > 1 {
				return renewalFailure
			}
			return nil
		},
		func(context.Context) error { return nil },
		func(ctx context.Context) error { <-ctx.Done(); return nil })
	if !errors.Is(err, ErrMySQLSourceClaimLost) || !errors.Is(err, renewalFailure) {
		t.Fatalf("post-lock renewal failure reported success: %v", err)
	}
}

func TestMySQLSourceQuiescenceDoesNotLockAfterAmbiguousStop(t *testing.T) {
	stopFailure := errors.New("Nomad stop response lost")
	var locks atomic.Int32
	err := runClaimedMySQLSourceQuiescence(context.Background(), time.Second,
		func(context.Context, time.Duration) error { return nil },
		func(context.Context) error { return stopFailure },
		func(context.Context) error { locks.Add(1); return nil })
	if !errors.Is(err, stopFailure) || locks.Load() != 0 {
		t.Fatalf("ambiguous stop crossed lock boundary: err=%v locks=%d", err, locks.Load())
	}
}
