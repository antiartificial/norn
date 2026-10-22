package etcdstore

import (
	"errors"
	"testing"

	"norn/v2/api/fleet/appdelete"
)

func TestAppDeleteSnapshotCoherentAuthorityAndRegistryDrift(t *testing.T) {
	h, request := appDeletePermitFixture(t)
	got, err := h.s.GetAppDeleteSnapshot(h.ctx(), request.PlanID, request.AttemptID)
	if err != nil || got.SchemaVersion != appdelete.SnapshotSchema || got.PlanID != request.PlanID ||
		got.AttemptID != request.AttemptID || got.TargetID != h.targets["app-delete"] ||
		!got.TargetFenceHeld || got.TargetFenceHolderPlanID != request.PlanID ||
		got.TargetFenceGeneration <= 0 || got.TargetFenceAuthorityEpoch != got.CurrentAuthorityEpoch ||
		got.ConsumedPermit != nil || got.App03Admission != nil || got.App04Admission != nil || got.EtcdRevision <= 0 {
		t.Fatalf("initial snapshot=%+v err=%v", got, err)
	}
	recordApp03Admission(t, h, request)
	got, err = h.s.GetAppDeleteSnapshot(h.ctx(), request.PlanID, request.AttemptID)
	if err != nil || got.App03Admission == nil || got.App03Admission.Target != "app-03" {
		t.Fatalf("admitted snapshot=%+v err=%v", got, err)
	}
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), request, "fleet-attempt:"+request.AttemptID); err != nil {
		t.Fatal(err)
	}
	got, err = h.s.GetAppDeleteSnapshot(h.ctx(), request.PlanID, request.AttemptID)
	if err != nil || got.ConsumedPermit == nil || *got.ConsumedPermit != request {
		t.Fatalf("consumed snapshot=%+v err=%v", got, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetRegistryKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetAppDeleteSnapshot(h.ctx(), request.PlanID, request.AttemptID); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("deleted registry snapshot=%v", err)
	}
}
