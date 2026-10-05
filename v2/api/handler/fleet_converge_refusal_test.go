package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/store"
)

// refusalBackend adds the converge-plan facts to the E5 fake backend.
type refusalBackend struct {
	*fakeOpsBackend
	plan *store.FleetExecutorConvergePlanFact
}

func (b *refusalBackend) FleetExecutorConvergePlan(context.Context, string) (*store.FleetExecutorConvergePlanFact, error) {
	return b.plan, nil
}

type refusalFixture struct {
	*opsFixture
	backend *refusalBackend
	commit  string
	digest  string
}

func newRefusalFixture(t *testing.T) *refusalFixture {
	t.Helper()
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "infrastructure_applied")
	commit, digest := strings.Repeat("c", 40), strings.Repeat("a", 64)
	backend := &refusalBackend{fakeOpsBackend: f.backend, plan: &store.FleetExecutorConvergePlanFact{Resource: "staging", CommitSHA: commit, PlanSHA256: digest}}
	routes := NewFleetExecutorOpsRoutes(backend, opsTestSecret)
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/converge-refusals", routes.Refuse)
	f.router = router
	return &refusalFixture{opsFixture: f, backend: backend, commit: commit, digest: digest}
}

func (f *refusalFixture) refuse(principal *AccessPrincipal, key string, body interface{}) *httptest.ResponseRecorder {
	return fleetTargetDo(f.router, http.MethodPost, "/api/v1/fleet/plans/"+f.planID+"/converge-refusals", principal, key, body)
}

func (f *refusalFixture) destructive() map[string]interface{} {
	return map[string]interface{}{
		"code": executor.RefusalDestructive, "commitSha": f.commit, "planSha256": f.digest,
		"planTextSha256": strings.Repeat("b", 64), "changes": map[string]interface{}{"add": 1, "destroy": 2},
	}
}

func (f *refusalFixture) stateLock(sourceAttemptID string) map[string]interface{} {
	return map[string]interface{}{
		"code": executor.RefusalStateLockForeign, "commitSha": f.commit, "planSha256": f.digest, "sourceAttemptId": sourceAttemptID,
		"tofuLock": map[string]interface{}{"id": "lock-7", "operation": "OperationTypePlan", "who": "ops@laptop", "created": "2026-10-05 10:00:00 UTC"},
	}
}

func TestConvergeRefusalDestructiveRecordsLedgerOperation(t *testing.T) {
	f := newRefusalFixture(t)
	principal := opsExecutorPrincipal()
	key := "executor-refusal:" + f.planID

	rec := f.refuse(principal, key, f.destructive())
	if rec.Code != http.StatusCreated {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
	}
	op := f.backend.ops.last(executor.KindConvergeRefusal)
	var payload executor.ConvergeRefusalPayload
	if op.Operation.Ref != f.planID || !decodeLedgerValueMap(op.Operation.Payload, &payload) || payload.PlanID != f.planID ||
		payload.Code != executor.RefusalDestructive || payload.CommitSHA != f.commit || payload.PlanSHA256 != f.digest ||
		!strings.Contains(string(payload.Changes), `"destroy":2`) || op.Operation.Metadata[store.FleetExecutorAttesterMetadataKey] != executor.PrincipalSubject(opsDevice) {
		t.Fatalf("ledger operation: %+v", op.Operation)
	}

	// A lost response is retried with the same key until a 2xx: the first
	// operation comes back and nothing new is recorded.
	replay := f.refuse(principal, key, f.destructive())
	if replay.Code != http.StatusOK || f.backend.ops.count(executor.KindConvergeRefusal) != 1 {
		t.Fatalf("replay: %d %s (%d operations)", replay.Code, replay.Body.String(), f.backend.ops.count(executor.KindConvergeRefusal))
	}
	// The same key with another body is a conflict, not a second refusal.
	other := f.destructive()
	other["planTextSha256"] = strings.Repeat("d", 64)
	requireProblem(t, f.refuse(principal, key, other), http.StatusConflict, "")
	if f.backend.ops.count(executor.KindConvergeRefusal) != 1 {
		t.Fatal("a conflicting body recorded something")
	}
}

