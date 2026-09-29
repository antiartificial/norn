package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

// These tests exercise ordinary (unbound) Docker builds through the counted
// build-command boundary, and dirty or non-git sources, across claims of the
// same accepted operation. They complement the prebuilt-image tests.

func TestOrdinaryDeployBuildIsNotRepeatedWhileRecoveringDeferredTest(t *testing.T) {
	f := newEffectFixtureWith(t, fixtureOptions{ordinaryBuild: true})
	op := f.queue("app.deploy", true)

	f.claim(op)
	f.requirePending(op)
	if f.builds.Load() != 1 || f.backend.totalStarts() != 1 {
		t.Fatalf("claim 1 builds=%d starts=%d", f.builds.Load(), f.backend.totalStarts())
	}
	if stages := f.checkpointStages(op.ID); strings.Join(stages, ",") != "build,source" {
		t.Fatalf("claim 1 checkpoints = %v", stages)
	}

	// Claim 2 while the test is still running: no rebuild, no relaunch.
	f.claim(op)
	f.requirePending(op)
	if f.builds.Load() != 1 || f.backend.totalStarts() != 1 {
		t.Fatalf("claim 2 builds=%d starts=%d", f.builds.Load(), f.backend.totalStarts())
	}

	// The owner then crashes mid-claim; lease recovery requeues it.
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = now() - interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "crashing-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("crash claim = %+v, %v", claimed, err)
	}
	if _, err := f.pipeline.ExecuteOperation(ctx, claimed, claim); !effect.IsDeferred(err) {
		t.Fatalf("crash claim execution = %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until = now() - interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if f.builds.Load() != 1 {
		t.Fatalf("crash claim rebuilt: builds=%d", f.builds.Load())
	}

	// The original test finishes (a contained failure); the next claim
	// completes it and fails the deploy once, still without rebuilding.
	execution, _ := f.effect(op.ID)
	f.backend.finish(execution, effect.SupervisorFailed, 1, "FAIL\n")
	f.claim(op)
	current := f.operation(op.ID)
	if current.Status != model.OperationFailed || !strings.Contains(current.Message, "tests failed (exit 1)") {
		t.Fatalf("deploy after test failure = %s %q", current.Status, current.Message)
	}
	if f.builds.Load() != 1 || f.backend.totalStarts() != 1 || f.sagaCount(op.SagaID, "deploy.failed") != 1 {
		t.Fatalf("builds=%d starts=%d failed=%d", f.builds.Load(), f.backend.totalStarts(), f.sagaCount(op.SagaID, "deploy.failed"))
	}
	if reused := f.sagaCount(op.SagaID, "build.reused"); reused != 3 {
		t.Fatalf("build.reused events = %d, want one per later claim", reused)
	}
}

func TestOrdinaryPreflightBuildRecoversToCompletionWithOneBuildAndOneTest(t *testing.T) {
	f := newEffectFixtureWith(t, fixtureOptions{ordinaryBuild: true})
	op := f.queue("app.preflight", false)
	f.claim(op)
	f.requirePending(op)
	execution, _ := f.effect(op.ID)
	f.backend.finish(execution, effect.SupervisorSucceeded, 0, "ok\n")
	f.claim(op)
	if current := f.operation(op.ID); current.Status != model.OperationSucceeded {
		t.Fatalf("preflight = %s %q", current.Status, current.Message)
	}
	if f.builds.Load() != 1 || f.backend.totalStarts() != 1 || f.sagaCount(op.SagaID, "preflight.complete") != 1 {
		t.Fatalf("builds=%d starts=%d complete=%d", f.builds.Load(), f.backend.totalStarts(), f.sagaCount(op.SagaID, "preflight.complete"))
	}
}

func TestUnpinnedSourcesKeepOneOperationIdentityAcrossClaims(t *testing.T) {
	for name, source := range map[string]sourceMode{"dirty git": sourceGitDirty, "non-git": sourceNonGit} {
		t.Run(name+" unchanged reuses the original test", func(t *testing.T) {
			f := newEffectFixtureWith(t, fixtureOptions{source: source, ordinaryBuild: true})
			op := f.queue("app.preflight", false)
			f.claim(op)
			f.requirePending(op)
			execution, _ := f.effect(op.ID)
			f.backend.finish(execution, effect.SupervisorSucceeded, 0, "ok\n")
			f.claim(op)
			if current := f.operation(op.ID); current.Status != model.OperationSucceeded {
				t.Fatalf("preflight = %s %q", current.Status, current.Message)
			}
			if f.backend.totalStarts() != 1 || f.builds.Load() != 1 {
				t.Fatalf("starts=%d builds=%d", f.backend.totalStarts(), f.builds.Load())
			}
		})
		t.Run(name+" changed source never authorizes another test", func(t *testing.T) {
			f := newEffectFixtureWith(t, fixtureOptions{source: source, ordinaryBuild: true})
			op := f.queue("app.preflight", false)
			f.claim(op)
			f.requirePending(op)
			if err := os.WriteFile(filepath.Join(f.appDir, "notes.txt"), []byte("edited after the first claim\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			f.claim(op)
			current := f.operation(op.ID)
			if current.Status != model.OperationFailed || !strings.Contains(current.Message, "recorded checkpoint") {
				t.Fatalf("changed source = %s %q", current.Status, current.Message)
			}
			if f.backend.totalStarts() != 1 || f.builds.Load() != 1 {
				t.Fatalf("changed source launched or built again: starts=%d builds=%d", f.backend.totalStarts(), f.builds.Load())
			}
			// The ambiguous original execution stays gated for recovery.
			if _, lifecycle := f.effect(op.ID); lifecycle != "launched" {
				t.Fatalf("original effect lifecycle = %s", lifecycle)
			}
			if f.sagaCount(op.SagaID, "preflight.failed") != 1 {
				t.Fatal("changed-source failure was not published exactly once")
			}
		})
	}
}
