package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// pgFactsFixture drives the real PG facts reader against a registered target,
// a signed converge plan and real executor-lane attempts.
type pgFactsFixture struct {
	t        *testing.T
	db       *store.DB
	h        *Handler
	facts    *pgConvergeFacts
	plan     *controller.ConvergeCapacityPlan
	ref      fleettest.ExecutorPlanRef
	resource *controller.Resource
}

func newPGFactsFixture(t *testing.T) *pgFactsFixture {
	t.Helper()
	db := integrationtest.PG(t)
	p := newPGLifecycleHarness(t, db)
	ctx := context.Background()
	target, err := db.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "facts", StateBackend: "s3://facts/state"}, []string{"cluster:facts"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureFleetResource(ctx, "facts", target.TargetID); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	plan, err := controller.BuildConvergePlan(controller.ConvergePlanInput{
		Resource: "facts", Cluster: "facts", Repository: "acme/norn-fleet", CommitSHA: commit, LastApplied: strings.Repeat("d", 40),
		PathClass: executor.PathClassConfig, Generation: 1, SigningKey: p.h.cfg.AuditSigningKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(plan)
	var payload map[string]interface{}
	_ = json.Unmarshal(encoded, &payload)
	started := time.Now().UTC().Add(-time.Hour)
	f := &pgFactsFixture{t: t, db: db, h: p.h, plan: plan,
		ref:   fleettest.ExecutorPlanRef{ID: plan.ID, Cluster: "facts", CommitSHA: commit, PlanSHA256: plan.PlanSHA256(), Generation: 1},
		facts: &pgConvergeFacts{h: p.h, now: time.Now}, resource: &controller.Resource{Name: "facts", Desired: controller.DesiredRevision{PlanID: plan.ID, CommitSHA: commit}}}
	f.insert(&model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: "facts", Payload: payload, StartedAt: started})
	return f
}

func (f *pgFactsFixture) insert(op *model.Operation) {
	f.t.Helper()
	now := time.Now().UTC()
	if op.StartedAt.IsZero() {
		op.StartedAt = now
	}
	op.UpdatedAt, op.Status = now, model.OperationSucceeded
	if op.ID == "" {
		op.ID = uuid.NewString()
	}
	if err := f.db.InsertCompletedOperation(context.Background(), op); err != nil {
		f.t.Fatal(err)
	}
}

func asMap(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *pgFactsFixture) start(host fleettest.ExecutorHost, resume bool) (*fleet.RunnerAttempt, error) {
	f.t.Helper()
	result, err := f.db.CreateFleetExecutorAttempt(context.Background(), store.FleetExecutorStart{
		PlanID: f.plan.ID, Request: fleettest.ExecutorStartRequest(f.ref, host, resume), PrincipalSubject: executor.PrincipalSubject(host.DeviceID),
	})
	if err != nil {
		return nil, err
	}
	attempt := lifecycle.FromLegacy(result.Attempt)
	return &attempt, nil
}

func (f *pgFactsFixture) attest(source *fleet.RunnerAttempt, attester fleettest.ExecutorHost, observedAt time.Time) {
	f.t.Helper()
	request := fleettest.ExecutorAttestation(source, attester)
	request.ObservedAt = observedAt
	f.insert(&model.Operation{Kind: executor.KindStopAttestation, Ref: f.plan.ID,
		Payload:  asMap(f.t, executor.StopAttestationPayload{PlanID: f.plan.ID, StopAttestationRequest: request}),
		Metadata: map[string]interface{}{store.FleetExecutorAttesterMetadataKey: executor.PrincipalSubject(attester.DeviceID)}})
}

func (f *pgFactsFixture) approveResume(source *fleet.RunnerAttempt) {
	f.t.Helper()
	f.insert(&model.Operation{Kind: executor.KindConvergeApprove, Ref: f.plan.ID,
		Payload: map[string]interface{}{"planId": f.plan.ID, "commitSha": f.ref.CommitSHA, "resume": true, "sourceAttemptId": source.ID}})
}

func (f *pgFactsFixture) read() *controller.ConvergeFacts {
	f.t.Helper()
	facts, err := f.facts.ConvergeFacts(context.Background(), f.resource, controller.Status{Active: &controller.ActiveRefs{PlanID: f.plan.ID}})
	if err != nil {
		f.t.Fatal(err)
	}
	return facts
}

// TestPGResumeApprovedSurvivesTheAttestationAge: the restarted loop attests
// once per invocation, so a --resume given more than five minutes later must
// still derive resumeApproved (P3 blocker); the recovery's admission keeps
// checking freshness.
func TestPGResumeApprovedSurvivesTheAttestationAge(t *testing.T) {
	f := newPGFactsFixture(t)
	old := fleettest.NewExecutorHost()
	source, err := f.start(old, false)
	if err != nil {
		t.Fatal(err)
	}
	restarted := old.NextInvocation()
	f.attest(source, restarted, time.Now().UTC().Add(-2*executor.AttestationMaxAge))

	if facts := f.read(); facts.ResumeApproved || facts.ResumeNeededAttemptID != source.ID {
		t.Fatalf("attested, not yet approved: %+v", facts)
	}
	f.approveResume(source)
	if facts := f.read(); !facts.ResumeApproved || facts.ResumeNeededAttemptID != "" {
		t.Fatalf("a resume approve given after the attestation aged out must still derive resumeApproved: %+v", facts)
	}

	// Admission still checks freshness: the stale attestation admits nothing.
	_, err = f.start(restarted, true)
	var refusal *executor.Error
	if !errors.As(err, &refusal) || refusal.Code != executor.CodeFleetExecutorStopAttestationInvalid {
		t.Fatalf("a stale attestation must not admit a recovery: %v", err)
	}
	// Every other rule still applies to the derivation: an attestation by
	// another device, or of another attempt, is no proof.
	stranger := fleettest.NewExecutorHost()
	f2 := newPGFactsFixture(t)
	source2, err := f2.start(fleettest.NewExecutorHost(), false)
	if err != nil {
		t.Fatal(err)
	}
	f2.attest(source2, stranger, time.Now().UTC().Add(-time.Minute))
	f2.approveResume(source2)
	if facts := f2.read(); facts.ResumeApproved || facts.ResumeNeededAttemptID != "" {
		t.Fatalf("another device cannot attest: %+v", facts)
	}
}

// TestPGKilledRecoveryNeedsANewResumeApprove: the resume approve is consumed
// by the recovery attempt; when that attempt dies and the restarted loop
// attests it, the record must ask for a new --resume approve even though the
// fence is held (P3 hand-off).
func TestPGKilledRecoveryNeedsANewResumeApprove(t *testing.T) {
	f := newPGFactsFixture(t)
	old := fleettest.NewExecutorHost()
	source, err := f.start(old, false)
	if err != nil {
		t.Fatal(err)
	}
	second := old.NextInvocation()
	f.attest(source, second, time.Now().UTC())
	f.approveResume(source)
	recovery, err := f.start(second, true)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if recovery.RetryOf != source.ID {
		t.Fatalf("recovery attempt: %+v", recovery)
	}
	// The recovery is running: nothing to approve, nothing approved.
	if facts := f.read(); facts.ResumeApproved || facts.ResumeNeededAttemptID != "" {
		t.Fatalf("a live recovery needs nothing: %+v", facts)
	}
	// It is killed; the restarted loop attests it.
	third := second.NextInvocation()
	f.attest(recovery, third, time.Now().UTC())
	facts := f.read()
	if facts.ResumeApproved || facts.ResumeNeededAttemptID != recovery.ID {
		t.Fatalf("the consumed approve must not approve the new recovery: %+v", facts)
	}
	// The derivation shows it through nextAction and the blocker.
	status := controller.Status{Active: &controller.ActiveRefs{PlanID: f.plan.ID, Occupancy: "uncertain"}, NextAction: controller.NextActionResolveUncertainOutcome}
	derived := controller.DeriveConverge(controller.Resource{Name: "facts", ExecutionMode: executor.ModeController}, status, facts)
	if !derived.ResumeNeeded || derived.NextAction != controller.NextActionApproveConverge || derived.Blocker == nil || !strings.Contains(derived.Blocker.Message, "--resume") {
		t.Fatalf("derivation: %+v / %+v", derived, derived.Blocker)
	}
	// A new approve naming the new source clears the need.
	f.approveResume(recovery)
	if facts := f.read(); !facts.ResumeApproved || facts.ResumeNeededAttemptID != "" {
		t.Fatalf("approved again: %+v", facts)
	}
}

// TestPGFactsReadRefusals: a destructive refusal sets the plan's refused; a
// state_lock_foreign refusal reports the lock only while its source attempt
// is the latest and never sets refused.
func TestPGFactsReadRefusals(t *testing.T) {
	f := newPGFactsFixture(t)
	host := fleettest.NewExecutorHost()
	source, err := f.start(host, false)
	if err != nil {
		t.Fatal(err)
	}
	if facts := f.read(); facts.Plan == nil || facts.Plan.Refused || facts.StateLock != nil {
		t.Fatalf("no refusal yet: %+v", facts)
	}
	refusal := func(request executor.ConvergeRefusalRequest) {
		f.insert(&model.Operation{Kind: executor.KindConvergeRefusal, Ref: f.plan.ID,
			Payload: asMap(t, executor.ConvergeRefusalPayload{PlanID: f.plan.ID, ConvergeRefusalRequest: request})})
	}
	base := executor.ConvergeRefusalRequest{CommitSHA: f.ref.CommitSHA, PlanSHA256: f.ref.PlanSHA256}

	lock := base
	lock.Code, lock.SourceAttemptID = executor.RefusalStateLockForeign, source.ID
	lock.TofuLock = &executor.RefusalTofuLock{ID: "lock-7", Who: "ops@laptop", Operation: "OperationTypePlan"}
	refusal(lock)
	facts := f.read()
	if facts.StateLock == nil || facts.StateLock.LockID != "lock-7" || facts.StateLock.SourceAttemptID != source.ID || facts.Plan.Refused {
		t.Fatalf("state_lock_foreign: lock=%+v refused=%v", facts.StateLock, facts.Plan.Refused)
	}

	// Another attempt becoming the latest ends the blocker.
	second := host.NextInvocation()
	f.attest(source, second, time.Now().UTC())
	f.approveResume(source)
	if _, err := f.start(second, true); err != nil {
		t.Fatal(err)
	}
	if facts := f.read(); facts.StateLock != nil {
		t.Fatalf("a newer attempt ends the state lock blocker: %+v", facts.StateLock)
	}

	destructive := base
	destructive.Code, destructive.PlanTextSHA256 = executor.RefusalDestructive, strings.Repeat("b", 64)
	refusal(destructive)
	if facts := f.read(); !facts.Plan.Refused {
		t.Fatalf("destructive refusal must set refused: %+v", facts.Plan)
	}
}

// TestFleetExecutionModeRoutePostgres drives the real route and E4b's PG
// recorder against the PG operation store's atomic
// SetFleetResourceExecutionMode: the change and the signed operation land
// together, a replay returns the first operation, and a refusal leaves
// nothing behind.
func TestFleetExecutionModeRoutePostgres(t *testing.T) {
	db := integrationtest.PG(t)
	p := newPGLifecycleHarness(t, db)
	routes := p.h.FleetResourceRoutes()
	mountFleetTargetRoutes(p.router, p.h.FleetTargetRoutes())
	mountFleetResourceRoutes(p.router, routes)
	p.router.Post("/api/v1/fleet/resources/{name}/execution-mode", routes.SetExecutionMode)
	target := fleetTargetRegisterResp(p.router, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "mode-route", StateBackend: "s3://mode-route/state"}, []string{"cluster:mode-route"})
	if target.TargetID == "" {
		t.Fatalf("register target: %+v", target)
	}
	if rec := fleetTargetDo(p.router, http.MethodPost, resourcePath("staging"), platformPrincipal(), "", map[string]string{"targetId": target.TargetID}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	modePath := resourcePath("staging") + "/execution-mode"
	admin := adminPrincipal()

	rec := fleetTargetDo(p.router, http.MethodPost, modePath, admin, "mode-1", map[string]interface{}{"mode": "controller", "expectedRevision": 0})
	if rec.Code != http.StatusCreated {
		t.Fatalf("switch to controller: %d %s", rec.Code, rec.Body)
	}
	var first model.Operation
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil || first.Kind != executor.KindResourceExecutionMode || first.Ref != "staging" {
		t.Fatalf("operation: %+v %v", first, err)
	}
	view := decodeResource(t, fleetTargetDo(p.router, http.MethodGet, resourcePath("staging"), readerPrincipal(), "", nil))
	if view.ExecutionMode != executor.ModeController || view.Revision != 1 {
		t.Fatalf("the change is applied with the operation: mode=%q revision=%d", view.ExecutionMode, view.Revision)
	}
	// Replay applies nothing and returns the first operation.
	replay := fleetTargetDo(p.router, http.MethodPost, modePath, admin, "mode-1", map[string]interface{}{"mode": "controller", "expectedRevision": 0})
	var again model.Operation
	if replay.Code != http.StatusOK || json.Unmarshal(replay.Body.Bytes(), &again) != nil || again.ID != first.ID {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body)
	}
	if rec := fleetTargetDo(p.router, http.MethodPost, modePath, admin, "mode-1", map[string]interface{}{"mode": "github", "expectedRevision": 0}); rec.Code != http.StatusConflict {
		t.Fatalf("key reuse: %d %s", rec.Code, rec.Body)
	}
	if rec := fleetTargetDo(p.router, http.MethodPost, modePath, admin, "mode-2", map[string]interface{}{"mode": "github", "expectedRevision": 0}); rec.Code != http.StatusConflict || problemCode(rec) != "fleet_resource_revision_conflict" {
		t.Fatalf("stale revision: %d %s", rec.Code, rec.Body)
	}
	watched, err := db.ListWatchedResources(context.Background())
	if err != nil || len(watched) != 1 || watched[0].Name != "staging" || watched[0].Cluster != "mode-route" {
		t.Fatalf("watched: %+v %v", watched, err)
	}
}
