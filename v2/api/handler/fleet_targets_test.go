package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/store"
)

// pgFleetTargetRoutes and etcdFleetTargetRoutes are the routes main.go and
// etcd_fleet_targets.go register, verbatim: {method, path, handler} (and the
// etcd auth middleware variable as a fourth column). requireRoutesRegistered
// fails the conformance suites if production stops registering any of them.
var pgFleetTargetRoutes = [][3]string{
	{"Post", "/fleet/targets", "Register"},
	{"Post", "/fleet/targets/abandon-plan", "AbandonPlan"},
	{"Get", "/fleet/targets/{targetID}", "Get"},
	{"Get", "/fleet/targets/{targetID}/fence", "Fence"},
	{"Post", "/fleet/targets/{targetID}/fence/release", "Release"},
}

var etcdFleetTargetRoutes = [][4]string{
	{"Post", "/api/v1/fleet/targets", "Register", "targetRegister"},
	{"Post", "/api/v1/fleet/targets/abandon-plan", "AbandonPlan", "targetAdmin"},
	{"Get", "/api/v1/fleet/targets/{targetID}", "Get", "targetRead"},
	{"Get", "/api/v1/fleet/targets/{targetID}/fence", "Fence", "targetRead"},
	{"Post", "/api/v1/fleet/targets/{targetID}/fence/release", "Release", "targetAdmin"},
}

// mountFleetTargetRoutes mounts the shared routes under /api/v1 on router.
func mountFleetTargetRoutes(router chi.Router, routes *FleetTargetRoutes) {
	router.Post("/api/v1/fleet/targets", routes.Register)
	router.Post("/api/v1/fleet/targets/abandon-plan", routes.AbandonPlan)
	router.Get("/api/v1/fleet/targets/{targetID}", routes.Get)
	router.Get("/api/v1/fleet/targets/{targetID}/fence", routes.Fence)
	router.Post("/api/v1/fleet/targets/{targetID}/fence/release", routes.Release)
}

func fleetTargetPrincipal(scopes ...string) *AccessPrincipal {
	return &AccessPrincipal{Subject: "operator", TokenID: "token-fleet-target", DeviceID: "device-fleet-target", Source: AccessPrincipalSourceManagedToken, Scopes: scopes}
}

func fleetTargetDo(router http.Handler, method, path string, principal *AccessPrincipal, key string, body interface{}) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	switch value := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, _ := json.Marshal(value)
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if principal != nil {
		req = WithAccessPrincipal(req, principal)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func fleetTargetRegisterResp(router http.Handler, identity lifecycle.TargetIdentity, aliases []string) fleettest.TargetResp {
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), uuid.NewString(), map[string]interface{}{
		"provider": identity.Provider, "providerAccount": identity.ProviderAccount, "stateBackend": identity.StateBackend, "aliases": aliases,
	})
	if rec.Code >= http.StatusBadRequest {
		return fleettest.TargetResp{HTTPStatus: rec.Code, Code: decodeProblemCode(rec)}
	}
	var operation struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &operation)
	return fleettest.TargetResp{HTTPStatus: rec.Code, TargetID: operation.Ref}
}

func fleetTargetReleaseResp(router http.Handler, targetID string, mode lifecycle.ReleaseMode, expectedGeneration int64) fleettest.Resp {
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/"+targetID+"/fence/release", fleetTargetPrincipal(ScopeAdmin), uuid.NewString(), map[string]interface{}{
		"mode": string(mode), "expectedGeneration": expectedGeneration, "reason": "conformance",
	})
	return conformanceProblemResp(rec)
}

func fleetTargetAbandonPlanResp(router http.Handler, planID string) fleettest.Resp {
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", fleetTargetPrincipal(ScopeAdmin), uuid.NewString(), map[string]interface{}{
		"planId": planID, "reason": "conformance",
	})
	return conformanceProblemResp(rec)
}

// --- backend-free route tests: a fake backend, store and observer ---

type fakeTargetOperations struct {
	mu        sync.Mutex
	accepted  map[string]store.OperationAcceptance
	last      store.OperationAcceptance
	acceptErr error
	accepts   int
}

func (f *fakeTargetOperations) Authority(context.Context) (string, error) {
	return "4f0d9f0e-8c4d-4c5b-9a5c-0d6f7d2f2b11", nil
}

