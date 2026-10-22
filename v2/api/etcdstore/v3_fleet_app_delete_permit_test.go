package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func terminalRequest(r appdelete.Request) appdelete.TerminalRequest {
	return appdelete.TerminalRequest{SchemaVersion: appdelete.TerminalRequestSchema, PlanID: r.PlanID, AttemptID: r.AttemptID, WriterGeneration: r.WriterGeneration, WriterHolder: r.WriterHolder, Target: r.Target, DropletID: r.DropletID, ProviderAccount: r.ProviderAccount, AuthorizationSHA256: r.AuthorizationSHA256, WorkloadReceiptSHA256: r.WorkloadReceiptSHA256, DeleteRequestSHA256: r.DeleteRequestSHA256, RefreshDispatchSHA256: strings.Repeat("c", 64), StateSHA256: strings.Repeat("d", 64), ProviderAbsenceSHA256: strings.Repeat("e", 64), WorkloadSHA256: strings.Repeat("f", 64), PublicPilotSHA256: strings.Repeat("1", 64), ObservationSHA256: strings.Repeat("2", 64)}
}

func TestFleetAppDeleteTerminalBindsConsumedPermitAndIsOneUseEtcd(t *testing.T) {
	for _, target := range []struct{ name, id string }{{"app-04", "4242"}, {"app-03", "610327215"}} {
		t.Run(target.name, func(t *testing.T) {
			h, r := appDeletePermitFixture(t)
			r.Target, r.DropletID = target.name, target.id
			r.DeleteRequestSHA256 = appdelete.ExactDeleteRequestSHA256(target.id)
			if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); err != nil {
				t.Fatal(err)
			}
			request := terminalRequest(r)
			receipt, err := h.s.RecordAppDeleteTerminal(h.ctx(), request, "fleet-app-delete-verifier:work-staging")
			if err != nil || !receipt.TerminalEvidenceRecorded || receipt.Evidence != request {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			if _, err := h.s.RecordAppDeleteTerminal(h.ctx(), request, "fleet-app-delete-verifier:work-staging"); !errors.Is(err, appdelete.ErrRefused) {
				t.Fatalf("replay=%v", err)
			}
			read, err := h.s.GetAppDeleteTerminal(h.ctx(), r.PlanID)
			if err != nil || read != receipt {
				t.Fatalf("read=%+v err=%v", read, err)
			}
			attemptRows, _ := h.s.kv.Get(h.ctx(), h.s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID))
			var attempt fleet.RunnerAttempt
			if len(attemptRows.Kvs) != 1 || json.Unmarshal(attemptRows.Kvs[0].Value, &attempt) != nil || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" {
				t.Fatalf("terminal recording changed attempt: %+v", attempt)
			}
			fenceRows, _ := h.s.kv.Get(h.ctx(), h.s.fleetTargetFenceKey(h.targets["app-delete"]))
			var fence v3FleetTargetFence
			if len(fenceRows.Kvs) != 1 || json.Unmarshal(fenceRows.Kvs[0].Value, &fence) != nil || !fence.Held || fence.HolderPlanID != r.PlanID {
				t.Fatalf("terminal recording released fence: %+v", fence)
			}
		})
	}
}

func TestFleetAppDeleteTerminalRefusesCrossScopeAndTupleDriftEtcd(t *testing.T) {
	h, r := appDeletePermitFixture(t)
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); err != nil {
		t.Fatal(err)
	}
	request := terminalRequest(r)
	if _, err := h.s.RecordAppDeleteTerminal(h.ctx(), request, "fleet-attempt:"+r.AttemptID); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("attempt scope=%v", err)
	}
	request.StateSHA256 = strings.Repeat("3", 64)
	request.Target = "app-04"
	if _, err := h.s.RecordAppDeleteTerminal(h.ctx(), request, "fleet-app-delete-verifier:work-staging"); !errors.Is(err, appdelete.ErrRefused) {
		t.Fatalf("tuple drift=%v", err)
	}
	if _, err := h.s.GetAppDeleteTerminal(h.ctx(), r.PlanID); !errors.Is(err, appdelete.ErrNotFound) {
		t.Fatalf("refusal recorded terminal=%v", err)
	}
}

func TestFleetAppDeleteTerminalAllowsExpiredHeartbeatWithOriginalFenceEtcd(t *testing.T) {
	h, r := appDeletePermitFixture(t)
	if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); err != nil {
		t.Fatal(err)
	}
	key := h.s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID)
	rows, _ := h.s.kv.Get(h.ctx(), key)
	var attempt fleet.RunnerAttempt
	if len(rows.Kvs) != 1 || json.Unmarshal(rows.Kvs[0].Value, &attempt) != nil {
		t.Fatal("attempt missing")
	}
	attempt.HeartbeatExpiresAt = time.Now().Add(-time.Minute)
	raw, _ := json.Marshal(attempt)
	if _, err := h.s.kv.Put(h.ctx(), key, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.RecordAppDeleteTerminal(h.ctx(), terminalRequest(r), "fleet-app-delete-verifier:work-staging"); err != nil {
		t.Fatalf("expired heartbeat terminal=%v", err)
	}
}

func TestFleetAppDeleteTerminalRefusesChangedOriginalTenureEtcd(t *testing.T) {
	for _, kind := range []string{"generation", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			h, r := appDeletePermitFixture(t)
			if err := h.s.ConsumeAppDeleteCoPermit(h.ctx(), r, "fleet-attempt:"+r.AttemptID); err != nil {
				t.Fatal(err)
			}
			fenceKey := h.s.fleetTargetFenceKey(h.targets["app-delete"])
			rows, _ := h.s.kv.Get(h.ctx(), fenceKey)
			var fence v3FleetTargetFence
			if len(rows.Kvs) != 1 || json.Unmarshal(rows.Kvs[0].Value, &fence) != nil {
				t.Fatal("fence missing")
			}
			if kind == "generation" {
				fence.Generation++
			} else {
				fence.AuthorityEpoch++
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), fmt.Sprintf("%d", fence.AuthorityEpoch)); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(fence)
			if _, err := h.s.kv.Put(h.ctx(), fenceKey, string(raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.s.RecordAppDeleteTerminal(h.ctx(), terminalRequest(r), "fleet-app-delete-verifier:work-staging"); !errors.Is(err, appdelete.ErrRefused) {
				t.Fatalf("changed %s accepted: %v", kind, err)
			}
			if _, err := h.s.GetAppDeleteTerminal(h.ctx(), r.PlanID); !errors.Is(err, appdelete.ErrNotFound) {
				t.Fatalf("changed %s recorded terminal: %v", kind, err)
			}
		})
	}
}
