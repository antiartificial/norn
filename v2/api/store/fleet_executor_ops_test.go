package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// --- pure decisions (no database) ---

func opsAttempt(host fleettest.ExecutorHost, phase string) FleetExecutorAttemptFact {
	return FleetExecutorAttemptFact{
		ID: uuid.NewString(), PlanID: uuid.NewString(), Attempt: 1, Status: "running", CurrentPhase: phase,
		PrincipalSubject: executor.PrincipalSubject(host.DeviceID), Executor: true,
		Metadata:           executor.AttemptMetadata{Lane: executor.LaneMetadataValue, DropletID: host.DropletID, MachineID: host.MachineID, BootID: host.BootID, InvocationID: host.InvocationID},
		HeartbeatExpiresAt: time.Now().Add(time.Minute),
	}
}

func executorCode(t *testing.T, err error) string {
	t.Helper()
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

// TestFleetExecutorGrantTypeTable pins the admission table for both backends:
// for every phase of a profile exactly its listed types are admitted and every
// other credential type is refused; a phase outside the table is invalid.
func TestFleetExecutorGrantTypeTable(t *testing.T) {
	all := []string{executor.CredDORead, executor.CredDOWrite, executor.CredSpacesRW, executor.CredSpacesRead, executor.CredSSHNode,
		executor.CredBootstrapDir, executor.CredReleaseGitHub, executor.CredNornAttempt}
	host := fleettest.NewExecutorHost()
	for profile, table := range executor.GrantTypes {
		for phase, allowed := range table {
			attempt := opsAttempt(host, phase)
			if phase == executor.GrantPhaseRecover {
				attempt.RetryOf = uuid.NewString()
			}
			facts := []FleetExecutorAttemptFact{attempt}
			if phase == executor.GrantPhasePreflight {
				attempt.CurrentPhase = "provider_applying"
				facts = []FleetExecutorAttemptFact{attempt}
			}
			in := func(t string) bool {
				for _, a := range allowed {
					if a == t {
						return true
					}
				}
				return false
			}
			for _, credential := range all {
				request := executor.CredentialGrantRequest{Phase: phase, Types: []string{credential}}
				if phase != executor.GrantPhasePlan {
					request.AttemptID = attempt.ID
				}
				_, _, err := DecideFleetExecutorGrant(request, profile, facts, true, attempt.PrincipalSubject, time.Now())
				switch {
				case in(credential) && err != nil:
					t.Errorf("%s/%s/%s refused: %v", profile, phase, credential, err)
				case !in(credential) && executorCode(t, err) != executor.CodeFleetCredentialTypeNotAllowed:
					t.Errorf("%s/%s/%s: want %s, got %v", profile, phase, credential, executor.CodeFleetCredentialTypeNotAllowed, err)
				}
			}
			// The whole allowed set at once is admitted.
			request := executor.CredentialGrantRequest{Phase: phase, Types: append([]string(nil), allowed...)}
			if phase != executor.GrantPhasePlan {
				request.AttemptID = attempt.ID
			}
			if _, _, err := DecideFleetExecutorGrant(request, profile, facts, true, attempt.PrincipalSubject, time.Now()); err != nil {
				t.Errorf("%s/%s full set refused: %v", profile, phase, err)
			}
		}
	}
	// Phases that exist on only one backend are invalid on the other.
	_, _, err := DecideFleetExecutorGrant(executor.CredentialGrantRequest{Phase: "provider_applying", AttemptID: uuid.NewString(), Types: []string{executor.CredNornAttempt}},
		executor.GrantProfilePostgresLegacyNoDrain, nil, false, "x", time.Now())
	if executorCode(t, err) != executor.CodeInvalidFleetExecutorRequest {
		t.Fatalf("provider_applying on the PG list: %v", err)
	}
}

func TestFleetExecutorGrantRequiresLiveAttemptAndCurrentPhase(t *testing.T) {
	host := fleettest.NewExecutorHost()
	now := time.Now()
	profile := executor.GrantProfilePostgresLegacyNoDrain
	phase := "inventory_generated"
	good := opsAttempt(host, phase)
	request := func(a FleetExecutorAttemptFact, p string) executor.CredentialGrantRequest {
		return executor.CredentialGrantRequest{AttemptID: a.ID, Phase: p, Types: []string{executor.CredNornAttempt}}
	}
	if _, attempt, err := DecideFleetExecutorGrant(request(good, phase), profile, []FleetExecutorAttemptFact{good}, false, good.PrincipalSubject, now); err != nil || attempt == nil || attempt.ID != good.ID {
		t.Fatalf("live owned attempt in its current phase: %v", err)
	}
	expired := good
	expired.HeartbeatExpiresAt = now.Add(-time.Second)
	terminal := good
	terminal.Status = "succeeded"
	foreign := good
	foreign.PrincipalSubject = executor.PrincipalSubject("another-device")
	githubLane := good
	githubLane.Executor = false
	for name, tc := range map[string]struct {
		attempts []FleetExecutorAttemptFact
		request  executor.CredentialGrantRequest
		want     string
	}{
		"unknown attempt":       {nil, request(good, phase), executor.CodeFleetCredentialGrantAttemptRequired},
		"expired lease":         {[]FleetExecutorAttemptFact{expired}, request(expired, phase), executor.CodeFleetCredentialGrantAttemptRequired},
		"terminal attempt":      {[]FleetExecutorAttemptFact{terminal}, request(terminal, phase), executor.CodeFleetCredentialGrantAttemptRequired},
		"other device":          {[]FleetExecutorAttemptFact{foreign}, request(foreign, phase), executor.CodeFleetCredentialGrantAttemptRequired},
		"github lane attempt":   {[]FleetExecutorAttemptFact{githubLane}, request(githubLane, phase), executor.CodeFleetCredentialGrantAttemptRequired},
		"earlier phase":         {[]FleetExecutorAttemptFact{good}, request(good, "nodes_configured"), executor.CodeFleetCredentialGrantPhaseMismatch},
		"missing attempt id":    {[]FleetExecutorAttemptFact{good}, executor.CredentialGrantRequest{Phase: phase, Types: []string{executor.CredNornAttempt}}, executor.CodeFleetCredentialGrantAttemptRequired},
		"plan phase w/ attempt": {nil, executor.CredentialGrantRequest{AttemptID: good.ID, Phase: executor.GrantPhasePlan, Types: []string{executor.CredDORead}}, executor.CodeInvalidFleetExecutorRequest},
		"unknown phase":         {[]FleetExecutorAttemptFact{good}, request(good, "old_nodes_drained"), executor.CodeInvalidFleetExecutorRequest},
	} {
		if _, _, err := DecideFleetExecutorGrant(tc.request, profile, tc.attempts, false, good.PrincipalSubject, now); executorCode(t, err) != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
	}
	// plan needs a converge plan.
	if _, _, err := DecideFleetExecutorGrant(executor.CredentialGrantRequest{Phase: executor.GrantPhasePlan, Types: []string{executor.CredDORead}}, profile, nil, false, "x", now); executorCode(t, err) != executor.CodeFleetPlanNotConverge {
		t.Fatalf("plan grant for a non-converge plan: %v", err)
	}
	// recover: latest, live, owned, with retryOf.
	recovery := opsAttempt(host, "infrastructure_applied")
	recovery.Attempt, recovery.RetryOf = 2, good.ID
	older := good
	older.Attempt = 1
	recoverRequest := executor.CredentialGrantRequest{AttemptID: recovery.ID, Phase: executor.GrantPhaseRecover, Types: []string{executor.CredSpacesRead}}
	if _, _, err := DecideFleetExecutorGrant(recoverRequest, profile, []FleetExecutorAttemptFact{older, recovery}, false, recovery.PrincipalSubject, now); err != nil {
		t.Fatalf("recover grant on the latest recovery attempt: %v", err)
	}
	notRecovery := recovery
	notRecovery.RetryOf = ""
	if _, _, err := DecideFleetExecutorGrant(recoverRequest, profile, []FleetExecutorAttemptFact{older, notRecovery}, false, recovery.PrincipalSubject, now); executorCode(t, err) != executor.CodeFleetCredentialGrantAttemptRequired {
		t.Fatalf("recover grant on a non-recovery attempt: %v", err)
	}
	notLatest := recovery
	notLatest.Attempt = 0
	if _, _, err := DecideFleetExecutorGrant(recoverRequest, profile, []FleetExecutorAttemptFact{older, notLatest}, false, recovery.PrincipalSubject, now); executorCode(t, err) != executor.CodeFleetCredentialGrantAttemptRequired {
		t.Fatalf("recover grant on a superseded attempt: %v", err)
	}
}

func TestFleetExecutorPreflightGrantRequiresOwnedInitialAttempt(t *testing.T) {
	host := fleettest.NewExecutorHost()
	now := time.Now()
	live := opsAttempt(host, "provider_applying")
	request := executor.CredentialGrantRequest{
		AttemptID: live.ID, Phase: executor.GrantPhasePreflight,
		Types: []string{executor.CredBootstrapDir, executor.CredSSHNode},
	}
	profile := executor.GrantProfileV3
	if _, attempt, err := DecideFleetExecutorGrant(request, profile, []FleetExecutorAttemptFact{live}, false, live.PrincipalSubject, now); err != nil || attempt == nil || attempt.ID != live.ID {
		t.Fatalf("preflight on the owned initial attempt: attempt=%v, err=%v", attempt, err)
	}
	terminal := live
	terminal.Status = "succeeded"
	githubLane := live
	githubLane.Executor = false
	advanced := live
	advanced.CurrentPhase = "infrastructure_applied"
	recovery := live
	recovery.RetryOf = uuid.NewString()
	for name, attempts := range map[string][]FleetExecutorAttemptFact{
		"unknown":           nil,
		"terminal executor": {terminal},
		"live GitHub lane":  {githubLane},
		"advanced":          {advanced},
	} {
		if _, _, err := DecideFleetExecutorGrant(request, profile, attempts, false, live.PrincipalSubject, now); err == nil {
			t.Errorf("%s: want preflight refusal, got %v", name, err)
		}
	}
	if _, attempt, err := DecideFleetExecutorGrant(request, profile, []FleetExecutorAttemptFact{recovery}, false, live.PrincipalSubject, now); err != nil || attempt == nil || attempt.ID != recovery.ID {
		t.Fatalf("recovery at provider_applying also requires private-input preflight: %v", err)
	}
}

func TestFleetExecutorAttestationSameDropletOnly(t *testing.T) {
	old := fleettest.NewExecutorHost()
	source := opsAttempt(old, "inventory_generated")
	restarted := old.NextInvocation()
	restarted.BootID = uuid.NewString()
	attest := func(host fleettest.ExecutorHost) executor.StopAttestationRequest {
		return executor.StopAttestationRequest{
			SourceAttemptID: source.ID, DropletID: host.DropletID, MachineID: host.MachineID, BootID: host.BootID, InvocationID: host.InvocationID,
			PhaseUIDEmpty: true, PhaseProcessGroupReaped: true, SourceCgroupEmpty: true, ObservedAt: time.Now().UTC(),
		}
	}
	now := time.Now()
	if err := DecideFleetExecutorStopAttestation(attest(restarted), &source, source.PrincipalSubject, now); err != nil {
		t.Fatalf("same droplet, device and machine on a new boot: %v", err)
	}
	clone := restarted
	clone.DropletID = fleettest.NewExecutorHost().DropletID
	other := fleettest.NewExecutorHost()
	for name, tc := range map[string]struct {
		request  executor.StopAttestationRequest
		source   *FleetExecutorAttemptFact
		attester string
	}{
		"another droplet (snapshot clone)": {attest(clone), &source, source.PrincipalSubject},
		"another device":                   {attest(restarted), &source, executor.PrincipalSubject(other.DeviceID)},
		"the source invocation itself":     {attest(old), &source, source.PrincipalSubject},
		"unknown source":                   {attest(restarted), nil, source.PrincipalSubject},
	} {
		if err := DecideFleetExecutorStopAttestation(tc.request, tc.source, tc.attester, now); executorCode(t, err) != executor.CodeFleetExecutorStopAttestationInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	stale := attest(restarted)
	stale.ObservedAt = now.Add(-executor.AttestationMaxAge - time.Minute)
	if err := DecideFleetExecutorStopAttestation(stale, &source, source.PrincipalSubject, now); executorCode(t, err) != executor.CodeFleetExecutorStopAttestationInvalid {
		t.Errorf("stale attestation: %v", err)
	}
	sameBoot := restarted
	sameBoot.BootID = old.BootID
	weak := attest(sameBoot)
	weak.SourceCgroupEmpty = false
	if err := DecideFleetExecutorStopAttestation(weak, &source, source.PrincipalSubject, now); executorCode(t, err) != executor.CodeFleetExecutorStopAttestationInvalid {
		t.Errorf("same boot without an empty cgroup: %v", err)
	}
}

func TestFleetExecutorProviderEvidenceDecision(t *testing.T) {
	host := fleettest.NewExecutorHost()
	now := time.Now()
	source := opsAttempt(host, "nodes_configured")
	source.HeartbeatExpiresAt = now.Add(-2 * time.Hour)
	evidence := executor.ProviderEvidence{Provider: "digitalocean", DropletID: host.DropletID, Status: executor.ProviderStatusOff, ObservedAt: now.Add(-time.Minute)}
	revoked := FleetExecutorDeviceFact{Exists: true, Revoked: true, Platform: executor.Platform}
	attempts := []FleetExecutorAttemptFact{source}
	if err := DecideFleetExecutorProviderEvidence(evidence, source.ID, attempts, revoked, now); err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	wrongDroplet := evidence
	wrongDroplet.DropletID = fleettest.NewExecutorHost().DropletID
	old := evidence
	old.ObservedAt = now.Add(-2 * time.Hour)
	recent := source
	recent.HeartbeatExpiresAt = now.Add(-10 * time.Minute)
	newer := source
	newer.ID, newer.Attempt = uuid.NewString(), 2
	for name, tc := range map[string]struct {
		evidence executor.ProviderEvidence
		source   string
		attempts []FleetExecutorAttemptFact
		device   FleetExecutorDeviceFact
		want     string
	}{
		"device still active":    {evidence, source.ID, attempts, FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}, executor.CodeFleetExecutorDeviceActive},
		"device unknown":         {evidence, source.ID, attempts, FleetExecutorDeviceFact{}, executor.CodeFleetExecutorDeviceActive},
		"another droplet":        {wrongDroplet, source.ID, attempts, revoked, executor.CodeFleetProviderEvidenceInvalid},
		"observed over an hour":  {old, source.ID, attempts, revoked, executor.CodeFleetProviderEvidenceInvalid},
		"not the latest attempt": {evidence, source.ID, []FleetExecutorAttemptFact{source, newer}, revoked, lifecycle.CodeFleetTargetReleaseEvidenceMismatch},
		"under the H5 age":       {evidence, source.ID, []FleetExecutorAttemptFact{recent}, revoked, lifecycle.CodeFleetTargetAbandonTooSoon},
		"live lease":             {evidence, source.ID, []FleetExecutorAttemptFact{opsAttempt(host, "x")}, revoked, lifecycle.CodeFleetTargetReleaseEvidenceMismatch},
		"unknown source":         {evidence, uuid.NewString(), attempts, revoked, lifecycle.CodeFleetTargetReleaseEvidenceMismatch},
	} {
		if err := DecideFleetExecutorProviderEvidence(tc.evidence, tc.source, tc.attempts, tc.device, now); executorCode(t, err) != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
	}
}

// --- PostgreSQL: readers and the executor-lane release ---

type opsFixture struct {
	t       *testing.T
	h       *pgExecutorHarness
	targets fleetTargetTestHarness
	target  string
	plan    fleettest.ExecutorPlanRef
	host    fleettest.ExecutorHost
	source  *model.FleetRunnerAttempt
}

func newOpsFixture(t *testing.T) *opsFixture {
	t.Helper()
	h := &pgExecutorHarness{t: t, db: fleetTargetConformanceDB(t), targets: map[string]string{}}
	f := &opsFixture{t: t, h: h, targets: fleetTargetTestHarness{db: h.db}}
	f.target = h.RegisterTarget("ops")
	f.plan = h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "ops"})
	f.host = fleettest.NewExecutorHost()
	started := h.Start(f.plan, f.host.DeviceID, fleettest.ExecutorStartRequest(f.plan, f.host, false))
	if started.Attempt == nil {
		t.Fatalf("start: %+v", started)
	}
	attempt, err := h.db.GetFleetRunnerAttempt(context.Background(), started.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.source = attempt
	return f
}

