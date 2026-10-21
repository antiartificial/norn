package fleettest

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
)

// ExecutorPlanSpec describes the converge plan a harness records (a
// fleet.capacity-plan ledger operation with action "converge" in E0's pinned
// shape). Zero values mean: the harness's default registered cluster, path
// class config without approval, generation 1, started ten minutes ago (clear
// of the revalidation skew).
type ExecutorPlanSpec struct {
	Cluster string
	// Resource is the converge plan's resource; "" means Cluster. Only the
	// newest converge plan of a resource may make a first start.
	Resource         string
	PathClass        string
	RequiresApproval bool
	Generation       int64
	StartedAt        time.Time
	// NotConverge records an ordinary capacity plan (no converge object), as
	// the GitHub lane uses.
	NotConverge bool
}

// ExecutorPlanRef names a recorded converge plan.
type ExecutorPlanRef struct {
	ID         string
	Cluster    string
	CommitSHA  string
	PlanSHA256 string
	Generation int64
}

// ExecutorHost is one executor loop's identity: the device that authenticates
// (principal_subject managed-device:<DeviceID>) and the host facts of one
// systemd invocation.
type ExecutorHost struct {
	DeviceID     string
	DropletID    string
	MachineID    string
	BootID       string
	InvocationID string
}

// NewExecutorHost returns a fresh, valid host identity.
func NewExecutorHost() ExecutorHost {
	return ExecutorHost{
		DeviceID:     "device-" + uuid.NewString(),
		DropletID:    fmt.Sprintf("%d", 100000000+uuid.New().ID()%900000000),
		MachineID:    strings.ReplaceAll(uuid.NewString(), "-", ""),
		BootID:       uuid.NewString(),
		InvocationID: strings.ReplaceAll(uuid.NewString(), "-", ""),
	}
}

// NextInvocation is the same host after a service restart: same device,
// droplet, machine and boot, a new invocation.
func (h ExecutorHost) NextInvocation() ExecutorHost {
	h.InvocationID = strings.ReplaceAll(uuid.NewString(), "-", "")
	return h
}

// ExecResp is a backend-neutral admission result. Code is the problem code of
// a refusal (empty on success). Failed is true only for an unexpected storage
// failure that is not a refusal.
type ExecResp struct {
	Code     string
	Attempt  *fleet.RunnerAttempt
	Replayed bool
	Failed   bool
}

// GitHubStartResult is a full GitHub-lane acquire, bind and insert, driven
// through the backend's existing path.
type GitHubStartResult struct {
	PlanID      string
	NonceSHA256 string
	Code        string
	Attempt     *fleet.RunnerAttempt
}

// ExecutorHarness drives one backend's executor admission and the existing
// GitHub-lane path that shares its fence. The PG harness calls the store
// directly (E2); E2e supplies the etcd one. Approve, attestation and provider
// evidence are seeded straight into the ledger in E0's pinned shapes (E4b and
// E5 are not required).
type ExecutorHarness interface {
	ExpectedInitialPhase() string
	// RegisterTarget registers a fresh target with the alias cluster:<cluster>
	// and returns its target ID.
	RegisterTarget(cluster string) string
	SeedConvergePlan(spec ExecutorPlanSpec) ExecutorPlanRef
	// SeedApprove records a fleet.converge.approve ledger operation.
	SeedApprove(plan ExecutorPlanRef, commitSHA string, resume bool, sourceAttemptID string)
	// SeedRefusal records a fleet.converge.refusal ledger operation (Ref =
	// plan ID) with the given code for commitSHA.
	SeedRefusal(plan ExecutorPlanRef, code, commitSHA string)
	// SeedStopAttestation records a fleet.executor.stop-attestation ledger
	// operation attributed to attesterDeviceID.
	SeedStopAttestation(plan ExecutorPlanRef, attesterDeviceID string, request executor.StopAttestationRequest)
	// SeedProviderEvidence records an admin, non-releasing providerEvidence
	// assertion (store.KindFleetExecutorProviderEvidence on PG) for plan's
	// source attempt sourceAttemptID: its signed admission carries the
	// evidence and the server-stored digest sits in operation metadata
	// (ProviderEvidenceDigestMetadataKey).
	SeedProviderEvidence(plan ExecutorPlanRef, sourceAttemptID string, evidence executor.ProviderEvidence)
	// SeedResource creates a resource bound to cluster's target with desired
	// accepted, and returns its name.
	SeedResource(cluster string, desired controller.DesiredRevision) string

	// Start and StartInsertFails are the executor-lane attempt start for the
	// device deviceID. StartInsertFails must make the attempt insert fail
	// AFTER the fence was written inside the same transaction, in the
	// backend's own way, and report Failed.
	Start(plan ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) ExecResp
	StartInsertFails(plan ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) ExecResp
	Heartbeat(attempt *fleet.RunnerAttempt, sequence int64) ExecResp
	// CompleteAttempt records phase evidence and advances every remaining
	// phase to complete, releasing the fence in the terminal transaction.
	CompleteAttempt(attempt *fleet.RunnerAttempt) ExecResp
	Get(attempt *fleet.RunnerAttempt) *fleet.RunnerAttempt
	// ExpireAttempt forces attempt's heartbeat lease into the past.
	ExpireAttempt(attempt *fleet.RunnerAttempt)

	// GitHubStart runs the whole existing GitHub-lane sequence on cluster:
	// dispatch acquire, run binding, attempt start (fence bind).
	GitHubStart(cluster string) GitHubStartResult

	Epoch() int64
	AdvanceEpoch(expected int64) error
	FenceFacts(targetID string) lifecycle.FenceFacts
	Occupancy(targetID string) (lifecycle.Occupancy, string)
	// ReconcileResource runs one status derivation with the real derive
	// function.
	ReconcileResource(name string) *controller.Resource
}

