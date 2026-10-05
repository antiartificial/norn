package etcdstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

type opsEtcdFixture struct {
	t      *testing.T
	h      *etcdExecutorHarness
	auth   *AuthStore
	target string
	plan   fleettest.ExecutorPlanRef
	host   fleettest.ExecutorHost
	source string
}

func newOpsEtcdFixture(t *testing.T) *opsEtcdFixture {
	t.Helper()
	h := newEtcdExecutorHarness(t)
	f := &opsEtcdFixture{t: t, h: h, auth: NewAuthStore(h.s.kv, h.s.prefix)}
	f.target = h.RegisterTarget("ops")
	f.plan = h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "ops"})
	f.host = fleettest.NewExecutorHost()
	started := h.Start(f.plan, f.host.DeviceID, fleettest.ExecutorStartRequest(f.plan, f.host, false))
	if started.Attempt == nil {
		t.Fatalf("start: %+v", started)
	}
	f.source = started.Attempt.ID
	return f
}

func (f *opsEtcdFixture) device(revoked bool) {
	f.t.Helper()
	ctx := context.Background()
	err := f.auth.CreateAccessDevice(ctx, &store.AccessDevice{ID: f.host.DeviceID, Name: "executor", Platform: executor.Platform, CreatedAt: time.Now()})
	if err != nil {
		f.t.Fatal(err)
	}
	if revoked {
		if _, err := f.auth.RevokeAccessDevice(ctx, f.host.DeviceID); err != nil {
			f.t.Fatal(err)
		}
	}
}

func opsEtcdCode(err error) string {
	var refusal *executor.Error
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	var fence *lifecycle.FenceError
	if errors.As(err, &fence) {
		return fence.Code
	}
	return ""
}

func (f *opsEtcdFixture) abandon(key string, evidence *executor.ProviderEvidence) (store.AcceptedOperation, error) {
	return fleetTargetTestHarness{store: f.h.s}.acceptMutation(context.Background(), key, store.FleetTargetMutationAdmission{
		Kind: store.FleetTargetMutationAbandonPlan, PlanID: f.plan.ID,
		Release: &store.FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon), ProviderEvidence: evidence},
	}, nil)
}

func (f *opsEtcdFixture) evidence() *executor.ProviderEvidence {
	return &executor.ProviderEvidence{Provider: "digitalocean", DropletID: f.host.DropletID, Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC().Add(-time.Minute)}
}

func TestFleetExecutorOpsReadersEtcd(t *testing.T) {
	f := newOpsEtcdFixture(t)
	ctx := context.Background()
	f.device(false)
	attempts, err := f.h.s.FleetExecutorAttempts(ctx, f.plan.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %v %v", attempts, err)
	}
	fact := attempts[0]
	if !fact.Executor || fact.PrincipalSubject != executor.PrincipalSubject(f.host.DeviceID) || fact.Metadata.DropletID != f.host.DropletID || !fact.Live(time.Now()) {
		t.Fatalf("fact: %+v", fact)
	}
	byID, err := f.h.s.FleetExecutorAttemptByID(ctx, f.source)
	if err != nil || byID == nil || byID.PlanID != f.plan.ID || !byID.Executor {
		t.Fatalf("by id: %+v %v", byID, err)
	}
	if missing, err := f.h.s.FleetExecutorAttemptByID(ctx, uuid.NewString()); err != nil || missing != nil {
		t.Fatalf("unknown attempt: %+v %v", missing, err)
	}
	if device, err := f.h.s.FleetExecutorDevice(ctx, f.host.DeviceID); err != nil || !device.Exists || device.Revoked || device.Platform != executor.Platform {
		t.Fatalf("device: %+v %v", device, err)
	}
	if device, err := f.h.s.FleetExecutorDevice(ctx, "unknown"); err != nil || device.Exists {
		t.Fatalf("unknown device: %+v %v", device, err)
	}
	if _, err := f.auth.RevokeAccessDevice(ctx, f.host.DeviceID); err != nil {
		t.Fatal(err)
	}
	if device, _ := f.h.s.FleetExecutorDevice(ctx, f.host.DeviceID); !device.Revoked {
		t.Fatalf("revoked device: %+v", device)
	}
	if ok, err := f.h.s.FleetExecutorPlanIsConverge(ctx, f.plan.ID); err != nil || !ok {
		t.Fatalf("converge plan: %v %v", ok, err)
	}
	if ok, _ := f.h.s.FleetExecutorPlanIsConverge(ctx, uuid.NewString()); ok {
		t.Fatal("unknown plan is not a converge plan")
	}
	// The grant decision runs over etcd facts exactly as over PG facts.
	request := executor.CredentialGrantRequest{AttemptID: fact.ID, Phase: fact.CurrentPhase, Types: []string{executor.CredNornAttempt}}
	profile := executor.GrantProfileV3
	if _, _, err := store.DecideFleetExecutorGrant(request, profile, attempts, false, fact.PrincipalSubject, time.Now()); opsEtcdCode(err) != executor.CodeFleetCredentialTypeNotAllowed && err != nil {
		t.Fatalf("grant decision over etcd facts: %v", err)
	}
	other := executor.CredentialGrantRequest{AttemptID: fact.ID, Phase: "readiness_verified", Types: []string{executor.CredNornAttempt}}
	if _, _, err := store.DecideFleetExecutorGrant(other, profile, attempts, false, fact.PrincipalSubject, time.Now()); opsEtcdCode(err) != executor.CodeFleetCredentialGrantPhaseMismatch {
		t.Fatalf("phase mismatch over etcd facts: %v", err)
	}
	source, err := f.h.s.GetFleetRunnerAttempt(ctx, f.plan.ID, f.source)
	if err != nil {
		t.Fatal(err)
	}
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(source, f.host))
	rows, err := f.h.s.FleetExecutorLedgerRows(ctx, executor.KindStopAttestation, f.plan.ID)
	if err != nil || len(rows) != 1 || rows[0].Metadata[store.FleetExecutorAttesterMetadataKey] != executor.PrincipalSubject(f.host.DeviceID) {
		t.Fatalf("ledger rows: %+v %v", rows, err)
	}
}

