package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/appdelete"
	"norn/v2/api/fleet/fleettest"
)

func appDeletePermitFixture(t *testing.T) (*etcdExecutorHarness, appdelete.Request) {
	t.Helper()
	h := newEtcdExecutorHarness(t)
	h.RegisterTarget("app-delete")
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "app-delete"})
	host := fleettest.NewExecutorHost()
	request := fleettest.ExecutorStartRequest(plan, host, false)
	request.OperationClass = "unknown"
	started := h.Start(plan, host.DeviceID, request)
	if started.Attempt == nil {
		t.Fatal("executor attempt did not start")
	}
	item := *started.Attempt
	item.CurrentPhase = "provider_applying"
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.kv.Put(h.ctx(), h.s.fleetRunnerAttemptKey(plan.ID, item.ID), string(raw)); err != nil {
		t.Fatal(err)
	}
	r := appdelete.Request{SchemaVersion: appdelete.RequestSchema, PlanID: plan.ID, AttemptID: item.ID,
		WriterGeneration: 9, WriterHolder: uuid.NewString(), Target: "app-03", DropletID: "610327215",
		ProviderAccount: uuid.NewString(), AuthorizationSHA256: strings.Repeat("a", 64),
		WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: appdelete.ExactDeleteRequestSHA256("610327215")}
	return h, r
}

func TestFleetAppDeleteCoPermitRaceReplayAndStaleEtcd(t *testing.T) {
	h, r := appDeletePermitFixture(t)
	principal := "fleet-attempt:" + r.AttemptID
	var wg sync.WaitGroup
	results := make([]error, 12)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.s.ConsumeAppDeleteCoPermit(context.Background(), r, principal)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, appdelete.ErrRefused) {
			t.Fatalf("unexpected result: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("want one permit, got %d", winners)
	}
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, principal); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("replay = %v", err)
	}
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+uuid.NewString()); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("wrong principal = %v", err)
	}
}

func TestFleetAppDeleteCoPermitRefusesFenceAndRecoveryDriftEtcd(t *testing.T) {
	for _, kind := range []string{"fence", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			h, r := appDeletePermitFixture(t)
			var key string
			var raw []byte
			if kind == "fence" {
				key = h.s.fleetTargetFenceKey(h.targets["app-delete"])
				response, err := h.s.kv.Get(h.ctx(), key)
				if err != nil || len(response.Kvs) != 1 {
					t.Fatal(err)
				}
				var fence v3FleetTargetFence
				if err := json.Unmarshal(response.Kvs[0].Value, &fence); err != nil {
					t.Fatal(err)
				}
				fence.Held = false
				raw, _ = json.Marshal(fence)
			} else {
				key = h.s.fleetExecutorMetaKey(r.PlanID, r.AttemptID)
				response, err := h.s.kv.Get(h.ctx(), key)
				if err != nil || len(response.Kvs) != 1 {
					t.Fatal(err)
				}
				var meta FleetExecutorAttemptMeta
				if err := json.Unmarshal(response.Kvs[0].Value, &meta); err != nil {
					t.Fatal(err)
				}
				meta.Recovery = true
				raw, _ = json.Marshal(meta)
			}
			if _, err := h.s.kv.Put(h.ctx(), key, string(raw)); err != nil {
				t.Fatal(err)
			}
			if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); !errors.Is(err, appdelete.ErrRefused) {
				t.Fatalf("%s drift = %v", kind, err)
			}
			permit, err := h.s.kv.Get(h.ctx(), h.s.appDeleteCoPermitKey(r.PlanID))
			if err != nil || permit.Count != 0 {
				t.Fatalf("%s drift consumed permit: count=%d err=%v", kind, permit.Count, err)
			}
		})
	}
}

func TestFleetAppDeleteCoPermitRefusesWrongPhaseBeforeConsumeEtcd(t *testing.T) {
	h, r := appDeletePermitFixture(t)
	key := h.s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID)
	response, err := h.s.kv.Get(h.ctx(), key)
	if err != nil || len(response.Kvs) != 1 {
		t.Fatal(err)
	}
	var item fleet.RunnerAttempt
	if err := json.Unmarshal(response.Kvs[0].Value, &item); err != nil {
		t.Fatal(err)
	}
	item.CurrentPhase = "prechange_verified"
	raw, _ := json.Marshal(item)
	if _, err := h.s.kv.Put(h.ctx(), key, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("wrong phase = %v", err)
	}
	permit, err := h.s.kv.Get(h.ctx(), h.s.appDeleteCoPermitKey(r.PlanID))
	if err != nil || permit.Count != 0 {
		t.Fatalf("refusal consumed permit: count=%d err=%v", permit.Count, err)
	}
}