func (f *fakeTargetOperations) Accept(_ context.Context, acceptance store.OperationAcceptance) (store.AcceptedOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return store.AcceptedOperation{}, f.acceptErr
	}
	f.accepts++
	f.last = acceptance
	if f.accepted == nil {
		f.accepted = map[string]store.OperationAcceptance{}
	}
	f.accepted[acceptance.Identity.Key] = acceptance
	return store.AcceptedOperation{Operation: acceptance.Operation}, nil
}

func (f *fakeTargetOperations) Resolve(_ context.Context, identity store.OperationRequestIdentity, fingerprint store.RequestFingerprint) (store.AcceptedOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prior, ok := f.accepted[identity.Key]
	if !ok {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: identity}
	}
	if prior.Fingerprint != fingerprint {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	return store.AcceptedOperation{Operation: prior.Operation, Replayed: true}, nil
}

type fakeTargetBackend struct {
	operations *fakeTargetOperations
	target     *FleetTargetRecord
	fence      FleetTargetFenceView
	binding    FleetTargetHolderBinding
	plans      map[string]bool
}

func (b *fakeTargetBackend) OperationStore() store.OperationStore { return b.operations }
func (b *fakeTargetBackend) Actor(http.ResponseWriter, *http.Request, AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	return store.OperationActor{Issuer: "norn://managed-token", Subject: "token-fleet-target"}, store.AcceptanceAuditContext{Source: "test"}, true
}
func (b *fakeTargetBackend) GetTarget(_ context.Context, id string) (*FleetTargetRecord, error) {
	if b.target != nil && b.target.TargetID == id {
		return b.target, nil
	}
	return nil, nil
}
func (b *fakeTargetBackend) FenceView(context.Context, string, time.Time) (FleetTargetFenceView, error) {
	return b.fence, nil
}
func (b *fakeTargetBackend) PlanExists(_ context.Context, id string) (bool, error) {
	return b.plans[id], nil
}
func (b *fakeTargetBackend) HolderBinding(context.Context, string) (FleetTargetHolderBinding, error) {
	return b.binding, nil
}

type fakeTargetObserver struct {
	mu        sync.Mutex
	applyRuns []string
	recovered []string
	listings  int
	completed map[string]bool
	listErr   error
}

func (o *fakeTargetObserver) status(runID, runAttempt int64) *githubapp.ApplyRunObservation {
	status := "in_progress"
	if o.completed[runKeyString(runID, runAttempt)] {
		status = "completed"
	}
	return &githubapp.ApplyRunObservation{RunID: runID, RunAttempt: runAttempt, Status: status}
}

func runKeyString(runID, runAttempt int64) string {
	return strings.Join([]string{itoa(runID), itoa(runAttempt)}, ":")
}

func itoa(v int64) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

func (o *fakeTargetObserver) ObserveApplyRunByNonceHash(_ context.Context, _, _ string, approved *githubapp.Dispatch, _ string, runAttempt int64) (*githubapp.ApplyRunObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.applyRuns = append(o.applyRuns, runKeyString(approved.RunID, runAttempt))
	return o.status(approved.RunID, runAttempt), nil
}

func (o *fakeTargetObserver) ObserveRecoverRun(_ context.Context, _ int64, _ string, runID, runAttempt int64, _ string) (*githubapp.ApplyRunObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recovered = append(o.recovered, runKeyString(runID, runAttempt))
	return o.status(runID, runAttempt), nil
}

func (o *fakeTargetObserver) ListPlanRuns(context.Context, string) ([]githubapp.RunSummary, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.listings++
	return nil, o.listErr
}

func (o *fakeTargetObserver) calls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.applyRuns) + len(o.recovered) + o.listings
}

const fakeTargetID = "tgt_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const fakeHolderPlanID = "7d9b3a52-1f0e-4b7e-8b6a-3c5d2e1f4a60"

func newFakeTargetRouter(t *testing.T) (http.Handler, *fakeTargetBackend, *fakeTargetObserver) {
	t.Helper()
	backend := &fakeTargetBackend{
		operations: &fakeTargetOperations{},
		target:     &FleetTargetRecord{TargetID: fakeTargetID, Aliases: []string{"cluster:fleet"}},
		fence:      FleetTargetFenceView{TargetID: fakeTargetID, Generation: 3, Held: true, HolderPlanID: fakeHolderPlanID, Occupancy: lifecycle.OccupancyUncertain},
		plans:      map[string]bool{fakeHolderPlanID: true},
	}
	observer := &fakeTargetObserver{completed: map[string]bool{}}
	router := chi.NewRouter()
	mountFleetTargetRoutes(router, NewFleetTargetRoutes(backend, observer))
	return router, backend, observer
}

