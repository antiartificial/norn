package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
)

// TestEtcdConvergeRecorderRevalidationStale covers the etcd form of the M4
// staleness rule: a plan inside a closed revalidation window is stale, but a
// plan that has a dispatch row (it ran) is never stale, even after another
// plan's later release replaced LastRelease.
func TestEtcdConvergeRecorderRevalidationStale(t *testing.T) {
	client, prefix := integrationtest.Etcd(t)
	h := newEtcdLifecycleHarness(t, client, prefix)
	ctx := context.Background()
	const cluster = "stale-etcd"
	reg := h.RegisterTarget(lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "stale-etcd", StateBackend: "s3://stale-etcd/state"}, []string{"cluster:" + cluster})
	if reg.HTTPStatus != http.StatusCreated || reg.TargetID == "" {
		t.Fatalf("register: %+v", reg)
	}
	release := func(plan fleettest.PlanRef, dispatch fleettest.DispatchRef) lifecycle.FenceFacts {
		t.Helper()
		h.SetRunTerminal(fleettest.RunRef{RunID: dispatch.RunID, RunAttempt: dispatch.RunAttempt, Intent: "apply", SHA: dispatch.ApprovedHeadSHA}, true)
		held := h.FenceFacts(reg.TargetID)
		if !held.Held || held.HolderPlanID != plan.ID {
			t.Fatalf("fence not held by %s: %+v", plan.ID, held)
		}
		if resp := h.Release(reg.TargetID, lifecycle.ReleaseModeTerminal, held.Generation); resp.HTTPStatus >= http.StatusBadRequest {
			t.Fatalf("release: %d %s", resp.HTTPStatus, resp.Code)
		}
		return h.FenceFacts(reg.TargetID)
	}
	dispatch := func(plan fleettest.PlanRef, at time.Time) fleettest.DispatchRef {
		t.Helper()
		resp, d := h.Dispatch(plan, at, fleettest.GitHubOutcomeSubmitted)
		if resp.HTTPStatus >= http.StatusBadRequest || d.RunID == 0 {
			t.Fatalf("dispatch %s: %d %s", plan.ID, resp.HTTPStatus, resp.Code)
		}
		return d
	}

	// Y runs and is terminal-released; then another plan Z acquires and
	// releases, so LastRelease is Z's, not Y's.
	planY := h.SeedPlanOnCluster("scale", cluster, "")
	releasedY := release(planY, dispatch(planY, time.Now().UTC().Add(-time.Hour)))
	planZ := h.SeedPlanOnCluster("scale", cluster, "")
	releasedZ := release(planZ, dispatch(planZ, releasedY.LastRelease.At.Add(lifecycle.RevalidationSkew+time.Minute)))
	if releasedZ.LastRelease.PlanID != planZ.ID {
		t.Fatalf("fixture: LastRelease is %+v", releasedZ.LastRelease)
	}
	// W never ran and was recorded before Z's release window closed.
	planW := h.SeedPlanOnCluster("scale", cluster, "")

	recorder := NewEtcdConvergePlanRecorder(nil, h.operations)
	recorder.now = func() time.Time { return releasedZ.LastRelease.At.Add(lifecycle.RevalidationSkew + time.Minute) }
	stale := func(plan fleettest.PlanRef) bool {
		t.Helper()
		got, err := recorder.revalidationStale(ctx, &controller.ConvergeCapacityPlan{CapacityPlan: fleet.CapacityPlan{ID: plan.ID, Cluster: cluster}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !stale(planW) {
		t.Fatal("a plan with no history inside a closed revalidation window must be stale")
	}
	if stale(planY) {
		t.Fatal("a plan that ran (dispatch row) must never be stale, even after another plan's release")
	}
	recorder.now = func() time.Time { return releasedZ.LastRelease.At.Add(time.Minute) }
	if stale(planW) {
		t.Fatal("a plan inside the open revalidation window is stale")
	}
}
