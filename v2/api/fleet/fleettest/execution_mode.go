package fleettest

import (
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
)

// Stable refusal codes ModeResp.Code carries. The harness maps each backend's
// own error to them.
const (
	ModeCodeKeyReused        = "idempotency_key_reused"
	ModeCodeRevisionConflict = "fleet_resource_revision_conflict"
	ModeCodeNotFound         = "fleet_resource_not_found"
)

// ModeResp is a backend-neutral execution-mode result. Code is the refusal
// code (the executor or acceptance code; empty on success). Failed is true
// only for an unexpected storage failure.
type ModeResp struct {
	Code         string
	OpID         string
	Replayed     bool
	PreviousMode string
	Failed       bool
}

// ExecutionModeHarness drives SetFleetResourceExecutionMode and the watched
// list on one backend, on top of the executor harness (targets, plans,
// attempts, resources). The identity is a fixed admin actor, the key is the
// caller's.
type ExecutionModeHarness interface {
	ExecutorHarness
	// SetMode applies request to resource under Idempotency-Key key.
	SetMode(resource, key string, request executor.ExecutionModeRequest) ModeResp
	// ResourceMode returns the stored executionMode ("" until set) and revision.
	ResourceMode(resource string) (mode string, revision int64)
	// PlanAbandoned reports whether planID has a permanent abandon record.
	PlanAbandoned(planID string) bool
	// ModeOperations counts the recorded fleet.resource.execution-mode
	// operations of resource.
	ModeOperations(resource string) int
	// Watched is the backend's ListWatchedResources.
	Watched() []controller.WatchedResource
}

// RunFleetExecutionModeConformance runs the shared execution-mode cases. Every
// case calls newHarness(t) for its own isolated backend.
func RunFleetExecutionModeConformance(t *testing.T, newHarness func(t *testing.T) ExecutionModeHarness) {
	cases := []struct {
		name string
		run  func(*testing.T, ExecutionModeHarness)
	}{
		{"SwitchBumpsRevisionOnce", testModeSwitchBumpsRevisionOnce},
		{"ReplayAppliesNothing", testModeReplayAppliesNothing},
		{"KeyReuseAppliesNothing", testModeKeyReuseAppliesNothing},
		{"StaleRevisionRefused", testModeStaleRevisionRefused},
		{"HeldFenceLeavesNoRow", testModeHeldFenceLeavesNoRow},
		{"GitHubAbandonsUnstartedPlans", testModeGitHubAbandonsUnstartedPlans},
		{"WatchedListsOnlyController", testModeWatchedListsOnlyController},
		{"UnknownResource", testModeUnknownResource},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) { c.run(t, newHarness(t)) })
	}
}

func modeResource(h ExecutionModeHarness, cluster string) (target, name string) {
	target = h.RegisterTarget(cluster)
	name = h.SeedResource(cluster, controller.DesiredRevision{
		Generation: 1, PlanID: "operator", CommitSHA: strings.Repeat("d", 40), Verification: controller.VerificationOperatorDeclared,
		AcceptedBy: "operator", AcceptedAt: time.Now().UTC(),
	})
	return target, name
}

func requireMode(t *testing.T, h ExecutionModeHarness, resource, mode string, revision int64) {
	t.Helper()
	if gotMode, gotRevision := h.ResourceMode(resource); gotMode != mode || gotRevision != revision {
		t.Fatalf("resource %s has mode %q revision %d, want %q revision %d", resource, gotMode, gotRevision, mode, revision)
	}
}

func requireModeOK(t *testing.T, resp ModeResp) ModeResp {
	t.Helper()
	if resp.Code != "" || resp.Failed || resp.OpID == "" {
		t.Fatalf("set mode refused or failed: %+v", resp)
	}
	return resp
}

func testModeSwitchBumpsRevisionOnce(t *testing.T, h ExecutionModeHarness) {
	_, name := modeResource(h, "mode-bump")
	_, base := h.ResourceMode(name)
	first := requireModeOK(t, h.SetMode(name, "k1", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}))
	if first.Replayed || first.PreviousMode != executor.ModeGitHub {
		t.Fatalf("first switch: %+v", first)
	}
	requireMode(t, h, name, executor.ModeController, base+1)
	// The same mode again records an operation and does not bump the revision.
	again := requireModeOK(t, h.SetMode(name, "k2", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base + 1}))
	if again.OpID == first.OpID || again.PreviousMode != executor.ModeController {
		t.Fatalf("second set: %+v", again)
	}
	requireMode(t, h, name, executor.ModeController, base+1)
	if got := h.ModeOperations(name); got != 2 {
		t.Fatalf("both changes are recorded, got %d operations", got)
	}
}