// ExecutorStartRequest builds the executor-lane start body for plan by host.
func ExecutorStartRequest(plan ExecutorPlanRef, host ExecutorHost, resume bool) executor.AttemptStartRequest {
	return executor.AttemptStartRequest{
		SchemaVersion:           executor.AttemptSchemaVersion,
		RunnerAttemptID:         executor.RunnerAttemptID(host.DropletID, host.InvocationID, plan.ID),
		CommitSHA:               plan.CommitSHA,
		PlanSHA256:              plan.PlanSHA256,
		WorkflowURL:             executor.WorkflowURL(host.DropletID, host.InvocationID),
		Resume:                  resume,
		HeartbeatTimeoutSeconds: 120,
		Executor: executor.AttemptMetadata{
			Lane: executor.LaneMetadataValue, DropletID: host.DropletID, MachineID: host.MachineID, BootID: host.BootID,
			InvocationID: host.InvocationID, LoopRelease: "fleet-executor-v1", CommitSHA: plan.CommitSHA,
			WheelhouseSHA256: strings.Repeat("c", 64), TofuVersion: "1.8.0",
		},
	}
}

// ExecutorAttestation builds an attestation by attester of source's stop on a
// new boot (the strongest proof), observed now.
func ExecutorAttestation(source *fleet.RunnerAttempt, attester ExecutorHost) executor.StopAttestationRequest {
	return executor.StopAttestationRequest{
		SourceAttemptID: source.ID, DropletID: attester.DropletID, MachineID: attester.MachineID,
		BootID: attester.BootID, InvocationID: attester.InvocationID,
		PhaseUIDEmpty: true, PhaseProcessGroupReaped: true, SourceCgroupEmpty: true, ObservedAt: time.Now().UTC(),
	}
}

// RunFleetExecutorConformance runs the E2 executor-admission cases. Every case
// calls newHarness(t) for its own harness on a fresh, isolated backend with
// authority epoch 1 and an empty target registry; no case depends on another.
func RunFleetExecutorConformance(t *testing.T, newHarness func(t *testing.T) ExecutorHarness) {
	cases := []struct {
		name string
		run  func(*testing.T, ExecutorHarness)
	}{
		{"AcquireBindInsertAtomic", testExecutorAcquireBindInsertAtomic},
		{"LostResponseReplays", testExecutorLostResponseReplays},
		{"LanesShareFence", testExecutorLanesShareFence},
		{"ExpiryDoesNotRelease", testExecutorExpiryDoesNotRelease},
		{"RecoveryRequiresAttestationOrEvidence", testExecutorRecoveryRequiresAttestationOrEvidence},
		{"CloneDropletAttestationRejected", testExecutorCloneDropletAttestationRejected},
		{"ApprovalRequiredForExecutable", testExecutorApprovalRequiredForExecutable},
		{"ResumeApproveConsumedOnce", testExecutorResumeApproveConsumedOnce},
		{"RefusesUnregisteredTarget", testExecutorRefusesUnregisteredTarget},
		{"RefusesOlderConvergePlan", testExecutorRefusesOlderConvergePlan},
		{"NewestPlanOrder", testExecutorNewestPlanOrder},
		{"EpochAdvanceRefusesHeartbeat", testExecutorEpochAdvanceRefusesHeartbeat},
		{"LastAppliedFromConvergePlan", testExecutorLastAppliedFromConvergePlan},
		{"CIBranchUnchanged", testExecutorCIBranchUnchanged},
		{"DestructiveRefusalBlocksFreshStart", testExecutorDestructiveRefusalBlocksFreshStart},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) { c.run(t, newHarness(t)) })
	}
}

