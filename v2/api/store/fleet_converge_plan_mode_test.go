package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
)

// convergePlanAcceptance builds the signed fleet.capacity-plan acceptance the
// PG recorder submits, with the recording-path mode gate set.
func convergePlanAcceptance(t *testing.T, h *pgExecutionModeHarness, resource, key string) OperationAcceptance {
	t.Helper()
	authority, err := h.ops.Authority(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	id, commit := uuid.NewString(), strings.Repeat("c", 40)
	now := time.Now().UTC().Truncate(time.Microsecond)
	finished := now
	acceptance := OperationAcceptance{
		Identity: OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: authority + "/system", Subject: "fleet-converge-watcher"}, Kind: "fleet.capacity-plan", Resource: resource, Key: key},
		Audit:    AcceptanceAuditContext{Source: "fleet-converge-watcher"},
		Operation: model.Operation{
			ID: id, Kind: "fleet.capacity-plan", Ref: resource, Status: model.OperationSucceeded, MaxAttempts: 1, Source: "control-api",
			Risk: "records a converge plan; no provider mutation performed", Message: "converge plan recorded",
			Payload: map[string]interface{}{
				"id": id, "cluster": "mode-gate", "action": executor.ActionConverge, "digest": "sha256:" + strings.Repeat("a", 64),
				"converge": executor.ConvergePlan{Resource: resource, Repository: "acme/norn-fleet", CommitSHA: commit, PathClass: executor.PathClassConfig, Generation: 1},
			},
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
		},
		Semantics:                map[string]interface{}{"action": "fleet.capacity-plan", "resource": resource, "commitSha": commit},
		RequireFleetResourceMode: executor.ModeController,
	}
	if acceptance.Fingerprint, err = CanonicalOperationRequestFingerprint(acceptance); err != nil {
		t.Fatal(err)
	}
	return acceptance
}

func modeGateResource(t *testing.T, h *pgExecutionModeHarness, cluster string) string {
	t.Helper()
	h.RegisterTarget(cluster)
	return h.SeedResource(cluster, controller.DesiredRevision{
		Generation: 1, PlanID: "operator", CommitSHA: strings.Repeat("d", 40), Verification: controller.VerificationOperatorDeclared,
		AcceptedBy: "operator", AcceptedAt: time.Now().UTC(),
	})
}

func TestFleetConvergePlanRecordingRequiresControllerMode(t *testing.T) {
	h := newPGExecutionModeHarness(t)
	name := modeGateResource(t, h, "mode-gate")
	_, revision := h.ResourceMode(name)

	// github (the default): nothing is recorded.
	refused := convergePlanAcceptance(t, h, name, "k-refused")
	if _, err := h.ops.Accept(h.ctx(), refused); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("a github-mode resource must refuse the record with ErrNotWatched, got %v", err)
	}
	if got, err := h.db.GetOperation(h.ctx(), refused.Operation.ID); err == nil && got != nil {
		t.Fatal("a refused record left an operation behind")
	}
	// An unknown resource is not watched either.
	if _, err := h.ops.Accept(h.ctx(), convergePlanAcceptance(t, h, "no-such-resource", "k-missing")); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("unknown resource: %v", err)
	}

	if resp := h.SetMode(name, "to-controller", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: revision}); resp.Code != "" || resp.Failed {
		t.Fatalf("switch to controller: %+v", resp)
	}
	accepted := convergePlanAcceptance(t, h, name, "k-ok")
	if _, err := h.ops.Accept(h.ctx(), accepted); err != nil {
		t.Fatalf("controller mode records: %v", err)
	}

	// A replay is answered from the ledger without reading the mode.
	_, revision = h.ResourceMode(name)
	if resp := h.SetMode(name, "to-github", executor.ExecutionModeRequest{Mode: executor.ModeGitHub, ExpectedRevision: revision}); resp.Code != "" || resp.Failed {
		t.Fatalf("switch to github: %+v", resp)
	}
	replay, err := h.ops.Accept(h.ctx(), accepted)
	if err != nil || !replay.Replayed || replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("replay after a mode switch: %+v %v", replay, err)
	}
}

// TestFleetConvergePlanRecordRacesModeSwitch: a switch to github that lands
// while a record is in flight (after the mode check, before the commit) waits
// on the resource row and then abandons the plan the record just made.
func TestFleetConvergePlanRecordRacesModeSwitch(t *testing.T) {
	h := newPGExecutionModeHarness(t)
	name := modeGateResource(t, h, "mode-race")
	_, revision := h.ResourceMode(name)
	if resp := h.SetMode(name, "to-controller", executor.ExecutionModeRequest{Mode: executor.ModeController, ExpectedRevision: revision}); resp.Code != "" || resp.Failed {
		t.Fatalf("switch to controller: %+v", resp)
	}
	_, revision = h.ResourceMode(name)

	switched := make(chan struct{})
	var result chan error = make(chan error, 1)
	var once sync.Once
	h.ops.test.beforeCommit = func() {
		// The switch's own acceptance runs this hook too: only the record
		// starts the switch.
		once.Do(func() {
			go func() {
				defer close(switched)
				resp := h.SetMode(name, "to-github", executor.ExecutionModeRequest{Mode: executor.ModeGitHub, ExpectedRevision: revision})
				if resp.Code != "" || resp.Failed {
					result <- errors.New("switch to github failed")
				}
			}()
		})
		// Give the switch time to reach (and block on) the resource row.
		time.Sleep(300 * time.Millisecond)
	}
	acceptance := convergePlanAcceptance(t, h, name, "k-race")
	if _, err := h.ops.Accept(h.ctx(), acceptance); err != nil {
		t.Fatalf("the record held the resource row first: %v", err)
	}
	<-switched
	h.ops.test.beforeCommit = nil
	select {
	case err := <-result:
		t.Fatal(err)
	default:
	}
	if mode, _ := h.ResourceMode(name); mode != executor.ModeGitHub {
		t.Fatalf("mode after the race: %q", mode)
	}
	if !h.PlanAbandoned(acceptance.Operation.ID) {
		t.Fatal("the plan recorded before the switch must be abandoned by it")
	}
}