func testModeReplayAppliesNothing(t *testing.T, h ExecutionModeHarness) {
	_, name := modeResource(h, "mode-replay")
	_, base := h.ResourceMode(name)
	request := executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}
	first := requireModeOK(t, h.SetMode(name, "replay", request))
	// A lost response is retried with the same key and body: the stored
	// operation comes back and nothing is applied again, even though the
	// revision no longer matches expectedRevision.
	replay := requireModeOK(t, h.SetMode(name, "replay", request))
	if !replay.Replayed || replay.OpID != first.OpID || replay.PreviousMode != first.PreviousMode {
		t.Fatalf("replay must return the stored operation: first=%+v replay=%+v", first, replay)
	}
	requireMode(t, h, name, executor.ModeController, base+1)
	if got := h.ModeOperations(name); got != 1 {
		t.Fatalf("a replay records nothing, got %d operations", got)
	}
}

func testModeKeyReuseAppliesNothing(t *testing.T, h ExecutionModeHarness) {
	_, name := modeResource(h, "mode-reuse")
	_, base := h.ResourceMode(name)
	requireModeOK(t, h.SetMode(name, "reuse", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}))
	for label, request := range map[string]executor.ExecutionModeRequest{
		"another mode":     {Mode: executor.ModeGitHub, ExpectedRevision: base},
		"another revision": {Mode: executor.ModeController, ExpectedRevision: base + 9},
	} {
		if resp := h.SetMode(name, "reuse", request); resp.Code != ModeCodeKeyReused {
			t.Fatalf("same key with %s must be idempotency_key_reused, got %+v", label, resp)
		}
	}
	requireMode(t, h, name, executor.ModeController, base+1)
	if got := h.ModeOperations(name); got != 1 {
		t.Fatalf("a reused key records nothing, got %d operations", got)
	}
}

func testModeStaleRevisionRefused(t *testing.T, h ExecutionModeHarness) {
	_, name := modeResource(h, "mode-stale")
	_, base := h.ResourceMode(name)
	if resp := h.SetMode(name, "stale", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base + 5}); resp.Code != ModeCodeRevisionConflict {
		t.Fatalf("a stale expectedRevision must be refused, got %+v", resp)
	}
	requireMode(t, h, name, "", base)
	if got := h.ModeOperations(name); got != 0 {
		t.Fatalf("a refused change leaves no ledger row, got %d", got)
	}
	// The refusal consumed no key: the same key succeeds with the right revision.
	requireModeOK(t, h.SetMode(name, "stale", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}))
}

func testModeHeldFenceLeavesNoRow(t *testing.T, h ExecutionModeHarness) {
	// Executor-lane holder.
	target, name := modeResource(h, "mode-held")
	_, base := h.ResourceMode(name)
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "mode-held", Resource: name})
	host := NewExecutorHost()
	attempt := requireExecStarted(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)))
	request := executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}
	if resp := h.SetMode(name, "held", request); resp.Code != executor.CodeFleetExecutionModeFenceHeld {
		t.Fatalf("an executor-lane holder must refuse the change, got %+v", resp)
	}
	requireMode(t, h, name, "", base)
	if got := h.ModeOperations(name); got != 0 || !h.FenceFacts(target).Held {
		t.Fatalf("a refused change leaves no ledger row and the fence alone (ops=%d)", got)
	}
	// The refusal rolled the request identity back too: once the fence is
	// free the same key applies.
	if done := h.CompleteAttempt(attempt); done.Code != "" {
		t.Fatalf("complete: %+v", done)
	}
	requireModeOK(t, h.SetMode(name, "held", request))

	// GitHub-lane holder.
	githubTarget, githubName := modeResource(h, "mode-held-gh")
	_, githubBase := h.ResourceMode(githubName)
	if started := h.GitHubStart("mode-held-gh"); started.Code != "" || started.Attempt == nil {
		t.Fatalf("GitHub start: %+v", started)
	}
	if resp := h.SetMode(githubName, "held-gh", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: githubBase}); resp.Code != executor.CodeFleetExecutionModeFenceHeld {
		t.Fatalf("a GitHub-lane holder must refuse the change, got %+v", resp)
	}
	if got := h.ModeOperations(githubName); got != 0 || !h.FenceFacts(githubTarget).Held {
		t.Fatalf("GitHub-lane refusal leaves no row (ops=%d)", got)
	}
}

