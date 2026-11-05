package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/savedplaneffect"
)

// savedPlanEffectFixture uses the normal real-etcd test harness; it skips
// without NORN_TEST_ETCD_ENDPOINTS and performs no provider or GitHub calls.
func savedPlanEffectFixture(t *testing.T) (*etcdExecutorHarness, savedplaneffect.Claim) {
	t.Helper()
	h := newEtcdExecutorHarness(t)
	target, err := h.s.RegisterFleetTarget(h.ctx(), lifecycle.TargetIdentity{Provider: "digitalocean", ProviderAccount: "staging", StateBackend: "s3://norn-work-fleet-state/staging/nyc3/terraform.tfstate"}, []string{"cluster:staging", "environment:staging/nyc3"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	h.targets["staging"] = target.TargetID
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "staging"})
	host := fleettest.NewExecutorHost()
	started := h.Start(plan, host.DeviceID, fleettest.ExecutorStartRequest(plan, host, false))
	if started.Attempt == nil {
		t.Fatal("attempt did not start")
	}
	attempt := *started.Attempt
	attempt.CurrentPhase, attempt.WorkflowURL = "provider_applying", "https://github.com/acme/norn-fleet/actions/runs/101"
	attemptRaw, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetRunnerAttemptKey(plan.ID, attempt.ID), string(attemptRaw)); err != nil {
		t.Fatal(err)
	}
	fenceRows, err := h.s.kv.Get(h.ctx(), h.s.fleetTargetFenceKey(h.targets["staging"]))
	if err != nil || len(fenceRows.Kvs) != 1 {
		t.Fatalf("fence: %v", err)
	}
	var fence v3FleetTargetFence
	if err := json.Unmarshal(fenceRows.Kvs[0].Value, &fence); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim := savedplaneffect.Claim{SchemaVersion: savedplaneffect.ClaimSchema, ID: "00000000-0000-4000-8000-000000000001", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, AttemptID: attempt.ID, ProtectedCommit: plan.CommitSHA,
		GitHubRunID: 101, GitHubRunAttempt: 1, GitHubJobID: 201, GitHubJobName: "saved-plan-effect", GitHubWorkflowRef: "acme/norn-fleet/.github/workflows/work-staging.yml@" + plan.CommitSHA, GitHubWorkflowSHA: plan.CommitSHA, PlanRunID: 91, PlanArtifactSHA256: etcdRandomHex(64), DispatchNonceSHA256: executor.HolderNonceSHA256(plan.ID), SavedBinaryPlanSHA256: etcdRandomHex(64), SavedJSONPlanSHA256: etcdRandomHex(64), PreparationProjectionSHA256: etcdRandomHex(64), PreparedPlanReceiptSHA256: etcdRandomHex(64), BackendKey: savedplaneffect.StagingBackendKey, StateLineage: "state-lineage", PreSerial: 19, WriterGeneration: 1, WriterHolder: uuid.NewString(), TargetID: h.targets["staging"], TargetFenceGeneration: fence.Generation, AuthorityEpoch: fence.AuthorityEpoch,
		Node: savedplaneffect.App03, DropletID: "610327215", DropletURN: "do:droplet:610327215", DeleteActions: []savedplaneffect.DeleteAction{{Address: savedplaneffect.App03DropletAddress, Action: "delete", DropletID: "610327215", DropletURN: "do:droplet:610327215"}}, TransitionKind: savedplaneffect.StagingAppContraction, FromDesired: 3, ToDesired: 2, DrainReceiptSHA256: etcdRandomHex(64), VerifierAuthorizationSHA256: etcdRandomHex(64), CreatedAt: now.Add(-time.Second), ExpiresAt: now.Add(10 * time.Minute)}
	if err := claim.Validate(); err != nil {
		t.Fatal(err)
	}
	planRows, err := h.s.kv.Get(h.ctx(), h.s.opKey(plan.ID))
	if err != nil || len(planRows.Kvs) != 1 {
		t.Fatalf("plan: %v", err)
	}
	var record v3Record
	if err := json.Unmarshal(planRows.Kvs[0].Value, &record); err != nil {
		t.Fatal(err)
	}
	record.Operation.Payload["savedPlanEffectPlanBinding"] = savedplaneffect.PlanBindingForClaim(claim)
	planRaw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.opKey(plan.ID), string(planRaw)); err != nil {
		t.Fatal(err)
	}
	dispatch := v3FleetRunnerDispatch{FleetRunnerDispatchBinding: FleetRunnerDispatchBinding{PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, ApprovedHeadSHA: plan.CommitSHA, DispatchNonceSHA256: claim.DispatchNonceSHA256, RunID: claim.GitHubRunID, WorkflowURL: attempt.WorkflowURL}, CreatedAt: now}
	prepared := v3FleetGitHubDispatchPreparation{FleetGitHubDispatchPreparation: FleetGitHubDispatchPreparation{PlanID: plan.ID, PlanRunID: claim.PlanRunID, PlanSHA256: plan.PlanSHA256, ApprovedHeadSHA: plan.CommitSHA, DispatchNonceSHA256: claim.DispatchNonceSHA256}, CreatedAt: now}
	for key, value := range map[string]any{h.s.fleetRunnerDispatchKey(plan.ID): dispatch, h.s.fleetGitHubDispatchPreparationKey(plan.ID): prepared} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.kv.Put(h.ctx(), key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return h, claim
}