func requireExecStarted(t *testing.T, resp ExecResp) *fleet.RunnerAttempt {
	t.Helper()
	if resp.Code != "" || resp.Failed || resp.Attempt == nil {
		t.Fatalf("start refused or failed: %+v", resp)
	}
	return resp.Attempt
}

func requireExecRefused(t *testing.T, resp ExecResp, code string) {
	t.Helper()
	if resp.Code != code || resp.Attempt != nil || resp.Failed {
		t.Fatalf("got code=%q attempt=%v failed=%v, want refusal %q", resp.Code, resp.Attempt, resp.Failed, code)
	}
}

// startOn registers a target for a fresh cluster and starts a config plan on it.
func startOn(t *testing.T, h ExecutorHarness, cluster string) (targetID string, plan ExecutorPlanRef, host ExecutorHost, attempt *fleet.RunnerAttempt) {
	t.Helper()
	targetID = h.RegisterTarget(cluster)
	plan = h.SeedConvergePlan(ExecutorPlanSpec{Cluster: cluster})
	host = NewExecutorHost()
	attempt = requireExecStarted(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)))
	return
}

func testExecutorAcquireBindInsertAtomic(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("atomic")
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "atomic"})
	before := h.FenceFacts(target)
	host := NewExecutorHost()
	attempt := requireExecStarted(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)))
	after := h.FenceFacts(target)
	if !after.Held || after.HolderPlanID != plan.ID || after.HolderNonceSHA256 != executor.HolderNonceSHA256(plan.ID) ||
		after.Generation != before.Generation+1 || after.AuthorityEpoch != h.Epoch() {
		t.Fatalf("one start must acquire and bind the fence: before=%+v after=%+v", before, after)
	}
	if attempt.RunnerAttemptID != executor.RunnerAttemptID(host.DropletID, host.InvocationID, plan.ID) ||
		attempt.WorkflowURL != executor.WorkflowURL(host.DropletID, host.InvocationID) ||
		attempt.Status != "running" || attempt.CurrentPhase != h.ExpectedInitialPhase() || attempt.RetryOf != "" {
		t.Fatalf("unexpected executor attempt: %+v", attempt)
	}
	if occupancy, reason := h.Occupancy(target); occupancy != lifecycle.OccupancyActive {
		t.Fatalf("a bound executor holder must read Active, got %s/%s", occupancy, reason)
	}

	// An insert that fails after the fence write rolls the fence back too.
	second := h.RegisterTarget("atomic-rollback")
	rollbackPlan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "atomic-rollback"})
	rollbackHost := NewExecutorHost()
	rollbackReq := ExecutorStartRequest(rollbackPlan, rollbackHost, false)
	secondBefore := h.FenceFacts(second)
	if resp := h.StartInsertFails(rollbackPlan, rollbackHost.DeviceID, rollbackReq); !resp.Failed || resp.Attempt != nil {
		t.Fatalf("the injected insert failure must surface: %+v", resp)
	}
	if got := h.FenceFacts(second); got.Held || got.Generation != secondBefore.Generation || got.Revision != secondBefore.Revision {
		t.Fatalf("a failed insert left fence state behind: before=%+v after=%+v", secondBefore, got)
	}
	requireExecStarted(t, h.Start(rollbackPlan, rollbackHost.DeviceID, rollbackReq))

	// Concurrent starts of different plans on one target: exactly one wins.
	h.RegisterTarget("atomic-race")
	const racers = 6
	results := make([]ExecResp, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		// Distinct resources on one target, so each plan is its resource's
		// newest and only the fence decides.
		racerPlan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "atomic-race", Resource: fmt.Sprintf("atomic-race-%d", i)})
		racerHost := NewExecutorHost()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.Start(racerPlan, racerHost.DeviceID, ExecutorStartRequest(racerPlan, racerHost, false))
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, r := range results {
		switch {
		case r.Attempt != nil && r.Code == "":
			winners++
		case r.Code == lifecycle.CodeFleetTargetExecutionOccupied && r.Attempt == nil:
		default:
			t.Fatalf("racer neither won nor was refused as occupied: %+v", r)
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one concurrent start may win the fence, got %d", winners)
	}
}