func TestConvergeRefusalAdmittedWhileRecoveryAttemptIsLive(t *testing.T) {
	f := newRefusalFixture(t)
	source := f.attempt
	source.HeartbeatExpiresAt = time.Now().Add(-time.Hour)
	recovery := source
	recovery.ID, recovery.Attempt, recovery.RetryOf = uuid.NewString(), 2, source.ID
	recovery.HeartbeatExpiresAt = time.Now().Add(time.Minute)
	f.backend.attempts = []store.FleetExecutorAttemptFact{source, recovery}

	if rec := f.refuse(opsExecutorPrincipal(), "executor-refusal:"+f.planID, f.destructive()); rec.Code != http.StatusCreated {
		t.Fatalf("a destructive remainder is reported while the recovery attempt is live: %d %s", rec.Code, rec.Body.String())
	}
	// The foreign lock is reported for the recovery's source only while it is
	// the latest attempt: here the recovery has already started.
	requireProblem(t, f.refuse(opsExecutorPrincipal(), "lock-source", f.stateLock(source.ID)), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	if rec := f.refuse(opsExecutorPrincipal(), "lock-recovery", f.stateLock(recovery.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("lock of the live recovery attempt: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConvergeRefusalStateLockForeign(t *testing.T) {
	f := newRefusalFixture(t)
	principal := opsExecutorPrincipal()
	// The recovery runs because the source loop died: its lease has expired
	// but it is still the latest, unfinished attempt.
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-time.Hour)

	if rec := f.refuse(principal, "lock-1", f.stateLock(f.attempt.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("state_lock_foreign: %d %s", rec.Code, rec.Body.String())
	}
	var payload executor.ConvergeRefusalPayload
	if !decodeLedgerValueMap(f.backend.ops.last(executor.KindConvergeRefusal).Operation.Payload, &payload) ||
		payload.SourceAttemptID != f.attempt.ID || payload.TofuLock == nil || payload.TofuLock.ID != "lock-7" {
		t.Fatalf("payload: %+v", payload)
	}
	// Unreadable lock text: every tofuLock field may be empty.
	empty := f.stateLock(f.attempt.ID)
	empty["tofuLock"] = map[string]interface{}{}
	if rec := f.refuse(principal, "lock-2", empty); rec.Code != http.StatusCreated {
		t.Fatalf("empty tofuLock: %d %s", rec.Code, rec.Body.String())
	}

	// Not the latest attempt, an unknown attempt, a succeeded attempt and a
	// GitHub-lane attempt are all refused.
	f.backend.attempts = append(f.backend.attempts, store.FleetExecutorAttemptFact{ID: uuid.NewString(), PlanID: f.planID, Attempt: 2, Status: "running", Executor: true, RetryOf: f.attempt.ID})
	requireProblem(t, f.refuse(principal, "lock-3", f.stateLock(f.attempt.ID)), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	requireProblem(t, f.refuse(principal, "lock-4", f.stateLock(uuid.NewString())), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	f.backend.attempts = f.backend.attempts[:1]
	f.backend.attempts[0].Status = "succeeded"
	requireProblem(t, f.refuse(principal, "lock-5", f.stateLock(f.attempt.ID)), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	f.backend.attempts[0].Status, f.backend.attempts[0].Executor = "running", false
	requireProblem(t, f.refuse(principal, "lock-6", f.stateLock(f.attempt.ID)), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	if f.backend.ops.count(executor.KindConvergeRefusal) != 2 {
		t.Fatalf("refused requests recorded something: %d", f.backend.ops.count(executor.KindConvergeRefusal))
	}
	// A dead loop's attempt that PG's read-time sweep moved to abandoned is
	// still the recoverable source (recovery admission refuses only succeeded).
	f.backend.attempts[0].Status, f.backend.attempts[0].Executor = "abandoned", true
	if rec := f.refuse(principal, "lock-7", f.stateLock(f.attempt.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("abandoned (swept) source: status %d body %s", rec.Code, rec.Body.String())
	}
	if f.backend.ops.count(executor.KindConvergeRefusal) != 3 {
		t.Fatalf("refused requests recorded something: %d", f.backend.ops.count(executor.KindConvergeRefusal))
	}
}

func TestConvergeRefusalChecksPrincipalAndPlan(t *testing.T) {
	f := newRefusalFixture(t)
	key := "executor-refusal:" + f.planID
	for name, principal := range map[string]*AccessPrincipal{
		"admin":  fleetTargetPrincipal(ScopeAdmin),
		"reader": fleetTargetPrincipal(ScopeAPIRead),
		"nobody": nil,
	} {
		rec := f.refuse(principal, key, f.destructive())
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	ci := executorPrincipal()
	ci.CI = &CIIdentity{Provider: "github-actions", Repository: "o/r"}
	requireProblem(t, f.refuse(&ci, key, f.destructive()), http.StatusForbidden, "insufficient_scope")
	// The attempt-bound credential is not the executor.
	attempt := AccessPrincipal{Subject: "fleet-attempt:" + f.attempt.ID, TokenID: "t", Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	requireProblem(t, f.refuse(&attempt, key, f.destructive()), http.StatusForbidden, "insufficient_scope")
	// A revoked executor device is refused.
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Revoked: true, Platform: executor.Platform}
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, f.destructive()), http.StatusForbidden, "insufficient_scope")
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}

	requireProblem(t, f.refuse(opsExecutorPrincipal(), "", f.destructive()), http.StatusBadRequest, "invalid_idempotency_key")
	bad := f.destructive()
	bad["code"] = "something_else"
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, bad), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	bad = f.destructive()
	bad["sourceAttemptId"] = f.attempt.ID
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, bad), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	bad = f.destructive()
	bad["commitSha"] = "nope"
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, bad), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)

	// The plan: unknown or not a converge plan, another commit, another digest.
	mismatch := f.destructive()
	mismatch["commitSha"] = strings.Repeat("e", 40)
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, mismatch), http.StatusConflict, executor.CodeFleetConvergeCommitMismatch)
	mismatch = f.destructive()
	mismatch["planSha256"] = strings.Repeat("e", 64)
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, mismatch), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	f.backend.plan = nil
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, f.destructive()), http.StatusConflict, executor.CodeFleetPlanNotConverge)
	if f.backend.ops.count(executor.KindConvergeRefusal) != 0 {
		t.Fatal("a refused request recorded something")
	}
}