func TestRegisterFleetTargetRequiresPlatformOperate(t *testing.T) {
	router, backend, observer := newFakeTargetRouter(t)
	body := map[string]interface{}{"provider": "digitalocean", "providerAccount": "acct", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:fleet"}}
	for name, principal := range map[string]*AccessPrincipal{
		"api-write only": fleetTargetPrincipal(ScopeAPIWrite),
		"fleet-operate":  fleetTargetPrincipal(ScopeFleetOperate),
		"ci workload":    {Subject: "runner", TokenID: "t", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopePlatformOperate}, CI: &CIIdentity{RunID: "1"}},
	} {
		if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", principal, "k-"+name, body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", nil, "k-anon", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status=%d", rec.Code)
	}
	if backend.operations.accepts != 0 || observer.calls() != 0 {
		t.Fatalf("a refused scope reached accept/observer: accepts=%d calls=%d", backend.operations.accepts, observer.calls())
	}
	// The body is intent only: proof or snapshot fields are unknown fields.
	withProof := map[string]interface{}{"provider": "digitalocean", "providerAccount": "acct", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:fleet"}, "listingSnapshotSha256": strings.Repeat("a", 64)}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), "k-proof", withProof); rec.Code != http.StatusBadRequest {
		t.Fatalf("proof field accepted: status=%d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), "", body); rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != "invalid_idempotency_key" {
		t.Fatalf("missing key: status=%d", rec.Code)
	}
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), "k-ok", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("platform:operate register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	last := backend.operations.last
	if last.FleetTargetReleaseEvidence != nil || last.FleetTargetMutation == nil || last.FleetTargetMutation.Kind != store.FleetTargetMutationRegister {
		t.Fatalf("register acceptance is wrong: %+v", last)
	}
	if got := strings.Join(last.Audit.Scopes, ","); got != ScopePlatformOperate {
		t.Fatalf("audit scopes = %q", got)
	}
	if replay := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopeAdmin), "k-ok", body); replay.Code != http.StatusOK {
		t.Fatalf("replay: status=%d", replay.Code)
	}
	if backend.operations.accepts != 1 {
		t.Fatalf("replay accepted again: %d", backend.operations.accepts)
	}
	changed := map[string]interface{}{"provider": "digitalocean", "providerAccount": "other", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:fleet"}}
	if conflict := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), "k-ok", changed); conflict.Code != http.StatusConflict {
		t.Fatalf("key reuse with another body: status=%d", conflict.Code)
	}
}

