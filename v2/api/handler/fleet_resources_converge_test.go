package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
)

// atomicModeStore is an in-memory FleetExecutionModeRecorder with the
// FleetExecutionModeStore contract: request identity first (a replay applies
// nothing; another body under the key is refused), then the fence, then the
// revision, and the mode and its operation land together or not at all.
type atomicModeStore struct {
	backend   *fakeResourceBackend
	fenceHeld bool
	calls     int
	byKey     map[string]executor.ExecutionModeRequest
	got       []executor.ExecutionModeRequest
}

func (f *atomicModeStore) RecordExecutionMode(w http.ResponseWriter, r *http.Request, key, resource string, request executor.ExecutionModeRequest) (model.Operation, bool, bool) {
	f.calls++
	op := model.Operation{ID: uuid.NewString(), Kind: executor.KindResourceExecutionMode, Ref: resource, Status: model.OperationSucceeded, StartedAt: time.Now(), UpdatedAt: time.Now()}
	if previous, ok := f.byKey[key]; ok {
		if previous != request {
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "key reused with another request")
			return model.Operation{}, false, false
		}
		return op, true, true
	}
	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	stored, ok := f.backend.resources[resource]
	switch {
	case !ok:
		writeFleetResourceError(w, r, errFleetResourceNotFound)
		return model.Operation{}, false, false
	case f.fenceHeld:
		writeExecutorError(w, r, &executor.Error{Code: executor.CodeFleetExecutionModeFenceHeld, Reason: "the target fence is held"})
		return model.Operation{}, false, false
	case stored.Revision != request.ExpectedRevision:
		writeFleetResourceError(w, r, errFleetResourceRevisionConflict)
		return model.Operation{}, false, false
	}
	if controller.EffectiveExecutionMode(stored.ExecutionMode) != request.Mode {
		stored.ExecutionMode = request.Mode
		stored.Revision++
	}
	if f.byKey == nil {
		f.byKey = map[string]executor.ExecutionModeRequest{}
	}
	f.byKey[key] = request
	f.got = append(f.got, request)
	return op, false, true
}

type fakeFacts struct {
	facts *controller.ConvergeFacts
	err   error
}

func (f *fakeFacts) ConvergeFacts(context.Context, *controller.Resource, controller.Status) (*controller.ConvergeFacts, error) {
	return f.facts, f.err
}

func modeRouter(routes *FleetResourceRoutes, principal *AccessPrincipal) http.Handler {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal != nil {
				r = WithAccessPrincipal(r, principal)
			}
			next.ServeHTTP(w, r)
		})
	})
	mountFleetResourceRoutes(router, routes)
	router.Post("/api/v1/fleet/resources/{name}/execution-mode", routes.SetExecutionMode)
	return router
}

func adminPrincipal() *AccessPrincipal { return fleetTargetPrincipal(ScopeAdmin) }

func modeFixture(t *testing.T) (*fakeResourceBackend, *atomicModeStore, *FleetResourceRoutes) {
	t.Helper()
	backend := newFakeResourceBackend()
	if _, err := backend.EnsureResource(context.Background(), "staging", resTargetID); err != nil {
		t.Fatal(err)
	}
	recorder := &atomicModeStore{backend: backend}
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
	routes.SetExecutionModeRecorder(recorder)
	return backend, recorder, routes
}