func testExecutorLostResponseReplays(t *testing.T, h ExecutorHarness) {
	target, plan, host, first := startOn(t, h, "replay")
	request := ExecutorStartRequest(plan, host, false)
	replay := h.Start(plan, host.DeviceID, request)
	if replay.Code != "" || replay.Attempt == nil || replay.Attempt.ID != first.ID || !replay.Replayed {
		t.Fatalf("a lost response must replay the same attempt: %+v", replay)
	}
	// T6: replay returns before any fence check, even once the fence is stale.
	if err := h.AdvanceEpoch(h.Epoch()); err != nil {
		t.Fatal(err)
	}
	fence := h.FenceFacts(target)
	if replay := h.Start(plan, host.DeviceID, request); replay.Attempt == nil || replay.Attempt.ID != first.ID {
		t.Fatalf("replay must not depend on the fence: %+v", replay)
	}
	if got := h.FenceFacts(target); got != fence {
		t.Fatalf("a replay must not write the fence: %+v then %+v", fence, got)
	}
	changed := request
	changed.Executor.LoopRelease = "fleet-executor-v2"
	requireExecRefused(t, h.Start(plan, host.DeviceID, changed), "fleet_runner_attempt_binding_conflict")
	requireExecRefused(t, h.Start(plan, "device-someone-else", request), "fleet_runner_attempt_binding_conflict")
}

func testExecutorLanesShareFence(t *testing.T, h ExecutorHarness) {
	// The GitHub lane holds: an executor start is occupied and writes nothing.
	githubTarget := h.RegisterTarget("shared-github")
	github := h.GitHubStart("shared-github")
	if github.Code != "" || github.Attempt == nil {
		t.Fatalf("GitHub lane could not start on a free target: %+v", github)
	}
	if fence := h.FenceFacts(githubTarget); fence.HolderNonceSHA256 != github.NonceSHA256 || fence.HolderNonceSHA256 == executor.HolderNonceSHA256(github.PlanID) {
		t.Fatalf("GitHub lane holds the fence under its own dispatch nonce: %+v", fence)
	}
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "shared-github"})
	host := NewExecutorHost()
	fenceBefore := h.FenceFacts(githubTarget)
	requireExecRefused(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)), lifecycle.CodeFleetTargetExecutionOccupied)
	if got := h.FenceFacts(githubTarget); got != fenceBefore {
		t.Fatalf("a refused executor start changed the GitHub holder's fence: %+v then %+v", fenceBefore, got)
	}

	// The executor lane holds: the GitHub lane's acquire is occupied.
	executorTarget, _, _, _ := startOn(t, h, "shared-executor")
	refused := h.GitHubStart("shared-executor")
	if refused.Code != lifecycle.CodeFleetTargetExecutionOccupied || refused.Attempt != nil {
		t.Fatalf("GitHub lane must lose to an executor holder: %+v", refused)
	}
	if !h.FenceFacts(executorTarget).Held {
		t.Fatal("the executor holder lost its fence")
	}
}

func testExecutorExpiryDoesNotRelease(t *testing.T, h ExecutorHarness) {
	target, _, _, attempt := startOn(t, h, "expiry")
	if resp := h.Heartbeat(attempt, 1); resp.Code != "" {
		t.Fatalf("heartbeat refused: %+v", resp)
	}
	h.ExpireAttempt(attempt)
	if got := h.Get(attempt); got == nil || got.Status == "running" {
		t.Fatalf("an expired lease must read abandoned, got %+v", got)
	}
	fence := h.FenceFacts(target)
	if !fence.Held || fence.HolderPlanID != attempt.PlanID {
		t.Fatalf("heartbeat expiry must never release the fence: %+v", fence)
	}
	if occupancy, _ := h.Occupancy(target); occupancy != lifecycle.OccupancyUncertain {
		t.Fatalf("an expired holder is Uncertain, never Free, got %s", occupancy)
	}
	other := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "expiry"})
	otherHost := NewExecutorHost()
	requireExecRefused(t, h.Start(other, otherHost.DeviceID, ExecutorStartRequest(other, otherHost, false)), lifecycle.CodeFleetTargetExecutionOccupied)
}

// recoverySetup starts a config plan, expires it and returns what recovery
// needs. The source host is the old invocation; next is the restarted loop.
func recoverySetup(t *testing.T, h ExecutorHarness, cluster string) (target string, plan ExecutorPlanRef, host, next ExecutorHost, source *fleet.RunnerAttempt) {
	t.Helper()
	target, plan, host, source = startOn(t, h, cluster)
	h.ExpireAttempt(source)
	next = host.NextInvocation()
	h.SeedApprove(plan, plan.CommitSHA, true, source.ID)
	return
}