func TestReleaseRequiresAdminAndExpectedGeneration(t *testing.T) {
	router, backend, observer := newFakeTargetRouter(t)
	path := "/api/v1/fleet/targets/" + fakeTargetID + "/fence/release"
	body := map[string]interface{}{"mode": "abandon", "expectedGeneration": 3, "reason": "stuck"}
	for name, principal := range map[string]*AccessPrincipal{
		"platform-operate": fleetTargetPrincipal(ScopePlatformOperate),
		"api-write":        fleetTargetPrincipal(ScopeAPIWrite),
		"fleet-operate":    fleetTargetPrincipal(ScopeFleetOperate),
	} {
		if rec := fleetTargetDo(router, http.MethodPost, path, principal, "k-"+name, body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status=%d", name, rec.Code)
		}
	}
	abandonPlan := map[string]interface{}{"planId": fakeHolderPlanID, "reason": "stuck"}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", fleetTargetPrincipal(ScopePlatformOperate), "k-ap", abandonPlan); rec.Code != http.StatusForbidden {
		t.Fatalf("abandon-plan without admin: status=%d", rec.Code)
	}
	if backend.operations.accepts != 0 || observer.calls() != 0 {
		t.Fatalf("a refused scope reached accept/observer: accepts=%d calls=%d", backend.operations.accepts, observer.calls())
	}
	admin := fleetTargetPrincipal(ScopeAdmin)
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "k-nogen", map[string]interface{}{"mode": "abandon"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing expectedGeneration: status=%d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, path, admin, "k-proof", map[string]interface{}{"mode": "terminal", "expectedGeneration": 3, "boundApplyRunCompleted": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("client proof accepted: status=%d", rec.Code)
	}
	stale := fleetTargetDo(router, http.MethodPost, path, admin, "k-stale", map[string]interface{}{"mode": "terminal", "expectedGeneration": 2})
	if stale.Code != http.StatusConflict || decodeProblemCode(stale) != store.CodeFleetTargetExpectedGenerationMismatch {
		t.Fatalf("stale generation: status=%d code=%s", stale.Code, decodeProblemCode(stale))
	}
	if observer.calls() != 0 || backend.operations.accepts != 0 {
		t.Fatal("a stale expectedGeneration must not reach GitHub or the store")
	}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/tgt_"+strings.Repeat("f", 64)+"/fence/release", admin, "k-unknown", body); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown target: status=%d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/not-a-target/fence/release", admin, "k-bad", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad target id: status=%d", rec.Code)
	}
	rec := fleetTargetDo(router, http.MethodPost, path, admin, "k-ok", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin abandon release: status=%d body=%s", rec.Code, rec.Body.String())
	}
	last := backend.operations.last
	if last.Identity.Kind != store.FleetTargetFenceReleaseOperationKind || last.FleetTargetReleaseEvidence == nil || last.FleetTargetReleaseEvidence.BoundApplyRunCompleted ||
		len(last.FleetTargetReleaseEvidence.CompletedRunnerAttemptIDs) != 0 || len(last.FleetTargetReleaseEvidence.ListingSnapshotSHA256) != 64 {
		t.Fatalf("abandon evidence must be the server listing digest only: %+v", last.FleetTargetReleaseEvidence)
	}
	// An empty listing is a recorded observation: sha256 of "[]".
	sum := sha256.Sum256([]byte("[]"))
	if last.FleetTargetReleaseEvidence.ListingSnapshotSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("listing digest = %s", last.FleetTargetReleaseEvidence.ListingSnapshotSHA256)
	}
	if observer.listings != 1 || len(observer.applyRuns) != 0 || len(observer.recovered) != 0 {
		t.Fatalf("abandon must only list: %+v", observer)
	}
}