func TestModeSwitchAdminAndFreeFence(t *testing.T) {
	backend, recorder, routes := modeFixture(t)
	path := resourcePath("staging") + "/execution-mode"
	body := map[string]interface{}{"mode": "controller", "expectedRevision": 0}

	// Admin only, never a workload identity, and never without a key.
	denied := map[string]*AccessPrincipal{
		"api:write":  writerPrincipal(),
		"platform":   platformPrincipal(),
		"read":       readerPrincipal(),
		"ci admin":   {Subject: "ci", TokenID: "t", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}, CI: &CIIdentity{}},
		"anonymous":  nil,
		"fleet:exec": func() *AccessPrincipal { p := executorPrincipal(); return &p }(),
	}
	for name, p := range denied {
		rec := fleetTargetDo(modeRouter(routes, p), http.MethodPost, path, p, uuid.NewString(), body)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d %s", name, rec.Code, rec.Body)
		}
	}
	if recorder.calls != 0 || len(recorder.got) != 0 {
		t.Fatalf("a refused caller changed the mode: calls=%d ops=%d", recorder.calls, len(recorder.got))
	}
	admin := adminPrincipal()
	router := modeRouter(routes, admin)
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "k", map[string]interface{}{"mode": "hybrid", "expectedRevision": 0}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d", rec.Code)
	}

	// Fence held: refused, mode unchanged, nothing recorded.
	recorder.fenceHeld = true
	rec := fleetTargetDo(router, http.MethodPost, path, admin, "held", body)
	if rec.Code != http.StatusConflict || problemCode(rec) != executor.CodeFleetExecutionModeFenceHeld {
		t.Fatalf("held fence: %d %s", rec.Code, rec.Body)
	}
	if got, _ := backend.GetResource(context.Background(), "staging"); got.ExecutionMode != "" || len(recorder.got) != 0 {
		t.Fatalf("a held fence changed the mode or recorded an op: %q %d", got.ExecutionMode, len(recorder.got))
	}

	// Free fence: switch, ledger op recorded, view reports it.
	recorder.fenceHeld = false
	rec = fleetTargetDo(router, http.MethodPost, path, admin, "set", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body)
	}
	if len(recorder.got) != 1 || recorder.got[0].Mode != executor.ModeController {
		t.Fatalf("ledger = %+v", recorder.got)
	}
	read := fleetTargetDo(router, http.MethodGet, resourcePath("staging"), readerPrincipal(), "", nil)
	if view := decodeResource(t, read); view.ExecutionMode != executor.ModeController || view.Revision != 1 {
		t.Fatalf("view mode=%q revision=%d", view.ExecutionMode, view.Revision)
	}

	// Replay of the same key with the same intent is a 200 replay.
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "set", body); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	// An old key cannot flip the mode back after another admin changed it.
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "back", map[string]interface{}{"mode": "github", "expectedRevision": 1}); rec.Code != http.StatusCreated {
		t.Fatalf("switch back: %d", rec.Code)
	}
	// A late replay returns the stored operation and re-applies nothing.
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "set", body); rec.Code != http.StatusOK {
		t.Fatalf("late replay: %d %s", rec.Code, rec.Body)
	}
	// The key with another body is refused and applies nothing.
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "set", map[string]interface{}{"mode": "controller", "expectedRevision": 2}); rec.Code != http.StatusConflict || problemCode(rec) != "idempotency_key_reused" {
		t.Fatalf("key reuse: %d %s", rec.Code, rec.Body)
	}
	// A fresh key with a stale expectedRevision is a revision conflict.
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "stale", body); rec.Code != http.StatusConflict || problemCode(rec) != "fleet_resource_revision_conflict" {
		t.Fatalf("stale revision: %d %s", rec.Code, rec.Body)
	}
	if got, _ := backend.GetResource(context.Background(), "staging"); got.ExecutionMode != executor.ModeGitHub || got.Revision != 2 || len(recorder.got) != 2 {
		t.Fatalf("mode = %q revision = %d ops = %d", got.ExecutionMode, got.Revision, len(recorder.got))
	}
	// Unknown resource.
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("nope")+"/execution-mode", admin, "k", body); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: %d", rec.Code)
	}
}