func testExecutorRecoveryRequiresAttestationOrEvidence(t *testing.T, h ExecutorHarness) {
	target, plan, host, next, source := recoverySetup(t, h, "recovery")
	resume := ExecutorStartRequest(plan, next, true)
	generation := h.FenceFacts(target).Generation

	// Neither stop proof exists: refused, and the source stays as it was.
	requireExecRefused(t, h.Start(plan, next.DeviceID, resume), lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource)

	// Stale, same-invocation and wrong-invocation attestations are not proof.
	stale := ExecutorAttestation(source, next)
	stale.ObservedAt = time.Now().UTC().Add(-10 * time.Minute)
	h.SeedStopAttestation(plan, next.DeviceID, stale)
	requireExecRefused(t, h.Start(plan, next.DeviceID, resume), executor.CodeFleetExecutorStopAttestationInvalid)
	h.SeedStopAttestation(plan, host.DeviceID, ExecutorAttestation(source, host))
	requireExecRefused(t, h.Start(plan, next.DeviceID, resume), executor.CodeFleetExecutorStopAttestationInvalid)
	otherInvocation := next.NextInvocation()
	h.SeedStopAttestation(plan, next.DeviceID, ExecutorAttestation(source, otherInvocation))
	requireExecRefused(t, h.Start(plan, next.DeviceID, resume), executor.CodeFleetExecutorStopAttestationInvalid)

	// A same-boot attestation needs every positive fact.
	sameBoot := ExecutorAttestation(source, next)
	sameBoot.BootID = host.BootID
	sameBoot.SourceCgroupEmpty = false
	h.SeedStopAttestation(plan, next.DeviceID, sameBoot)
	requireExecRefused(t, h.Start(plan, next.DeviceID, resume), executor.CodeFleetExecutorStopAttestationInvalid)
	if got := h.Get(source); got.Status == "canceled" {
		t.Fatalf("a refused recovery must not touch the source: %+v", got)
	}

	// A valid attestation admits one recovery that keeps the fence.
	h.SeedStopAttestation(plan, next.DeviceID, ExecutorAttestation(source, next))
	recovery := requireExecStarted(t, h.Start(plan, next.DeviceID, resume))
	if recovery.RetryOf != source.ID || recovery.RootAttemptID != source.RootAttemptID || recovery.Attempt != source.Attempt+1 || recovery.CurrentPhase != source.CurrentPhase {
		t.Fatalf("recovery lineage is wrong: %+v from %+v", recovery, source)
	}
	if fence := h.FenceFacts(target); !fence.Held || fence.HolderPlanID != plan.ID || fence.Generation != generation {
		t.Fatalf("recovery keeps the same holder and generation: %+v", fence)
	}

	// A rebuilt host (new device, new droplet) cannot attest; admin
	// provider evidence names the old droplet and is the only way.
	_, evidencePlan, oldHost, evidenceSource := startOn(t, h, "recovery-evidence")
	h.ExpireAttempt(evidenceSource)
	h.SeedApprove(evidencePlan, evidencePlan.CommitSHA, true, evidenceSource.ID)
	rebuilt := NewExecutorHost()
	rebuiltResume := ExecutorStartRequest(evidencePlan, rebuilt, true)
	h.SeedStopAttestation(evidencePlan, rebuilt.DeviceID, ExecutorAttestation(evidenceSource, rebuilt))
	requireExecRefused(t, h.Start(evidencePlan, rebuilt.DeviceID, rebuiltResume), executor.CodeFleetExecutorStopAttestationInvalid)
	h.SeedProviderEvidence(evidencePlan, evidenceSource.ID, executor.ProviderEvidence{
		Provider: "digitalocean", DropletID: oldHost.DropletID + "0", Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC(),
	})
	requireExecRefused(t, h.Start(evidencePlan, rebuilt.DeviceID, rebuiltResume), executor.CodeFleetExecutorStopAttestationInvalid)
	// Evidence naming another source attempt never vouches for this one.
	h.SeedProviderEvidence(evidencePlan, uuid.NewString(), executor.ProviderEvidence{
		Provider: "digitalocean", DropletID: oldHost.DropletID, Status: executor.ProviderStatusNotFound, ObservedAt: time.Now().UTC(),
	})
	requireExecRefused(t, h.Start(evidencePlan, rebuilt.DeviceID, rebuiltResume), executor.CodeFleetExecutorStopAttestationInvalid)
	h.SeedProviderEvidence(evidencePlan, evidenceSource.ID, executor.ProviderEvidence{
		Provider: "digitalocean", DropletID: oldHost.DropletID, Status: executor.ProviderStatusNotFound, ObservedAt: time.Now().UTC(),
	})
	requireExecStarted(t, h.Start(evidencePlan, rebuilt.DeviceID, rebuiltResume))
}

