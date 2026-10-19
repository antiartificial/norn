package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type offlineTerminalFixture struct {
	observation  offline.TerminalObservation
	beforeReturn func()
}

func (f offlineTerminalFixture) reobserveTerminal(context.Context, offline.Binding, offline.Attempt, offline.EffectGrant) (offline.TerminalObservation, error) {
	if f.beforeReturn != nil {
		f.beforeReturn()
	}
	return f.observation, nil
}

func terminalObservation(g offline.EffectGrant, now time.Time) offline.TerminalObservation {
	backend := g.Backend
	backend.Serial++
	backend.VersionID = "postchange-version"
	backend.RawSHA256 = strings.Repeat("9", 64)
	return offline.TerminalObservation{PlanID: g.PlanID, AttemptID: g.AttemptID, GrantID: g.ID,
		BindingSHA256: g.BindingSHA256, TargetID: g.TargetID, FenceGeneration: g.FenceGeneration,
		AuthorityEpoch: g.AuthorityEpoch, ObservedAt: now, EvidenceSHA256: strings.Repeat("a", 64),
		ProviderEvidenceSHA256: strings.Repeat("b", 64), BackendEvidenceSHA256: strings.Repeat("c", 64),
		WorkloadEvidenceSHA256: strings.Repeat("d", 64), ProviderAccountUUID: g.ProviderAccountUUID,
		NodeName: g.NodeName, DropletID: g.DropletID, Backend: backend,
		ProviderNodeAbsent: true, BackendLatestExact: true, BackendNodeAbsent: true,
		AllNamespacesInspected: true, WorkloadNodeAbsent: true, SurvivingPlacement: true,
		SurvivingHeadroom: true, ServicePassing: true}
}

func consumedOfflineGrant(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, offline.EffectGrant, time.Time) {
	t.Helper()
	h, b, a, now := offlineEffectFixture(t)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now,
		offlineTestReobserver{observation: offlineEffectObservation(b, a, now)})
	if err != nil {
		t.Fatal(err)
	}
	grant, err = h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now, offlineBackendReadFor(b))
	if err != nil {
		t.Fatal(err)
	}
	return h, b, a, grant, now
}

func TestOfflineTerminalCASAndRestore(t *testing.T) {
	h, b, a, grant, now := consumedOfflineGrant(t)
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, nil); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("nil observer accepted: %v", err)
	}
	observation := terminalObservation(grant, now)
	bad := observation
	bad.BackendNodeAbsent = false
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, offlineTerminalFixture{observation: bad}); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("state still contains node: %v", err)
	}
	bad = observation
	bad.Backend.Serial = grant.Backend.Serial
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, offlineTerminalFixture{observation: bad}); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("unadvanced state accepted: %v", err)
	}
	bad = observation
	bad.ServicePassing = false
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, offlineTerminalFixture{observation: bad}); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("failed service accepted: %v", err)
	}
	receipt, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, offlineTerminalFixture{observation: observation})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.s.GetOfflineTerminal(h.ctx(), b.PlanID)
	if err != nil || got != receipt {
		t.Fatalf("terminal restore: %+v %v", got, err)
	}
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, offlineTerminalFixture{observation: observation}); !errors.Is(err, ErrOfflineTerminalConflict) {
		t.Fatalf("replay accepted: %v", err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), b.TargetID)
	if err != nil || !fence.Held || fence.HolderPlanID != b.PlanID {
		t.Fatalf("terminal receipt released fence: %+v %v", fence, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineTerminalEventKey(b.PlanID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineTerminal(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial restore accepted: %v", err)
	}
}

func TestOfflineTerminalConcurrentFenceMutationFailsCAS(t *testing.T) {
	h, b, a, grant, now := consumedOfflineGrant(t)
	observation := terminalObservation(grant, now)
	fixture := offlineTerminalFixture{observation: observation, beforeReturn: func() {
		rows, err := h.s.kv.Get(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID))
		if err != nil || len(rows.Kvs) != 1 {
			t.Fatal(err)
		}
		var fence v3FleetTargetFence
		if err := json.Unmarshal(rows.Kvs[0].Value, &fence); err != nil {
			t.Fatal(err)
		}
		fence.Revision++
		raw, err := json.Marshal(fence)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID), string(raw)); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := h.s.recordOfflineTerminal(h.ctx(), b.PlanID, a.ID, grant.ID, now, fixture); !errors.Is(err, ErrOfflineTerminalConflict) {
		t.Fatalf("changed fence accepted: %v", err)
	}
	if _, err := h.s.GetOfflineTerminal(h.ctx(), b.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed CAS wrote terminal event: %v", err)
	}
}