func testModeGitHubAbandonsUnstartedPlans(t *testing.T, h ExecutionModeHarness) {
	_, name := modeResource(h, "mode-abandon")
	_, base := h.ResourceMode(name)
	requireModeOK(t, h.SetMode(name, "to-controller", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}))
	revision := base + 1

	ran := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "mode-abandon", Resource: name, StartedAt: time.Now().UTC().Add(-40 * time.Minute)})
	host := NewExecutorHost()
	attempt := requireExecStarted(t, h.Start(ran, host.DeviceID, ExecutorStartRequest(ran, host, false)))
	if done := h.CompleteAttempt(attempt); done.Code != "" {
		t.Fatalf("complete: %+v", done)
	}
	older := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "mode-abandon", Resource: name, StartedAt: time.Now().UTC().Add(-30 * time.Minute)})
	newer := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "mode-abandon", Resource: name, StartedAt: time.Now().UTC().Add(-time.Minute)})
	// A plan of another resource is untouched.
	_, otherName := modeResource(h, "mode-abandon-other")
	other := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "mode-abandon-other", Resource: otherName})

	resp := requireModeOK(t, h.SetMode(name, "to-github", executor.ExecutionModeRequest{Mode: executor.ModeGitHub, ExpectedRevision: revision}))
	if resp.PreviousMode != executor.ModeController {
		t.Fatalf("previous mode: %+v", resp)
	}
	requireMode(t, h, name, executor.ModeGitHub, revision+1)
	for label, want := range map[string]struct {
		plan      ExecutorPlanRef
		abandoned bool
	}{
		"older unstarted": {older, true}, "newer unstarted": {newer, true}, "the plan that ran": {ran, false}, "another resource's plan": {other, false},
	} {
		if got := h.PlanAbandoned(want.plan.ID); got != want.abandoned {
			t.Fatalf("%s: abandoned=%v, want %v", label, got, want.abandoned)
		}
	}
	// Start never reads the mode (T7); it refuses an abandoned plan.
	next := NewExecutorHost()
	requireExecRefused(t, h.Start(newer, next.DeviceID, ExecutorStartRequest(newer, next, false)), lifecycle.CodeFleetTargetHolderAbandoned)
}

func testModeWatchedListsOnlyController(t *testing.T, h ExecutionModeHarness) {
	_, controllerName := modeResource(h, "watch-controller")
	_, githubName := modeResource(h, "watch-github")
	if watched := h.Watched(); len(watched) != 0 {
		t.Fatalf("no resource is in controller mode yet: %+v", watched)
	}
	_, base := h.ResourceMode(controllerName)
	requireModeOK(t, h.SetMode(controllerName, "watch", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: base}))
	_, githubBase := h.ResourceMode(githubName)
	requireModeOK(t, h.SetMode(githubName, "watch-gh", executor.ExecutionModeRequest{Mode: executor.ModeGitHub, ExpectedRevision: githubBase}))
	watched := h.Watched()
	if len(watched) != 1 || watched[0].Name != controllerName || watched[0].Cluster != "watch-controller" || watched[0].LastApplied != "" || watched[0].DesiredGeneration != 1 {
		t.Fatalf("only the controller resource is watched: %+v", watched)
	}
}

func testModeUnknownResource(t *testing.T, h ExecutionModeHarness) {
	h.RegisterTarget("mode-unknown")
	if resp := h.SetMode("no-such-resource", "unknown", executor.ExecutionModeRequest{Mode: executor.ModeController}); resp.Code != ModeCodeNotFound {
		t.Fatalf("an unknown resource must be refused, got %+v", resp)
	}
}