// TestConvergeRefusalIsBoundToThePlan: only a device with a credential-grant
// ledger row for the plan, or an executor attempt on it, may refuse it. P2
// requests the plan-phase grant before it refuses, so that grant binds.
func TestConvergeRefusalIsBoundToThePlan(t *testing.T) {
	f := newRefusalFixture(t)
	// Another device's attempt is the plan's only history.
	f.backend.attempts[0].PrincipalSubject = executor.PrincipalSubject("dev-other")
	routes := NewFleetExecutorOpsRoutes(f.backend, opsTestSecret)
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/credential-grants", routes.Grant)
	router.Post("/api/v1/fleet/plans/{planID}/converge-refusals", routes.Refuse)
	f.router = router
	f.backend.converge = true
	key := "executor-refusal:" + f.planID

	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, f.destructive()), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	other := f.destructive()
	other["code"] = executor.RefusalStateLockForeign
	delete(other, "planTextSha256")
	delete(other, "changes")
	other["sourceAttemptId"], other["tofuLock"] = f.attempt.ID, map[string]interface{}{"id": "l"}
	requireProblem(t, f.refuse(opsExecutorPrincipal(), "lock-x", other), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)
	if f.backend.ops.count(executor.KindConvergeRefusal) != 0 {
		t.Fatal("an unbound device recorded a refusal")
	}

	// A grant for ANOTHER plan does not bind this one.
	otherPlan := uuid.NewString()
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/plans/"+otherPlan+"/credential-grants", opsExecutorPrincipal(), "g-other", f.grantBody(executor.GrantPhasePlan, executor.CredDORead))
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant for another plan: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, f.refuse(opsExecutorPrincipal(), key, f.destructive()), http.StatusConflict, executor.CodeFleetConvergeRefusalRejected)

	// The plan-phase grant for this plan binds the device.
	rec = fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/plans/"+f.planID+"/credential-grants", opsExecutorPrincipal(), "g-plan", f.grantBody(executor.GrantPhasePlan, executor.CredDORead))
	if rec.Code != http.StatusCreated {
		t.Fatalf("plan grant: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.refuse(opsExecutorPrincipal(), key, f.destructive()); rec.Code != http.StatusCreated {
		t.Fatalf("a granted device refuses its plan: %d %s", rec.Code, rec.Body.String())
	}
}