func testExecutorCloneDropletAttestationRejected(t *testing.T, h ExecutorHarness) {
	_, plan, host, next, source := recoverySetup(t, h, "clone")
	// A snapshot clone keeps machine-id (and the device credential on disk)
	// but is a different droplet.
	clone := next
	clone.DropletID = host.DropletID + "7"
	attestation := ExecutorAttestation(source, clone)
	h.SeedStopAttestation(plan, clone.DeviceID, attestation)
	requireExecRefused(t, h.Start(plan, clone.DeviceID, ExecutorStartRequest(plan, clone, true)), executor.CodeFleetExecutorStopAttestationInvalid)

	// The clone's own start on its attestation's identity is no better than
	// the original host recovering with the clone's attestation.
	requireExecRefused(t, h.Start(plan, next.DeviceID, ExecutorStartRequest(plan, next, true)), executor.CodeFleetExecutorStopAttestationInvalid)
	// A clone on another machine id is rejected as well.
	otherMachine := next
	otherMachine.MachineID = strings.Repeat("e", 32)
	h.SeedStopAttestation(plan, next.DeviceID, ExecutorAttestation(source, otherMachine))
	requireExecRefused(t, h.Start(plan, next.DeviceID, ExecutorStartRequest(plan, next, true)), executor.CodeFleetExecutorStopAttestationInvalid)
}

func testExecutorApprovalRequiredForExecutable(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("approval")
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "approval", PathClass: executor.PathClassExecutable, RequiresApproval: true})
	host := NewExecutorHost()
	request := ExecutorStartRequest(plan, host, false)
	fence := h.FenceFacts(target)
	requireExecRefused(t, h.Start(plan, host.DeviceID, request), executor.CodeFleetConvergeApprovalRequired)
	// A resume approve, or an approve for another commit, is not the approval.
	h.SeedApprove(plan, plan.CommitSHA, true, uuid.NewString())
	h.SeedApprove(plan, strings.Repeat("9", 40), false, "")
	requireExecRefused(t, h.Start(plan, host.DeviceID, request), executor.CodeFleetConvergeApprovalRequired)
	if got := h.FenceFacts(target); got != fence {
		t.Fatalf("a refused start must leave the fence untouched: %+v then %+v", fence, got)
	}
	h.SeedApprove(plan, plan.CommitSHA, false, "")
	requireExecStarted(t, h.Start(plan, host.DeviceID, request))

	// A config plan needs no approve.
	h.RegisterTarget("approval-config")
	config := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "approval-config"})
	configHost := NewExecutorHost()
	requireExecStarted(t, h.Start(config, configHost.DeviceID, ExecutorStartRequest(config, configHost, false)))
}

func testExecutorResumeApproveConsumedOnce(t *testing.T, h ExecutorHarness) {
	_, plan, host, next, source := recoverySetup(t, h, "consumed")
	h.SeedStopAttestation(plan, next.DeviceID, ExecutorAttestation(source, next))
	first := requireExecStarted(t, h.Start(plan, next.DeviceID, ExecutorStartRequest(plan, next, true)))
	h.ExpireAttempt(first)

	// The same approve cannot authorize a second recovery, even with fresh proof.
	again := next.NextInvocation()
	h.SeedStopAttestation(plan, again.DeviceID, ExecutorAttestation(first, again))
	requireExecRefused(t, h.Start(plan, again.DeviceID, ExecutorStartRequest(plan, again, true)), executor.CodeFleetConvergeApprovalConsumed)
	// A new approve naming the new latest attempt authorizes exactly that one.
	h.SeedApprove(plan, plan.CommitSHA, true, first.ID)
	second := requireExecStarted(t, h.Start(plan, again.DeviceID, ExecutorStartRequest(plan, again, true)))
	if second.RetryOf != first.ID {
		t.Fatalf("second recovery lineage: %+v", second)
	}

	// Two loops racing on one approve: one recovery, never two.
	_, racePlan, raceHost, raceSource := startOn(t, h, "consumed-race")
	h.ExpireAttempt(raceSource)
	h.SeedApprove(racePlan, racePlan.CommitSHA, true, raceSource.ID)
	const racers = 4
	results := make([]ExecResp, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		racer := raceHost.NextInvocation()
		h.SeedStopAttestation(racePlan, racer.DeviceID, ExecutorAttestation(raceSource, racer))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.Start(racePlan, racer.DeviceID, ExecutorStartRequest(racePlan, racer, true))
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, r := range results {
		switch {
		case r.Attempt != nil && r.Code == "":
			winners++
		case r.Code == executor.CodeFleetConvergeApprovalConsumed && r.Attempt == nil:
		default:
			t.Fatalf("racing recovery neither won nor was refused as consumed: %+v", r)
		}
	}
	if winners != 1 {
		t.Fatalf("one resume approve authorizes exactly one recovery, got %d", winners)
	}
	_ = host
}

func testExecutorRefusesUnregisteredTarget(t *testing.T, h ExecutorHarness) {
	// Empty registry: the executor lane has no legacy unfenced mode.
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "nowhere"})
	host := NewExecutorHost()
	requireExecRefused(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)), lifecycle.CodeFleetTargetUnregistered)
	// Non-empty registry, unknown cluster.
	other := h.RegisterTarget("somewhere-else")
	before := h.FenceFacts(other)
	requireExecRefused(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)), lifecycle.CodeFleetTargetUnregistered)
	if got := h.FenceFacts(other); got != before {
		t.Fatalf("an unregistered start touched another target's fence: %+v then %+v", before, got)
	}
}