func TestTerminalReleaseObservesEveryRun(t *testing.T) {
	router, backend, observer := newFakeTargetRouter(t)
	const boundRun, recoverA, recoverB = 100, 200, 300
	backend.binding = FleetTargetHolderBinding{
		FleetEnvironment: "production/nyc3", NonceSHA256: strings.Repeat("b", 64), BoundRunAttempt: 1,
		Dispatch: githubapp.Dispatch{RunID: boundRun, PlanRunID: 1, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: strings.Repeat("d", 40)},
		Attempts: []fleet.RunnerAttempt{
			{RunnerAttemptID: "github-actions:acme/fleet:100:1", CommitSHA: strings.Repeat("e", 40)},
			{RunnerAttemptID: "github-actions:acme/fleet:200:1", CommitSHA: strings.Repeat("e", 40)},
			{RunnerAttemptID: "github-actions:acme/fleet:200:1", CommitSHA: strings.Repeat("e", 40)}, // duplicate
			{RunnerAttemptID: "github-actions:acme/fleet:300:2", CommitSHA: strings.Repeat("e", 40)},
		},
	}
	observer.completed[runKeyString(boundRun, 1)] = true
	observer.completed[runKeyString(recoverA, 1)] = true
	// recoverB is still in progress and must not appear in the proof.
	path := "/api/v1/fleet/targets/" + fakeTargetID + "/fence/release"
	rec := fleetTargetDo(router, http.MethodPost, path, fleetTargetPrincipal(ScopeAdmin), "k-terminal", map[string]interface{}{"mode": "terminal", "expectedGeneration": 3})
	if rec.Code != http.StatusCreated {
		t.Fatalf("terminal release: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(observer.applyRuns, ","); got != "100:1" {
		t.Fatalf("apply observations = %s", got)
	}
	if got := strings.Join(observer.recovered, ","); got != "200:1,300:2" {
		t.Fatalf("every distinct recover run must be observed once: %s", got)
	}
	evidence := backend.operations.last.FleetTargetReleaseEvidence
	if evidence == nil || !evidence.BoundApplyRunCompleted || strings.Join(evidence.CompletedRunnerAttemptIDs, ",") != "github-actions:acme/fleet:100:1,github-actions:acme/fleet:200:1" {
		t.Fatalf("evidence = %+v", evidence)
	}
	before := observer.calls()
	if replay := fleetTargetDo(router, http.MethodPost, path, fleetTargetPrincipal(ScopeAdmin), "k-terminal", map[string]interface{}{"mode": "terminal", "expectedGeneration": 3}); replay.Code != http.StatusOK {
		t.Fatalf("replay: status=%d", replay.Code)
	}
	if observer.calls() != before {
		t.Fatal("a replay must skip GitHub")
	}
	// A rerun of the bound run: the bound proof observes its latest attempt
	// (the store's dispatch attempt, raised to the highest attempt any runner
	// recorded), and that completion proves the superseded attempt too.
	backend.binding.BoundRunAttempt = 1
	backend.binding.Attempts = []fleet.RunnerAttempt{
		{RunnerAttemptID: "github-actions:acme/fleet:100:1"}, {RunnerAttemptID: "github-actions:acme/fleet:100:2"},
	}
	observer.completed[runKeyString(boundRun, 2)] = true
	observer.applyRuns = nil
	if rec := fleetTargetDo(router, http.MethodPost, path, fleetTargetPrincipal(ScopeAdmin), "k-rerun", map[string]interface{}{"mode": "terminal", "expectedGeneration": 3}); rec.Code != http.StatusCreated {
		t.Fatalf("terminal release after a rerun: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(observer.applyRuns, ","); got != "100:2" {
		t.Fatalf("rerun apply observations = %s", got)
	}
	evidence = backend.operations.last.FleetTargetReleaseEvidence
	if evidence == nil || !evidence.BoundApplyRunCompleted || strings.Join(evidence.CompletedRunnerAttemptIDs, ",") != "github-actions:acme/fleet:100:1,github-actions:acme/fleet:100:2" {
		t.Fatalf("rerun evidence = %+v", evidence)
	}
	// Without a GitHub App the release cannot gather proof.
	noGitHub := chi.NewRouter()
	mountFleetTargetRoutes(noGitHub, NewFleetTargetRoutes(backend, nil))
	if rec := fleetTargetDo(noGitHub, http.MethodPost, path, fleetTargetPrincipal(ScopeAdmin), "k-nogh", map[string]interface{}{"mode": "terminal", "expectedGeneration": 3}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no observer: status=%d", rec.Code)
	}
}

func TestAbandonRequiresMinimumAge(t *testing.T) {
	router, backend, observer := newFakeTargetRouter(t)
	admin := fleetTargetPrincipal(ScopeAdmin)
	body := map[string]interface{}{"planId": fakeHolderPlanID, "reason": "stuck"}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "k-bad", map[string]interface{}{"planId": "not-a-uuid"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad plan id: status=%d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "k-none", map[string]interface{}{"planId": uuid.NewString()}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: status=%d", rec.Code)
	}
	// The age rule lives in the store (lifecycle.DecideRelease); the route
	// must surface its stable code as 409 and leave no receipt.
	backend.operations.acceptErr = lifecycle.ErrFleetTargetAbandonTooSoon
	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "k-soon", body)
	if rec.Code != http.StatusConflict || decodeProblemCode(rec) != lifecycle.CodeFleetTargetAbandonTooSoon {
		t.Fatalf("too soon: status=%d code=%s", rec.Code, decodeProblemCode(rec))
	}
	backend.operations.acceptErr = nil
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "k-soon", body); rec.Code != http.StatusCreated {
		t.Fatalf("abandon-plan: status=%d body=%s", rec.Code, rec.Body.String())
	}
	evidence := backend.operations.last.FleetTargetReleaseEvidence
	if backend.operations.last.Identity.Kind != store.FleetTargetAbandonPlanOperationKind || evidence == nil || evidence.ListingSnapshotSHA256 == "" || evidence.BoundApplyRunCompleted {
		t.Fatalf("abandon-plan acceptance: %+v", backend.operations.last)
	}
	observer.listErr = errors.New("github down")
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "k-gh", body); rec.Code != http.StatusBadGateway {
		t.Fatalf("listing failure: status=%d", rec.Code)
	}
}

