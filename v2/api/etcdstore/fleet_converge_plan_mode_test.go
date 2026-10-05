package etcdstore

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *etcdExecutionModeHarness) convergePlanAcceptance(resource, key string) store.OperationAcceptance {
	h.t.Helper()
	id, commit := uuid.NewString(), strings.Repeat("c", 40)
	now := time.Now().UTC().Truncate(time.Microsecond)
	finished := now
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: h.s.authority, Actor: store.OperationActor{Issuer: h.s.authority + "/system", Subject: "fleet-converge-watcher"}, Kind: "fleet.capacity-plan", Resource: resource, Key: key},
		Audit:    store.AcceptanceAuditContext{Source: "fleet-converge-watcher"},
		Operation: model.Operation{
			ID: id, Kind: "fleet.capacity-plan", Ref: resource, Status: model.OperationSucceeded, MaxAttempts: 1, Source: "control-api",
			Risk: "records a converge plan; no provider mutation performed", Message: "converge plan recorded",
			Payload: map[string]interface{}{
				"id": id, "cluster": "mode-gate", "action": executor.ActionConverge, "digest": "sha256:" + strings.Repeat("a", 64),
				"converge": executor.ConvergePlan{Resource: resource, Repository: "acme/norn-fleet", CommitSHA: commit, PathClass: executor.PathClassConfig, Generation: 1},
			},
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
		},
		Semantics: map[string]interface{}{"action": "fleet.capacity-plan", "resource": resource, "commitSha": commit},
	}
	var err error
	if acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance); err != nil {
		h.t.Fatal(err)
	}
	return acceptance
}

func (h *etcdExecutionModeHarness) modeResource(cluster string) (name string, revision int64) {
	h.t.Helper()
	h.RegisterTarget(cluster)
	name = h.SeedResource(cluster, controller.DesiredRevision{
		Generation: 1, PlanID: "operator", CommitSHA: strings.Repeat("d", 40), Verification: controller.VerificationOperatorDeclared,
		AcceptedBy: "operator", AcceptedAt: time.Now().UTC(),
	})
	_, revision = h.ResourceMode(name)
	return name, revision
}

func (h *etcdExecutionModeHarness) setMode(name, key, mode string) fleetModeResult {
	h.t.Helper()
	_, revision := h.ResourceMode(name)
	resp := h.SetMode(name, key, executor.ExecutionModeRequest{Mode: mode, ExpectedRevision: revision})
	return fleetModeResult{ok: resp.Code == "" && !resp.Failed, code: resp.Code}
}

type fleetModeResult struct {
	ok   bool
	code string
}

func TestFleetConvergePlanRecordingRequiresControllerModeEtcd(t *testing.T) {
	h := &etcdExecutionModeHarness{etcdExecutorHarness: newEtcdExecutorHarness(t)}
	name, _ := h.modeResource("mode-gate")

	refused := h.convergePlanAcceptance(name, "k-refused")
	if _, err := h.s.AcceptFleetConvergePlan(h.ctx(), refused); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("a github-mode resource must refuse the record with ErrNotWatched, got %v", err)
	}
	if got, err := h.s.GetOperation(h.ctx(), refused.Operation.ID); err == nil && got != nil {
		t.Fatal("a refused record left an operation behind")
	}
	if _, err := h.s.AcceptFleetConvergePlan(h.ctx(), h.convergePlanAcceptance("no-such-resource", "k-missing")); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("unknown resource: %v", err)
	}
	// The gate is applied by Accept too, for a recorder that goes through it.
	gated := h.convergePlanAcceptance(name, "k-gated")
	gated.RequireFleetResourceMode = executor.ModeController
	if _, err := h.s.Accept(h.ctx(), gated); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("Accept with RequireFleetResourceMode: %v", err)
	}

	if r := h.setMode(name, "to-controller", executor.ModeController); !r.ok {
		t.Fatalf("switch to controller: %+v", r)
	}
	accepted := h.convergePlanAcceptance(name, "k-ok")
	if _, err := h.s.AcceptFleetConvergePlan(h.ctx(), accepted); err != nil {
		t.Fatalf("controller mode records: %v", err)
	}
	if r := h.setMode(name, "to-github", executor.ModeGitHub); !r.ok {
		t.Fatalf("switch to github: %+v", r)
	}
	if !h.PlanAbandoned(accepted.Operation.ID) {
		t.Fatal("the switch abandons the recorded, unstarted plan")
	}
	// A replay is answered from the ledger without reading the mode.
	replay, err := h.s.AcceptFleetConvergePlan(h.ctx(), accepted)
	if err != nil || !replay.Replayed || replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("replay after a mode switch: %+v %v", replay, err)
	}
	// The resource key moves on every write (reconcile included), so the
	// helper retries a lost compare instead of failing the record.
	if r := h.setMode(name, "to-controller-again", executor.ModeController); !r.ok {
		t.Fatalf("switch back: %+v", r)
	}
	if _, err := h.s.AcceptFleetConvergePlan(h.ctx(), h.convergePlanAcceptance(name, "k-again")); err != nil {
		t.Fatalf("record after a switch back: %v", err)
	}
}

// TestFleetConvergePlanRecordRacesModeSwitchEtcd: whichever of a record and a
// switch to github commits first, no plan survives a resource that ended in
// github mode unabandoned.
func TestFleetConvergePlanRecordRacesModeSwitchEtcd(t *testing.T) {
	h := &etcdExecutionModeHarness{etcdExecutorHarness: newEtcdExecutorHarness(t)}
	for round := 0; round < 12; round++ {
		name, _ := h.modeResource(fmt.Sprintf("mode-race-%d", round))
		if r := h.setMode(name, "to-controller", executor.ModeController); !r.ok {
			t.Fatalf("round %d: switch to controller: %+v", round, r)
		}
		acceptance := h.convergePlanAcceptance(name, "k-race")
		var wg sync.WaitGroup
		var recordErr error
		var switched fleetModeResult
		wg.Add(2)
		go func() { defer wg.Done(); _, recordErr = h.s.AcceptFleetConvergePlan(h.ctx(), acceptance) }()
		go func() { defer wg.Done(); switched = h.setMode(name, "to-github", executor.ModeGitHub) }()
		wg.Wait()
		if !switched.ok {
			t.Fatalf("round %d: switch to github: %+v", round, switched)
		}
		if mode, _ := h.ResourceMode(name); mode != executor.ModeGitHub {
			t.Fatalf("round %d: mode %q", round, mode)
		}
		recorded := recordErr == nil
		if !recorded && !errors.Is(recordErr, controller.ErrNotWatched) {
			t.Fatalf("round %d: record failed for another reason: %v", round, recordErr)
		}
		if recorded && !h.PlanAbandoned(acceptance.Operation.ID) {
			t.Fatalf("round %d: a plan recorded before the switch survived it", round)
		}
		if !recorded {
			if got, err := h.s.GetOperation(h.ctx(), acceptance.Operation.ID); err == nil && got != nil {
				t.Fatalf("round %d: a refused record left an operation behind", round)
			}
		}
	}
}