func TestExecutionModeSwitchUnsupportedStoreFailsClosed(t *testing.T) {
	backend := newFakeResourceBackend()
	if _, err := backend.EnsureResource(context.Background(), "staging", resTargetID); err != nil {
		t.Fatal(err)
	}
	admin := adminPrincipal()
	body := map[string]interface{}{"mode": "controller", "expectedRevision": 0}
	// No recorder wired (the etcd routes until E6), and the PG recorder over
	// an operation store without SetFleetResourceExecutionMode: both 503,
	// nothing applied.
	for name, recorder := range map[string]FleetExecutionModeRecorder{"none": nil, "pg without store method": &pgExecutionModeRecorder{h: &Handler{}}} {
		routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
		if recorder != nil {
			routes.SetExecutionModeRecorder(recorder)
		}
		rec := fleetTargetDo(modeRouter(routes, admin), http.MethodPost, resourcePath("staging")+"/execution-mode", admin, "k", body)
		if rec.Code != http.StatusServiceUnavailable || problemCode(rec) != executor.CodeFleetExecutionModeUnavailable {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if got, _ := backend.GetResource(context.Background(), "staging"); got.ExecutionMode != "" || got.Revision != 0 {
		t.Fatalf("mode changed: %q %d", got.ExecutionMode, got.Revision)
	}
}

func TestApproveRefusesExecutorAndCIPrincipals(t *testing.T) {
	planID, commit := uuid.NewString(), strings.Repeat("b", 40)
	plans := &fakeConvergePlans{plan: &controller.RecordedConverge{PlanID: planID, Converge: executor.ConvergePlan{CommitSHA: commit}}}
	approvals := &fakeApprovals{}
	routes := NewFleetConvergeRoutes(&fakeRescanner{}, plans, approvals)
	path := "/api/v1/fleet/plans/" + planID + "/converge/approve"
	body := `{"commitSha":"` + commit + `"}`
	exec := executorPrincipal()
	for name, p := range map[string]*AccessPrincipal{
		"executor":        &exec,
		"ci operate":      {Subject: "ci", TokenID: "t", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopePlatformOperate}, CI: &CIIdentity{}},
		"ci admin":        {Subject: "ci", TokenID: "t", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}, CI: &CIIdentity{}},
		"executor+admin?": {Subject: "x", TokenID: "t", DeviceID: "d", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetExecute}, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if rec := doConverge(convergeRouter(routes, p), path, "k", body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(approvals.got) != 0 {
		t.Fatalf("approve recorded for a refused principal: %+v", approvals.got)
	}
}

func convergeFixture(t *testing.T, facts *controller.ConvergeFacts, mode, verification string) FleetResourceView {
	t.Helper()
	backend := newFakeResourceBackend()
	resource, _ := backend.EnsureResource(context.Background(), "staging", resTargetID)
	resource.ExecutionMode = mode
	if verification != "" {
		resource.Desired = controller.DesiredRevision{Generation: 1, PlanID: "plan-c", CommitSHA: strings.Repeat("c", 40), Repository: testFleetRepoName,
			Verification: verification, AcceptedAt: time.Now(), AcceptedBy: "watcher"}
	}
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
	routes.SetConvergeFacts(&fakeFacts{facts: facts})
	rec := fleetTargetDo(modeRouter(routes, readerPrincipal()), http.MethodGet, resourcePath("staging"), readerPrincipal(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	return decodeResource(t, rec)
}

func TestApprovalPolicyReportsMergeGate(t *testing.T) {
	plan := &controller.ConvergePlanFacts{PlanID: "plan-c", CommitSHA: strings.Repeat("c", 40), BaseCommitSHA: strings.Repeat("d", 40), PlanSHA256: strings.Repeat("a", 64), RequiresApproval: true}

	// Executor lane, protected-main desired, approved by an operator.
	approved := *plan
	approved.Approved, approved.ApprovedBy = true, "operator"
	view := convergeFixture(t, &controller.ConvergeFacts{Plan: &approved}, executor.ModeController, controller.VerificationGitHubProtectedMain)
	p := view.ApprovalPolicy
	if p.Basis != controller.VerificationGitHubProtectedMain || !p.ProtectedBranch || !p.ProtectedBranchVerified || !p.ValidateWorkflowSucceeded {
		t.Fatalf("trigger controls: %+v", p)
	}
	// What the watcher does not prove must not be claimed.
	if p.MergedPullRequest || p.PlanWorkflowSucceeded || p.BoundPlanDigest || p.OwnerApprovalEnvelope || p.EnvironmentReviewers != "not_enforced" || p.IndependentReview != "not_enforced" {
		t.Fatalf("over-claimed: %+v", p)
	}
	if p.MergeGate != "config_auto_executable_approve" || p.DestructiveApproval != "github_lane_dispatch" || p.RequiredReviews != "not_verified" || p.ApprovedBy != "operator" {
		t.Fatalf("merge gate: %+v", p)
	}

	// Not approved: approvedBy is absent, the gate is still reported.
	view = convergeFixture(t, &controller.ConvergeFacts{Plan: plan}, executor.ModeController, controller.VerificationGitHubProtectedMain)
	if view.ApprovalPolicy.ApprovedBy != "" || view.ApprovalPolicy.MergeGate == "" {
		t.Fatalf("unapproved policy: %+v", view.ApprovalPolicy)
	}

	// GitHub lane: every executor field is omitted from the wire.
	view = convergeFixture(t, nil, "", controller.VerificationGitHubMergedPlan)
	g := view.ApprovalPolicy
	if g.MergeGate != "" || g.DestructiveApproval != "" || g.RequiredReviews != "" || g.ApprovedBy != "" || g.ProtectedBranchVerified || g.ValidateWorkflowSucceeded || !g.MergedPullRequest {
		t.Fatalf("github lane policy: %+v", g)
	}
	if view.ExecutionMode != "" || view.Desired.Converge != nil {
		t.Fatalf("github lane view: mode=%q converge=%+v", view.ExecutionMode, view.Desired.Converge)
	}
}

func TestResourceViewReportsConverge(t *testing.T) {
	plan := &controller.ConvergePlanFacts{PlanID: "plan-c", CommitSHA: strings.Repeat("c", 40), BaseCommitSHA: strings.Repeat("d", 40), PlanSHA256: strings.Repeat("a", 64), RequiresApproval: true}
	view := convergeFixture(t, &controller.ConvergeFacts{Plan: plan}, executor.ModeController, controller.VerificationGitHubProtectedMain)
	c := view.Desired.Converge
	if c == nil || c.PlanSHA256 != plan.PlanSHA256 || !c.RequiresApproval || c.Approved || c.Refused {
		t.Fatalf("converge = %+v", c)
	}
	if view.Status.NextAction != controller.NextActionApproveConverge {
		t.Fatalf("nextAction = %q", view.Status.NextAction)
	}
	if view.Blocker == nil || view.Blocker.Kind != controller.BlockerKindApproval || view.Blocker.Reason != controller.BlockerReasonApprovalRequired || view.Blocker.PlanID != "plan-c" {
		t.Fatalf("blocker = %+v", view.Blocker)
	}

	refused := *plan
	refused.Refused = true
	view = convergeFixture(t, &controller.ConvergeFacts{Plan: &refused}, executor.ModeController, controller.VerificationGitHubProtectedMain)
	if view.Desired.Converge == nil || !view.Desired.Converge.Refused || view.Blocker == nil || view.Blocker.Reason != controller.BlockerReasonDestructive {
		t.Fatalf("refused view: %+v blocker=%+v", view.Desired.Converge, view.Blocker)
	}

	view = convergeFixture(t, &controller.ConvergeFacts{Plan: plan}, executor.ModeGitHub, controller.VerificationGitHubProtectedMain)
	if view.Blocker == nil || view.Blocker.Reason != controller.BlockerReasonModeMismatch {
		t.Fatalf("mode mismatch blocker = %+v", view.Blocker)
	}

	view = convergeFixture(t, &controller.ConvergeFacts{Trigger: controller.TriggerHistoryRewritten, TriggerReason: "diverged"}, executor.ModeController, "")
	if view.Blocker == nil || view.Blocker.Kind != controller.BlockerKindTrigger || view.Blocker.Reason != controller.BlockerReasonHistoryRewritten || !strings.Contains(view.Blocker.Message, "revert on main") {
		t.Fatalf("history_rewritten blocker = %+v", view.Blocker)
	}

	// A facts failure fails the read closed instead of reporting a view that
	// hides the approval state.
	backend := newFakeResourceBackend()
	backend.EnsureResource(context.Background(), "staging", resTargetID)
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
	routes.SetConvergeFacts(&fakeFacts{err: errors.New("db down")})
	rec := fleetTargetDo(modeRouter(routes, readerPrincipal()), http.MethodGet, resourcePath("staging"), readerPrincipal(), "", nil)
	if rec.Code != http.StatusServiceUnavailable || problemCode(rec) != "fleet_converge_facts_failed" || strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("facts failure: %d %s", rec.Code, rec.Body)
	}
}
