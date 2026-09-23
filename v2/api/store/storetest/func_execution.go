package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunFuncExecutionStoreConformance is the backend-neutral behavioral contract
// for store.FuncExecutionStore — insert, terminal update, per-app newest-first
// listing with a limit, and a silent no-op update for a missing record.
func RunFuncExecutionStoreConformance(t *testing.T, newStore func(t *testing.T) store.FuncExecutionStore) {
	ctx := context.Background()

	t.Run("InsertUpdateGet", func(t *testing.T) {
		s := newStore(t)
		fe := &store.FuncExecution{ID: uuid.NewString(), App: "web", Process: "migrate", Status: "running", StartedAt: time.Now()}
		if err := s.InsertFuncExecution(ctx, fe); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateFuncExecution(ctx, fe.ID, "succeeded", 0, 1500); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListFuncExecutions(ctx, "web", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("list returned %d, want 1", len(list))
		}
		got := list[0]
		if got.Status != "succeeded" {
			t.Fatalf("status after update = %q", got.Status)
		}
		if got.ExitCode == nil || *got.ExitCode != 0 {
			t.Fatalf("exit code not set: %v", got.ExitCode)
		}
		if got.FinishedAt == nil {
			t.Fatal("finishedAt not set")
		}
		if got.DurationMs == nil || *got.DurationMs != 1500 {
			t.Fatalf("duration not set: %v", got.DurationMs)
		}
	})

	t.Run("UpdateMissingIsNoop", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpdateFuncExecution(ctx, "nope", "succeeded", 0, 1); err != nil {
			t.Fatalf("update of a missing execution should be a no-op, got %v", err)
		}
	})

	t.Run("ListNewestFirstWithLimit", func(t *testing.T) {
		s := newStore(t)
		older := &store.FuncExecution{ID: uuid.NewString(), App: "web", Process: "a", Status: "succeeded", StartedAt: time.Now().Add(-time.Hour)}
		newer := &store.FuncExecution{ID: uuid.NewString(), App: "web", Process: "b", Status: "succeeded", StartedAt: time.Now()}
		other := &store.FuncExecution{ID: uuid.NewString(), App: "other", Process: "c", Status: "succeeded", StartedAt: time.Now()}
		for _, fe := range []*store.FuncExecution{older, newer, other} {
			if err := s.InsertFuncExecution(ctx, fe); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListFuncExecutions(ctx, "web", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("limit not applied: got %d", len(list))
		}
		if list[0].ID != newer.ID {
			t.Fatal("list not newest-first")
		}
	})
}