func testExecutorRefusesOlderConvergePlan(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("newest")
	older := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "newest", StartedAt: time.Now().UTC().Add(-20 * time.Minute)})
	newer := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "newest"})
	host := NewExecutorHost()
	fence := h.FenceFacts(target)
	requireExecRefused(t, h.Start(older, host.DeviceID, ExecutorStartRequest(older, host, false)), executor.CodeFleetExecutorPlanNotNewest)
	if got := h.FenceFacts(target); got != fence {
		t.Fatalf("a refused older plan touched the fence: %+v then %+v", fence, got)
	}
	requireExecStarted(t, h.Start(newer, host.DeviceID, ExecutorStartRequest(newer, host, false)))
}

// testExecutorNewestPlanOrder pins the one "newest converge plan" order both
// backends implement: started_at descending, then plan ID descending. Two
// plans recorded at the same instant are ordered by ID alone.
func testExecutorNewestPlanOrder(t *testing.T, h ExecutorHarness) {
	h.RegisterTarget("tie")
	at := time.Now().UTC().Add(-15 * time.Minute).Truncate(time.Microsecond)
	newer := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "tie", StartedAt: at})
	older := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "tie", StartedAt: at})
	if older.ID > newer.ID {
		newer, older = older, newer
	}
	host := NewExecutorHost()
	requireExecRefused(t, h.Start(older, host.DeviceID, ExecutorStartRequest(older, host, false)), executor.CodeFleetExecutorPlanNotNewest)
	requireExecStarted(t, h.Start(newer, host.DeviceID, ExecutorStartRequest(newer, host, false)))
}

func testExecutorEpochAdvanceRefusesHeartbeat(t *testing.T, h ExecutorHarness) {
	target, _, _, attempt := startOn(t, h, "epoch")
	if resp := h.Heartbeat(attempt, 1); resp.Code != "" {
		t.Fatalf("heartbeat refused before the epoch advanced: %+v", resp)
	}
	if err := h.AdvanceEpoch(h.Epoch()); err != nil {
		t.Fatal(err)
	}
	if resp := h.Heartbeat(attempt, 2); resp.Code != lifecycle.CodeFleetTargetAuthoritySuperseded {
		t.Fatalf("an epoch advance must refuse heartbeats, got %+v", resp)
	}
	if fence := h.FenceFacts(target); !fence.Held || fence.HolderPlanID != attempt.PlanID {
		t.Fatalf("an epoch advance never releases the fence: %+v", fence)
	}
	if occupancy, reason := h.Occupancy(target); occupancy != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonAuthoritySuperseded {
		t.Fatalf("a superseded holder is Uncertain/AuthoritySuperseded, got %s/%s", occupancy, reason)
	}
}

func testExecutorLastAppliedFromConvergePlan(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("applied")
	// The resource's desired history never held the plan's commit: derive's
	// own history lookup cannot resolve it, only the converge plan can.
	desired := controller.DesiredRevision{
		Generation: 7, PlanID: "operator", CommitSHA: strings.Repeat("d", 40), Verification: controller.VerificationOperatorDeclared,
		AcceptedBy: "operator", AcceptedAt: time.Now().UTC(),
	}
	name := h.SeedResource("applied", desired)
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "applied", Generation: 4})
	host := NewExecutorHost()
	attempt := requireExecStarted(t, h.Start(plan, host.DeviceID, ExecutorStartRequest(plan, host, false)))
	if running := h.ReconcileResource(name); running.Status.LastApplied != nil {
		t.Fatalf("nothing is applied while the executor attempt runs: %+v", running.Status)
	}
	done := h.CompleteAttempt(attempt)
	if done.Code != "" || done.Attempt == nil || done.Attempt.Status != "succeeded" {
		t.Fatalf("attempt did not complete: %+v", done)
	}
	if fence := h.FenceFacts(target); fence.Held || fence.LastRelease == nil || fence.LastRelease.Reason != "succeeded" {
		t.Fatalf("completion releases the fence with a succeeded release: %+v", fence)
	}
	applied := h.ReconcileResource(name).Status
	if applied.LastApplied == nil || applied.LastApplied.CommitSHA != plan.CommitSHA || applied.LastApplied.Generation != 4 ||
		applied.LastApplied.PlanID != plan.ID || applied.LastApplied.Reason != "" || applied.LastAppliedGeneration != 4 {
		t.Fatalf("last applied must come from the converge plan (commit %s generation 4): %+v / %d", plan.CommitSHA, applied.LastApplied, applied.LastAppliedGeneration)
	}
}

