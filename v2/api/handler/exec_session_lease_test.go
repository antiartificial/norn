package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"norn/v2/api/store"
)

func TestExecSessionRenewalLossClosesWebSocket(t *testing.T) {
	for _, tc := range []struct {
		name  string
		renew func(context.Context) (bool, error)
	}{
		{name: "ownership loss", renew: func(context.Context) (bool, error) { return false, nil }},
		{name: "database error", renew: func(context.Context) (bool, error) { return false, errors.New("database unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{execWatchIntervalOverride: time.Millisecond}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closed atomic.Bool
			done := make(chan struct{})
			go func() {
				h.watchExecSessionLease(ctx, "session", store.ExecSessionClaim{OwnerID: "runtime", OwnerToken: "token"}, tc.renew, func() { closed.Store(true) })
				close(done)
			}()
			select {
			case <-done:
				if !closed.Load() {
					t.Fatal("renewal loss did not close websocket")
				}
			case <-time.After(time.Second):
				t.Fatal("lease watcher did not stop after renewal loss")
			}
		})
	}
}