func TestFleetTargetFenceErrorsMapToStableStatuses(t *testing.T) {
	codes := []string{
		lifecycle.CodeFleetTargetExecutionOccupied, lifecycle.CodeFleetTargetUnregistered, lifecycle.CodeFleetTargetAliasConflict,
		lifecycle.CodeFleetTargetAuthoritySuperseded, lifecycle.CodeFleetPlanRevalidationRequired, lifecycle.CodeFleetTargetHolderAbandoned,
		lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource, lifecycle.CodeFleetTargetRegistrationInFlight,
		lifecycle.CodeFleetTargetFenceNotHeld, lifecycle.CodeFleetTargetHasLiveAttempt, lifecycle.CodeFleetTargetTerminalProofIncomplete,
		lifecycle.CodeFleetTargetAbandonTooSoon, lifecycle.CodeFleetTargetAbandonSnapshotRequired, lifecycle.CodeFleetTargetReleaseEvidenceMismatch,
		store.CodeFleetTargetExpectedGenerationMismatch,
	}
	router, backend, _ := newFakeTargetRouter(t)
	for _, code := range codes {
		backend.operations.acceptErr = &lifecycle.FenceError{Code: code, Reason: "refused"}
		rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets", fleetTargetPrincipal(ScopePlatformOperate), "k-"+code, map[string]interface{}{
			"provider": "digitalocean", "providerAccount": "acct", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:fleet"}})
		if rec.Code != http.StatusConflict || decodeProblemCode(rec) != code {
			t.Fatalf("%s: status=%d code=%s", code, rec.Code, decodeProblemCode(rec))
		}
		if FleetTargetFenceStatus(code) != http.StatusConflict {
			t.Fatalf("%s is not mapped to 409", code)
		}
	}
}

func TestFleetTargetReadRoutesRequireAPIRead(t *testing.T) {
	router, _, _ := newFakeTargetRouter(t)
	for _, path := range []string{"/api/v1/fleet/targets/" + fakeTargetID, "/api/v1/fleet/targets/" + fakeTargetID + "/fence"} {
		if rec := fleetTargetDo(router, http.MethodGet, path, fleetTargetPrincipal(ScopeFleetOperate), "", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without api:read: status=%d", path, rec.Code)
		}
		rec := fleetTargetDo(router, http.MethodGet, path, fleetTargetPrincipal(ScopeAPIRead), "", nil)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "holderNonce") {
			t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := fleetTargetDo(router, http.MethodGet, "/api/v1/fleet/targets/tgt_"+strings.Repeat("f", 64), fleetTargetPrincipal(ScopeAPIRead), "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown target: status=%d", rec.Code)
	}
}

func TestAttemptsMatcherNarrowedToPlans(t *testing.T) {
	for _, path := range []string{
		"/api/v1/fleet/plans/p/attempts", "/api/v1/fleet/plans/p/attempts/a", "/api/v1/fleet/plans/p/attempts/a/heartbeat",
		"/api/v1/fleet/plans/p/attempts/a/advance", "/api/v1/fleet/plans/p/attempts/a/cancel", "/api/v1/fleet/plans/p/reconciliations",
	} {
		if !FleetPlanRunnerScopedPath(path) {
			t.Fatalf("%s must keep deferring to the runner handler", path)
		}
	}
	for _, path := range []string{
		"/api/v1/fleet/resources/attempts-prod/desired", "/api/v1/fleet/resources/attempts/observations",
		"/api/v1/fleet/targets/attempts-x/fence/release", "/api/v1/fleet/resources/x/reconciliations",
		"/api/v1/apps/attempts/releases",
		// Exactness (M17): suffixes, empty/dot segments, trailing slashes and
		// extra (for example %2F-decoded) segments never defer.
		"/api/v1/fleet/plans/x/attempts-foo", "/api/v1/fleet/plans/x/attemptsfoo/a/advance",
		"/api/v1/fleet/plans/x/attempts/", "/api/v1/fleet/plans/x/reconciliations/", "/api/v1/fleet/plans//attempts",
		"/api/v1/fleet/plans/../attempts", "/api/v1/fleet/plans/x/../attempts", "/api/v1/fleet/plans/./attempts",
		"/api/v1/fleet/plans/a/b/attempts", "/api/v1/fleet/plans/a/attempts/b/c/advance", "/api/v1/fleet/plans/x/attempts/a/delete",
		"/api/v1/fleet/plans/x/github/reconciliations", "/api/v1/fleet/plans/attempts/github/dispatch",
		"/api/v1/fleet/plans/x/attempts/a/advance/reconciliations", "/api/v1/fleet/node-pools/attempts/plan",
		"/api/v1/fleet/plans/x/reconciliations-foo", "/api/v1/fleet/plans/x%2Fattempts", "/api/v1/fleet/plans",
	} {
		if FleetPlanRunnerScopedPath(path) {
			t.Fatalf("%s must not skip the generic scope check", path)
		}
	}
}