func testExecutorCIBranchUnchanged(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("ci")
	github := h.GitHubStart("ci")
	if github.Code != "" || github.Attempt == nil {
		t.Fatalf("GitHub lane start failed: %+v", github)
	}
	attempt := github.Attempt
	if !strings.HasPrefix(attempt.RunnerAttemptID, "github-actions:") || !strings.HasPrefix(attempt.WorkflowURL, "https://github.com/") {
		t.Fatalf("GitHub-lane identity changed: %+v", attempt)
	}
	if _, _, _, isExecutor := executor.ParseRunnerAttemptID(attempt.RunnerAttemptID); isExecutor {
		t.Fatalf("a GitHub attempt parses as an executor attempt: %+v", attempt)
	}
	fence := h.FenceFacts(target)
	if !fence.Held || fence.HolderPlanID != github.PlanID || fence.HolderNonceSHA256 != github.NonceSHA256 {
		t.Fatalf("GitHub-lane holder facts changed: %+v", fence)
	}
	if resp := h.Heartbeat(attempt, 1); resp.Code != "" {
		t.Fatalf("GitHub-lane heartbeat changed: %+v", resp)
	}
	if done := h.CompleteAttempt(attempt); done.Code != "" || done.Attempt == nil || done.Attempt.Status != "succeeded" {
		t.Fatalf("GitHub-lane completion changed: %+v", done)
	}
	if fence := h.FenceFacts(target); fence.Held || fence.LastRelease == nil || fence.LastRelease.Reason != "succeeded" {
		t.Fatalf("GitHub-lane release changed: %+v", fence)
	}

	// The executor lane admits converge plans only: it can never start a
	// GitHub-lane plan, whose payload has no converge object.
	plain := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "ci", NotConverge: true})
	host := NewExecutorHost()
	requireExecRefused(t, h.Start(plain, host.DeviceID, ExecutorStartRequest(plain, host, false)), executor.CodeFleetPlanNotConverge)
}

// testExecutorDestructiveRefusalBlocksFreshStart: a fleet.converge.refusal with
// code destructive_requires_github_lane for the plan's commit refuses a fresh
// start and leaves the fence untouched. A refusal of another commit or code
// does not, and a recovery of an already-started plan is exempt.
func testExecutorDestructiveRefusalBlocksFreshStart(t *testing.T, h ExecutorHarness) {
	target := h.RegisterTarget("refused")
	plan := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "refused"})
	host := NewExecutorHost()
	request := ExecutorStartRequest(plan, host, false)
	fence := h.FenceFacts(target)

	h.SeedRefusal(plan, executor.RefusalDestructive, strings.Repeat("9", 40))
	h.SeedRefusal(plan, executor.RefusalStateLockForeign, plan.CommitSHA)
	h.SeedRefusal(plan, executor.RefusalDestructive, plan.CommitSHA)
	requireExecRefused(t, h.Start(plan, host.DeviceID, request), executor.CodeFleetConvergeRefusalRejected)
	if got := h.FenceFacts(target); got != fence {
		t.Fatalf("a refused start must leave the fence untouched: %+v then %+v", fence, got)
	}

	// Only the matching refusal blocks: the other plan has the other two.
	h.RegisterTarget("refused-others")
	other := h.SeedConvergePlan(ExecutorPlanSpec{Cluster: "refused-others"})
	otherHost := NewExecutorHost()
	h.SeedRefusal(other, executor.RefusalDestructive, strings.Repeat("9", 40))
	h.SeedRefusal(other, executor.RefusalStateLockForeign, other.CommitSHA)
	started := requireExecStarted(t, h.Start(other, otherHost.DeviceID, ExecutorStartRequest(other, otherHost, false)))

	// Recovery is exempt: the plan holds the fence, a destructive remainder is
	// handled by abandon.
	h.SeedRefusal(other, executor.RefusalDestructive, other.CommitSHA)
	h.ExpireAttempt(started)
	next := otherHost.NextInvocation()
	h.SeedApprove(other, other.CommitSHA, true, started.ID)
	h.SeedStopAttestation(other, next.DeviceID, ExecutorAttestation(started, next))
	requireExecStarted(t, h.Start(other, next.DeviceID, ExecutorStartRequest(other, next, true)))
}
