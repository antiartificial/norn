package storetest

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunFleetGitHubDispatchStoreConformance is the backend-neutral behavioral
// contract for store.FleetGitHubDispatchStore — create/get, nonce-guarded finish,
// and errors for a missing plan or a nonce mismatch.
func RunFleetGitHubDispatchStoreConformance(t *testing.T, newStore func(t *testing.T) store.FleetGitHubDispatchStore) {
	ctx := context.Background()

	t.Run("CreateGetFinish", func(t *testing.T) {
		s := newStore(t)
		planID := uuid.NewString()
		item := store.FleetGitHubDispatch{
			PlanID: planID, PlanRunID: 7, PlanSHA256: "plansha", ApprovedHeadSHA: "head",
			FleetEnvironment: "prod", AllowDestructive: false,
			DispatchNonce: "nonce", DispatchNonceSHA256: "noncehash",
		}
		created, err := s.CreateFleetGitHubDispatch(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		if created.PlanID != planID || created.CreatedAt.IsZero() {
			t.Fatalf("create returned unexpected: %+v", created)
		}
		got, err := s.GetFleetGitHubDispatch(ctx, planID)
		if err != nil {
			t.Fatal(err)
		}
		if got.DispatchNonce != "nonce" {
			t.Fatalf("nonce not preserved: %q", got.DispatchNonce)
		}
		// Finish with the wrong nonce hash is rejected.
		if _, err := s.FinishFleetGitHubDispatch(ctx, planID, "wronghash", 99, "url"); err == nil {
			t.Fatal("finish with a mismatched nonce hash should error")
		}
		finished, err := s.FinishFleetGitHubDispatch(ctx, planID, "noncehash", 12345, "https://gh/run/12345")
		if err != nil {
			t.Fatal(err)
		}
		if finished.RunID != 12345 || finished.WorkflowURL != "https://gh/run/12345" {
			t.Fatalf("finish did not apply: %+v", finished)
		}
	})

	t.Run("GetMissingErrors", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetFleetGitHubDispatch(ctx, "missing-plan"); err == nil {
			t.Fatal("get of a missing dispatch should error")
		}
		if _, err := s.FinishFleetGitHubDispatch(ctx, "missing-plan", "h", 1, "u"); err == nil {
			t.Fatal("finish of a missing dispatch should error")
		}
	})
}