// TestConvergeRefusalRouteIsAnExecutorRoute: the route is in the executor's
// route table, so E6's scope matcher admits exactly it.
func TestConvergeRefusalRouteIsAnExecutorRoute(t *testing.T) {
	found := false
	for _, route := range executor.ExecutorRoutes {
		found = found || route == "POST /api/v1/fleet/plans/{planID}/converge-refusals"
	}
	if !found {
		t.Fatal("converge-refusals is missing from executor.ExecutorRoutes")
	}
	for _, route := range executor.AttemptCredentialRoutes {
		if strings.Contains(route, "converge-refusals") {
			t.Fatal("the attempt-bound credential must not reach converge-refusals")
		}
	}
}

// TestRotateRefusesTheAttemptBoundCredential: /auth/rotate answers 403 for a
// fleet:attempt principal before any device lookup or token issue, even when
// the principal carries a device that exists.
func TestRotateRefusesTheAttemptBoundCredential(t *testing.T) {
	auth := &lifecycleAuthStore{platform: FleetExecutorPlatform}
	principal := AccessPrincipal{
		Subject: "fleet-attempt:" + uuid.NewString(), TokenID: "attempt-jti", DeviceID: "dev-1",
		Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken,
	}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &principal)
	rec := httptest.NewRecorder()
	RotateManagedToken(&config.Config{APIToken: opsTestSecret}, auth, nil, rec, req)
	requireProblem(t, rec, http.StatusForbidden, "insufficient_scope")
	if auth.issued != nil || auth.rotatedFrom != "" || strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("a token was issued to the attempt credential: %+v", auth.issued)
	}
	// A managed token without a device claim is refused explicitly too.
	principal = AccessPrincipal{Subject: "service", TokenID: "svc-jti", Scopes: []string{ScopeAPIRead}, Source: AccessPrincipalSourceManagedToken}
	req = WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &principal)
	rec = httptest.NewRecorder()
	RotateManagedToken(&config.Config{APIToken: opsTestSecret}, auth, nil, rec, req)
	requireProblem(t, rec, http.StatusForbidden, "insufficient_scope")
	if auth.issued != nil {
		t.Fatalf("a token was issued without a device: %+v", auth.issued)
	}
}