func (f *opsFixture) device(revoked bool) {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.h.db.Pool.Exec(ctx, `INSERT INTO access_devices(id,name,platform) VALUES($1,'executor',$2) ON CONFLICT (id) DO NOTHING`, f.host.DeviceID, executor.Platform); err != nil {
		f.t.Fatal(err)
	}
	if revoked {
		if _, err := f.h.db.Pool.Exec(ctx, `UPDATE access_devices SET revoked_at=now() WHERE id=$1`, f.host.DeviceID); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *opsFixture) lease(age time.Duration) {
	f.t.Helper()
	if _, err := f.h.db.Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_at=now()-$2::interval, heartbeat_expires_at=now()-$2::interval WHERE id=$1`, f.source.ID, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *opsFixture) evidence() *executor.ProviderEvidence {
	return &executor.ProviderEvidence{Provider: "digitalocean", DropletID: f.host.DropletID, Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC().Add(-time.Minute)}
}

func (f *opsFixture) abandon(key string, evidence *executor.ProviderEvidence) (AcceptedOperation, error) {
	return f.targets.acceptMutation(context.Background(), key, FleetTargetMutationAdmission{
		Kind: FleetTargetMutationAbandonPlan, PlanID: f.plan.ID,
		Release: &FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon), ProviderEvidence: evidence},
	}, nil)
}

func (f *opsFixture) generation() int64 {
	_, generation, err := f.targets.FenceState(context.Background(), f.target)
	if err != nil {
		f.t.Fatal(err)
	}
	return generation
}

func TestFleetExecutorOpsReadersPostgres(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	f.device(false)
	attempts, err := f.h.db.FleetExecutorAttempts(ctx, f.plan.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %v %v", attempts, err)
	}
	fact := attempts[0]
	if !fact.Executor || fact.PrincipalSubject != executor.PrincipalSubject(f.host.DeviceID) || fact.Metadata.DropletID != f.host.DropletID || !fact.Live(time.Now()) || fact.CurrentPhase == "" {
		t.Fatalf("fact: %+v", fact)
	}
	byID, err := f.h.db.FleetExecutorAttemptByID(ctx, f.source.ID)
	if err != nil || byID == nil || byID.PlanID != f.plan.ID {
		t.Fatalf("by id: %+v %v", byID, err)
	}
	if missing, err := f.h.db.FleetExecutorAttemptByID(ctx, uuid.NewString()); err != nil || missing != nil {
		t.Fatalf("unknown attempt: %+v %v", missing, err)
	}
	if device, err := f.h.db.FleetExecutorDevice(ctx, f.host.DeviceID); err != nil || !device.Exists || device.Revoked || device.Platform != executor.Platform {
		t.Fatalf("device: %+v %v", device, err)
	}
	if device, err := f.h.db.FleetExecutorDevice(ctx, "unknown"); err != nil || device.Exists {
		t.Fatalf("unknown device: %+v %v", device, err)
	}
	f.device(true)
	if device, _ := f.h.db.FleetExecutorDevice(ctx, f.host.DeviceID); !device.Revoked {
		t.Fatalf("revoked device: %+v", device)
	}
	if ok, err := f.h.db.FleetExecutorPlanIsConverge(ctx, f.plan.ID); err != nil || !ok {
		t.Fatalf("converge plan: %v %v", ok, err)
	}
	if ok, _ := f.h.db.FleetExecutorPlanIsConverge(ctx, uuid.NewString()); ok {
		t.Fatal("unknown plan is not a converge plan")
	}
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(fleetAttemptNeutral(f.source), f.host))
	rows, err := f.h.db.FleetExecutorLedgerRows(ctx, executor.KindStopAttestation, f.plan.ID)
	if err != nil || len(rows) != 1 || rows[0].Metadata[FleetExecutorAttesterMetadataKey] != executor.PrincipalSubject(f.host.DeviceID) {
		t.Fatalf("ledger rows: %+v %v", rows, err)
	}
}

func TestFleetExecutorAbandonExecutorLaneNeedsEvidence(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	// A live executor attempt is never released.
	if _, err := f.abandon("live", f.evidence()); err == nil {
		t.Fatal("a live attempt must refuse")
	}
	f.lease(10 * time.Minute) // expired, but inside the H5 age
	f.device(true)
	if _, err := f.abandon("no-evidence", nil); executorCode(t, err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("without evidence or attestation: %v", err)
	}
	if _, err := f.abandon("too-soon", f.evidence()); executorCode(t, err) != lifecycle.CodeFleetTargetAbandonTooSoon {
		t.Fatalf("inside the H5 age: %v", err)
	}
	f.lease(2 * time.Hour)
	// The GitHub listing snapshot never satisfies an executor-lane holder.
	if _, err := f.targets.acceptMutation(ctx, "listing-only", FleetTargetMutationAdmission{
		Kind: FleetTargetMutationAbandonPlan, PlanID: f.plan.ID, Release: &FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon)},
	}, &FleetTargetReleaseEvidence{ListingSnapshotSHA256: randomHex(64)}); executorCode(t, err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("a GitHub listing snapshot must not release an executor holder: %v", err)
	}
	wrong := f.evidence()
	wrong.DropletID = fleettest.NewExecutorHost().DropletID
	if _, err := f.abandon("wrong-droplet", wrong); executorCode(t, err) != executor.CodeFleetProviderEvidenceInvalid {
		t.Fatalf("another droplet: %v", err)
	}
	stale := f.evidence()
	stale.ObservedAt = time.Now().UTC().Add(-3 * time.Hour)
	if _, err := f.abandon("stale", stale); executorCode(t, err) != executor.CodeFleetProviderEvidenceInvalid {
		t.Fatalf("stale evidence: %v", err)
	}
	if _, err := f.h.db.Pool.Exec(ctx, `UPDATE access_devices SET revoked_at=NULL WHERE id=$1`, f.host.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.abandon("device-active", f.evidence()); executorCode(t, err) != executor.CodeFleetExecutorDeviceActive {
		t.Fatalf("evidence while the device is active: %v", err)
	}
	if abandoned, _ := f.targets.PlanAbandoned(ctx, f.plan.ID); abandoned || f.generation() == 0 {
		t.Fatalf("refusals must leave the holder alone (abandoned=%v generation=%d)", abandoned, f.generation())
	}

	f.device(true)
	good := f.evidence()
	accepted, err := f.abandon("good", good)
	if err != nil {
		t.Fatalf("valid evidence on a revoked device after the H5 age: %v", err)
	}
	if digest, _ := accepted.Operation.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string); digest == "" {
		t.Fatalf("the evidence digest is stored in operation metadata: %+v", accepted.Operation.Metadata)
	}
	held, _, err := f.targets.FenceState(ctx, f.target)
	if err != nil || held {
		t.Fatalf("the fence must be free: held=%v %v", held, err)
	}
	if abandoned, _ := f.targets.PlanAbandoned(ctx, f.plan.ID); !abandoned {
		t.Fatal("the plan must be permanently abandoned")
	}
	// A replay of the same signed request is a replay, not a second release.
	if replay, err := f.abandon("good", good); err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v %v", replay, err)
	}
}

func TestFleetExecutorTerminalReleaseByAttestation(t *testing.T) {
	f := newOpsFixture(t)
	ctx := context.Background()
	f.lease(2 * time.Hour)
	release := func(key string) error {
		_, err := f.targets.acceptMutation(ctx, key, FleetTargetMutationAdmission{
			Kind: FleetTargetMutationRelease, TargetID: f.target,
			Release: &FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeTerminal), ExpectedGeneration: f.generation()},
		}, nil)
		return err
	}
	if err := release("none"); executorCode(t, err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("no attestation: %v", err)
	}
	// An attestation from another droplet (a clone) is not stop proof.
	clone := f.host.NextInvocation()
	clone.BootID, clone.DropletID = uuid.NewString(), fleettest.NewExecutorHost().DropletID
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(fleetAttemptNeutral(f.source), clone))
	if err := release("clone"); executorCode(t, err) != executor.CodeFleetProviderEvidenceRequired {
		t.Fatalf("clone attestation: %v", err)
	}
	restarted := f.host.NextInvocation()
	restarted.BootID = uuid.NewString()
	f.h.SeedStopAttestation(f.plan, f.host.DeviceID, fleettest.ExecutorAttestation(fleetAttemptNeutral(f.source), restarted))
	if err := release("good"); err != nil {
		t.Fatalf("valid attestation: %v", err)
	}
	if held, _, _ := f.targets.FenceState(ctx, f.target); held {
		t.Fatal("the fence must be free")
	}
	if abandoned, _ := f.targets.PlanAbandoned(ctx, f.plan.ID); !abandoned {
		t.Fatal("a terminal release permanently abandons the holder plan")
	}
}

func TestFleetExecutorProviderEvidenceNotAcceptedForGitHubHolder(t *testing.T) {
	h := &pgExecutorHarness{t: t, db: fleetTargetConformanceDB(t), targets: map[string]string{}}
	h.RegisterTarget("gh")
	started := h.GitHubStart("gh")
	if started.Attempt == nil {
		t.Fatalf("github start: %+v", started)
	}
	targets := fleetTargetTestHarness{db: h.db}
	evidence := &executor.ProviderEvidence{Provider: "digitalocean", DropletID: "1", Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC()}
	_, err := targets.acceptMutation(context.Background(), "gh-evidence", FleetTargetMutationAdmission{
		Kind: FleetTargetMutationAbandonPlan, PlanID: started.PlanID,
		Release: &FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon), ProviderEvidence: evidence},
	}, &FleetTargetReleaseEvidence{ListingSnapshotSHA256: randomHex(64)})
	var validation *AcceptanceValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("providerEvidence on a GitHub-lane holder must be refused as invalid: %v", err)
	}
}

// A plan with no attempt has nothing the evidence could vouch for: it is
// refused rather than recorded unused.
func TestFleetExecutorProviderEvidenceRefusedWithoutAttempts(t *testing.T) {
	evidence := &executor.ProviderEvidence{Provider: "digitalocean", DropletID: "1", Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC()}
	base := &lifecycle.TerminalProof{ListingSnapshotSHA256: "x"}
	var validation *AcceptanceValidationError
	if _, _, err := FleetExecutorReleaseProof(nil, nil, evidence, FleetExecutorDeviceFact{}, base, time.Now()); !errors.As(err, &validation) {
		t.Fatalf("want a validation refusal, got %v", err)
	}
	if proof, lane, err := FleetExecutorReleaseProof(nil, nil, nil, FleetExecutorDeviceFact{}, base, time.Now()); err != nil || lane || proof != base {
		t.Fatal("without evidence a non-executor plan keeps its GitHub proof")
	}
}

// fleetAttemptNeutral adapts a PG attempt for the shared fleettest builders.
func fleetAttemptNeutral(a *model.FleetRunnerAttempt) *fleet.RunnerAttempt {
	neutral := lifecycle.FromLegacy(a)
	return &neutral
}