func TestSavedPlanEffectStoreCASReplayFenceAndStaleStateEtcd(t *testing.T) {
	h, claim := savedPlanEffectFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	if err := h.s.RecordSavedPlanEffectPlanBinding(ctx, savedplaneffect.PlanBindingForClaim(claim), "fleet-saved-plan-effect-verifier:work-staging"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordSavedPlanEffectDispatchBinding(ctx, savedplaneffect.DispatchBindingForClaim(claim), "fleet-saved-plan-effect-verifier:work-staging"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RecordSavedPlanEffectClaim(ctx, claim, "fleet-saved-plan-effect-verifier:work-staging", now); err != nil {
		t.Fatal(err)
	}
	results := make([]error, 8)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, results[i] = h.s.ConsumeSavedPlanEffectApply(context.Background(), claim.PlanID, claim.AttemptID, "fleet-attempt:"+claim.AttemptID, time.Now().UTC())
		}(i)
	}
	group.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, savedplaneffect.ErrConsumed) && !errors.Is(err, ErrSavedPlanEffectConflict) {
			t.Fatalf("consume: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("one apply grant wanted, got %d", winners)
	}
	if _, err := h.s.ConsumeSavedPlanEffectApply(ctx, claim.PlanID, claim.AttemptID, "fleet-attempt:"+claim.AttemptID, time.Now().UTC()); !errors.Is(err, savedplaneffect.ErrConsumed) {
		t.Fatalf("replay=%v", err)
	}
	// A replacement fence rejects the separately fresh, read-only continuation.
	rows, err := h.s.kv.Get(ctx, h.s.fleetTargetFenceKey(claim.TargetID))
	if err != nil || len(rows.Kvs) != 1 {
		t.Fatal(err)
	}
	var fence v3FleetTargetFence
	if err := json.Unmarshal(rows.Kvs[0].Value, &fence); err != nil {
		t.Fatal(err)
	}
	fence.Generation++
	raw, _ := json.Marshal(fence)
	if _, err := h.s.kv.Put(ctx, h.s.fleetTargetFenceKey(claim.TargetID), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.AuthorizeSavedPlanRefreshOnly(ctx, claim.PlanID, claim.AttemptID, "fleet-saved-plan-effect-verifier:work-staging", strings.Repeat("a", 64), time.Now().UTC()); !errors.Is(err, savedplaneffect.ErrInvalid) {
		t.Fatalf("stale fence refresh=%v", err)
	}
}