// heldViewFixture is a controller-mode resource whose fence is held by plan
// "plan-c" (the same plan as its desired converge plan).
func heldViewFixture(t *testing.T, facts *controller.ConvergeFacts) FleetResourceView {
	t.Helper()
	backend := newFakeResourceBackend()
	resource, _ := backend.EnsureResource(context.Background(), "staging", resTargetID)
	resource.ExecutionMode = executor.ModeController
	resource.Desired = controller.DesiredRevision{Generation: 1, PlanID: "plan-c", CommitSHA: strings.Repeat("c", 40), Repository: testFleetRepoName,
		Verification: controller.VerificationGitHubProtectedMain, AcceptedAt: time.Now(), AcceptedBy: "watcher"}
	resource.Status = controller.Status{
		Active:      &controller.ActiveRefs{PlanID: "plan-c", AttemptIDs: []string{"attempt-2"}, FenceGeneration: 3, Occupancy: "uncertain", OccupancyReason: "heartbeat_expired"},
		NextAction:  controller.NextActionResolveUncertainOutcome,
		EvaluatedAt: time.Now(),
	}
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
	routes.SetConvergeFacts(&fakeFacts{facts: facts})
	rec := fleetTargetDo(modeRouter(routes, readerPrincipal()), http.MethodGet, resourcePath("staging"), readerPrincipal(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	return decodeResource(t, rec)
}

// TestResourceViewSurfacesHeldConvergeBlockers: while the fence is held the
// record still shows a stopped attempt that needs a new resume approve, the
// destructive remainder and a foreign state lock (P2/P3 hand-offs), and
// refused is set only by the destructive refusal.
func TestResourceViewSurfacesHeldConvergeBlockers(t *testing.T) {
	plan := func() *controller.ConvergePlanFacts {
		return &controller.ConvergePlanFacts{PlanID: "plan-c", CommitSHA: strings.Repeat("c", 40), BaseCommitSHA: strings.Repeat("d", 40), PlanSHA256: strings.Repeat("a", 64), Approved: true}
	}

	view := heldViewFixture(t, &controller.ConvergeFacts{Plan: plan(), ResumeNeededAttemptID: "attempt-2"})
	if view.Status.NextAction != controller.NextActionApproveConverge || view.Blocker == nil || view.Blocker.Reason != controller.BlockerReasonApprovalRequired ||
		!strings.Contains(view.Blocker.Message, "norn fleet converge staging --resume") || !strings.Contains(view.Blocker.Message, "attempt-2") {
		t.Fatalf("stopped attempt: nextAction=%q blocker=%+v", view.Status.NextAction, view.Blocker)
	}
	if view.Status.Active == nil || view.Status.Active.ResumeApproved {
		t.Fatalf("active: %+v", view.Status.Active)
	}

	view = heldViewFixture(t, &controller.ConvergeFacts{Plan: plan(), StateLock: &controller.StateLockFacts{SourceAttemptID: "attempt-2", LockID: "lock-7"}})
	if view.Blocker == nil || view.Blocker.Reason != controller.BlockerReasonStateLockForeign || view.Blocker.Kind != controller.BlockerKindExecution {
		t.Fatalf("state_lock_foreign blocker: %+v", view.Blocker)
	}
	if view.Desired.Converge == nil || view.Desired.Converge.Refused {
		t.Fatalf("state_lock_foreign must not set refused: %+v", view.Desired.Converge)
	}

	refused := plan()
	refused.Refused = true
	view = heldViewFixture(t, &controller.ConvergeFacts{Plan: refused})
	if view.Blocker == nil || view.Blocker.Reason != controller.BlockerReasonDestructive || view.Desired.Converge == nil || !view.Desired.Converge.Refused {
		t.Fatalf("destructive remainder while held: blocker=%+v converge=%+v", view.Blocker, view.Desired.Converge)
	}

	// Nothing to surface: the held fence keeps its execution blocker.
	view = heldViewFixture(t, &controller.ConvergeFacts{Plan: plan()})
	if view.Blocker == nil || view.Blocker.Kind != "execution" || view.Status.NextAction != controller.NextActionResolveUncertainOutcome {
		t.Fatalf("held fence: blocker=%+v nextAction=%q", view.Blocker, view.Status.NextAction)
	}
}

// Both backends' stores satisfy the capability the execution-mode route needs.
var (
	_ FleetExecutionModeStore = (*store.PGOperationStore)(nil)
	_ FleetExecutionModeStore = (*etcdstore.V3OperationStore)(nil)
)