func TestFleetExecutorAbandonExecutorLaneNeedsEvidenceEtcd(t *testing.T) {
	f := newOpsEtcdFixture(t)
	ctx := context.Background()
	started, err := f.h.s.GetFleetRunnerAttempt(ctx, f.plan.ID, f.source)
	if err != nil {
		t.Fatal(err)
	}
	// A live executor attempt is never released.
	if _, err := f.abandon("live", f.evidence()); err == nil {
		t.Fatal("a live attempt must refuse")
	}
	f.h.ExpireAttempt(started)
	if _, err := f.abandon("none", nil); opsEtcdCode(err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("without evidence or attestation: %v", err)
	}
	targets := fleetTargetTestHarness{store: f.h.s}
	if _, err := targets.acceptMutation(ctx, "listing", store.FleetTargetMutationAdmission{
		Kind: store.FleetTargetMutationAbandonPlan, PlanID: f.plan.ID, Release: &store.FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon)},
	}, &store.FleetTargetReleaseEvidence{ListingSnapshotSHA256: etcdRandomHex(64)}); opsEtcdCode(err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("a GitHub listing snapshot must not release an executor holder: %v", err)
	}
	f.device(false)
	if _, err := f.abandon("active", f.evidence()); opsEtcdCode(err) != executor.CodeFleetExecutorDeviceActive {
		t.Fatalf("evidence while the device is active: %v", err)
	}
	wrong := f.evidence()
	wrong.DropletID = fleettest.NewExecutorHost().DropletID
	if _, err := f.auth.RevokeAccessDevice(ctx, f.host.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.abandon("wrong", wrong); opsEtcdCode(err) != executor.CodeFleetProviderEvidenceInvalid {
		t.Fatalf("another droplet: %v", err)
	}
	good := f.evidence()
	accepted, err := f.abandon("good", good)
	if err != nil {
		t.Fatalf("valid evidence on a revoked device after the H5 age: %v", err)
	}
	if digest, _ := accepted.Operation.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string); digest != good.Digest() {
		t.Fatalf("digest: %+v", accepted.Operation.Metadata)
	}
	fence := f.h.FenceFacts(f.target)
	if fence.Held {
		t.Fatalf("the fence must be free: %+v", fence)
	}
	if abandoned, _ := f.h.s.CheckFleetTargetPlanAbandoned(ctx, f.plan.ID); !abandoned {
		t.Fatal("the plan must be permanently abandoned")
	}
}

func TestFleetExecutorTerminalReleaseByAttestationEtcd(t *testing.T) {
	f := newOpsEtcdFixture(t)
	ctx := context.Background()
	started, err := f.h.s.GetFleetRunnerAttempt(ctx, f.plan.ID, f.source)
	if err != nil {
		t.Fatal(err)
	}
	f.h.ExpireAttempt(started)
	release := func(key string) error {
		_, err := fleetTargetTestHarness{store: f.h.s}.acceptMutation(ctx, key, store.FleetTargetMutationAdmission{
			Kind: store.FleetTargetMutationRelease, TargetID: f.target,
			Release: &store.FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeTerminal), ExpectedGeneration: f.h.FenceFacts(f.target).Generation},
		}, nil)
		return err
	}
	if err := release("none"); opsEtcdCode(err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("no attestation: %v", err)
	}
	clone := f.host.NextInvocation()
	clone.BootID, clone.DropletID = uuid.NewString(), fleettest.NewExecutorHost().DropletID
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(started, clone))
	if err := release("clone"); opsEtcdCode(err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("clone attestation: %v", err)
	}
	restarted := f.host.NextInvocation()
	restarted.BootID = uuid.NewString()
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(started, restarted))
	if err := release("good"); err != nil {
		t.Fatalf("valid attestation: %v", err)
	}
	if f.h.FenceFacts(f.target).Held {
		t.Fatal("the fence must be free")
	}
}
