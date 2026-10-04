package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestFleetLifecycleConformanceEtcd drives EtcdFleetRunnerHandler (the fake
// GitHub observer pattern from TestEtcdFleetRunnerHTTPAdmissionAndEvidenceGate)
// through httptest against real etcd, so the same lifecycle conformance cases
// that run against the live PG legacy path also run against the live etcd
// path (plan.md WP6, fixing plan-review.md B3).
func TestFleetLifecycleConformanceEtcd(t *testing.T) {
	client, prefix := integrationtest.Etcd(t)
	requireRoutesRegistered(t, "../etcd_fleet_runtime.go", etcdFleetRunnerRoutes, func(route [3]string) string {
		return fmt.Sprintf("router.With(runnerAuth).%s(%q, fleetRunner.%s)", route[0], route[1], route[2])
	})
	h := newEtcdLifecycleHarness(t, client, prefix)
	fleettest.RunFleetLifecycleConformance(t, h)
}

// etcdFleetRunnerRoutes is {method, path, EtcdFleetRunnerHandler method} for
// every runner route etcd_fleet_runtime.go registers; requireRoutesRegistered
// fails the test if production stops registering any of them verbatim.
var etcdFleetRunnerRoutes = [][3]string{
	{"Get", "/api/v1/fleet/plans/{planID}/attempts", "List"},
	{"Post", "/api/v1/fleet/plans/{planID}/attempts", "Create"},
	{"Get", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}", "Get"},
	{"Post", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", "Heartbeat"},
	{"Post", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance", "Advance"},
	{"Post", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel", "Cancel"},
	{"Get", "/api/v1/fleet/plans/{planID}/reconciliations", "ListReconciliations"},
	{"Post", "/api/v1/fleet/plans/{planID}/reconciliations", "Reconcile"},
}

var etcdConformanceRunSeq atomic.Int64

func nextEtcdConformanceRunID() int64 {
	return 800_000_000 + etcdConformanceRunSeq.Add(1)
}

const etcdConformanceRepository = "acme/norn-fleet"
const etcdConformanceEnvironment = "staging/nyc3"

// etcdConformanceLane maps a fence-suite dispatch-lane label to a real lane.
// Production validation only accepts staging/nyc3 and production/nyc3, while
// the frozen suite uses free-form labels (AliasDoesNotBypass's
// "fence-bypass-env"). The suite needs at most one non-default lane per case,
// so "" maps to the default staging lane and any other label maps to
// production/nyc3, in both SeedPlanOnCluster and RegisterTarget's
// "environment:" aliases.
func etcdConformanceLane(label string) string {
	if label == "" {
		return etcdConformanceEnvironment
	}
	return "production/nyc3"
}

type etcdConformanceDispatch struct {
	runID           int64
	rawNonce        string
	approvedHeadSHA string
	planSHA256      string
}

type etcdLifecycleHarness struct {
	t            *testing.T
	operations   *etcdstore.V3OperationStore
	fleetHandler *EtcdFleetRunnerHandler
	router       chi.Router
	auditKey     string
	authority    string
	dispatch     map[string]*etcdConformanceDispatch
	planDigest   map[string]string
	ciForAttempt map[string]*CIIdentity

	// fenceFixtures and runTerminal back only the FenceHarness methods below
	// (WP8b); the lifecycle conformance suite above never touches them.
	fenceFixtures map[string]*etcdFenceFixture
	runTerminal   map[string]bool
}

// etcdFenceFixture is the per-plan bookkeeping the FenceHarness methods need
// that the lifecycle harness above has no reason to track: the cluster and
// dispatch-lane environment SeedPlanOnCluster chose, and the dispatch nonce
// hash and bound run ID once Dispatch has acquired the fence.
type etcdFenceFixture struct {
	cluster, environment        string
	nonceSHA256                 string
	runID                       int64
	approvedHeadSHA, planSHA256 string
}

// etcdConformanceSourceDigest is the fixed SourceDigest every seeded capacity
// plan carries, so SeedDispatch's signed dispatch payload can bind it without
// tracking it per plan.
var etcdConformanceSourceDigest = "sha256:" + strings.Repeat("1", 64)

// var _ fleettest.FenceHarness asserts at compile time that the stub methods
// below satisfy FenceHarness, even though WP7 does not call
// RunFleetFenceConformance here (WP8b does).
var _ fleettest.FenceHarness = (*etcdLifecycleHarness)(nil)

func newEtcdLifecycleHarness(t *testing.T, client *clientv3.Client, prefix string) *etcdLifecycleHarness {
	t.Helper()
	const auditKey = "norn-fleet-lifecycle-conformance-audit-signing-key"
	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuditSigningKey: auditKey, ControlAuthority: authority}
	fleetHandler := NewEtcdFleetRunnerHandler(cfg, operations)
	// etcd_fleet_runtime.go (package main) mounts these behind
	// etcdManagedTokenAuth; the harness injects the principal it would attach.
	handlers := map[string]http.HandlerFunc{
		"List": fleetHandler.List, "Create": fleetHandler.Create, "Get": fleetHandler.Get, "Heartbeat": fleetHandler.Heartbeat,
		"Advance": fleetHandler.Advance, "Cancel": fleetHandler.Cancel, "ListReconciliations": fleetHandler.ListReconciliations, "Reconcile": fleetHandler.Reconcile,
	}
	router := chi.NewRouter()
	for _, route := range etcdFleetRunnerRoutes {
		router.MethodFunc(strings.ToUpper(route[0]), route[1], handlers[route[2]])
	}
	return &etcdLifecycleHarness{
		t: t, operations: operations, fleetHandler: fleetHandler, router: router,
		auditKey: auditKey, authority: authority,
		dispatch: map[string]*etcdConformanceDispatch{}, planDigest: map[string]string{}, ciForAttempt: map[string]*CIIdentity{},
		fenceFixtures: map[string]*etcdFenceFixture{}, runTerminal: map[string]bool{},
	}
}

func (e *etcdLifecycleHarness) Profile() lifecycle.Profile { return lifecycle.V3 }

func (e *etcdLifecycleHarness) signCapacityPlan(plan *fleet.CapacityPlan) {
	canonical, err := canonicalCapacityPlan(plan)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(e.auditKey))
	_, _ = mac.Write([]byte(plan.Digest))
	plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func (e *etcdLifecycleHarness) SeedPlan(action string) fleettest.PlanRef {
	e.t.Helper()
	return e.seedPlanWithCluster(action, "fleet")
}

// SeedPlanOnCluster (FenceHarness; WP8b) seeds exactly like SeedPlan except
// the plan's cluster is caller-chosen, and it records cluster/environment in
// fenceFixtures so Dispatch can resolve the same target the real acquire
// step will.
func (e *etcdLifecycleHarness) SeedPlanOnCluster(action, cluster, environment string) fleettest.PlanRef {
	e.t.Helper()
	plan := e.seedPlanWithCluster(action, cluster)
	e.fenceFixtures[plan.ID] = &etcdFenceFixture{cluster: cluster, environment: etcdConformanceLane(environment)}
	return plan
}

func (e *etcdLifecycleHarness) seedPlanWithCluster(action, cluster string) fleettest.PlanRef {
	e.t.Helper()
	ctx := context.Background()
	planID := uuid.NewString()
	plan := fleet.CapacityPlan{
		SchemaVersion: "norn.fleet-capacity-plan/v1", ID: planID, Cluster: cluster, Pool: "workers",
		Current: fleet.NodePool{Desired: 3}, Proposed: fleet.NodePool{Desired: 3}, Action: action,
		SourceDigest: etcdConformanceSourceDigest,
	}
	e.signCapacityPlan(&plan)
	e.planDigest[planID] = plan.Digest
	encodedPlan, _ := json.Marshal(plan)
	var payload map[string]interface{}
	_ = json.Unmarshal(encodedPlan, &payload)
	now := time.Now().UTC()
	finished := now
	planOperation := model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "workers", Status: model.OperationSucceeded, Source: "test", Risk: "plan",
		Payload: payload, Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1,
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: e.authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: planOperation.Kind, Resource: planOperation.Ref, Key: "plan-" + planID},
		Operation: planOperation, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"},
	}
	var err error
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.operations.Accept(ctx, acceptance); err != nil {
		e.t.Fatal(err)
	}
	return fleettest.PlanRef{ID: planID, RequiresDrain: false}
}

func (e *etcdLifecycleHarness) SeedDispatch(plan fleettest.PlanRef) fleettest.DispatchRef {
	e.t.Helper()
	ctx := context.Background()
	runID := nextEtcdConformanceRunID()
	approvedHeadSHA := strings.Repeat("c", 40)
	planSHA256 := strings.Repeat("b", 64)
	dispatchPayload := map[string]interface{}{
		"planId": plan.ID, "planDigest": e.planDigest[plan.ID], "sourceDigest": etcdConformanceSourceDigest,
		"planRunId": int64(1), "planSha256": planSHA256, "approvedHeadSha": approvedHeadSHA,
		"fleetEnvironment": etcdConformanceEnvironment, "allowDestructive": false,
	}
	dispatchOperation := model.Operation{
		ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test",
		Payload: map[string]interface{}{"fleetGitHub": dispatchPayload}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1,
	}
	dispatchAcceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: e.authority, Actor: store.OperationActor{Issuer: e.authority + "/fleet-github", Subject: plan.ID}, Kind: dispatchOperation.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"},
		Operation: dispatchOperation, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": dispatchPayload},
	}
	var err error
	dispatchAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(dispatchAcceptance)
	if err != nil {
		e.t.Fatal(err)
	}
	_, prepared, err := e.operations.AcceptFleetGitHubDispatch(ctx, dispatchAcceptance, etcdstore.FleetGitHubDispatchPreparation{
		PlanID: plan.ID, PlanRunID: 1, PlanSHA256: planSHA256, ApprovedHeadSHA: approvedHeadSHA, FleetEnvironment: etcdConformanceEnvironment,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.operations.FinishFleetGitHubDispatch(ctx, plan.ID, prepared.DispatchNonceSHA256, runID, fmt.Sprintf("https://github.com/%s/actions/runs/%d", etcdConformanceRepository, runID)); err != nil {
		e.t.Fatal(err)
	}
	e.dispatch[plan.ID] = &etcdConformanceDispatch{runID: runID, rawNonce: prepared.DispatchNonce, approvedHeadSHA: approvedHeadSHA, planSHA256: planSHA256}
	return fleettest.DispatchRef{RunID: runID, RunAttempt: 1, RawNonce: prepared.DispatchNonce, ApprovedHeadSHA: approvedHeadSHA, PlanSHA256: planSHA256}
}

func etcdConformanceCI(run fleettest.RunRef) *CIIdentity {
	return &CIIdentity{
		Provider: "github-actions", Repository: etcdConformanceRepository,
		RunID: strconv.FormatInt(run.RunID, 10), RunAttempt: strconv.Itoa(run.RunAttempt),
		SHA: run.SHA, Intent: run.Intent,
	}
}

func (e *etcdLifecycleHarness) principal(ci *CIIdentity) *AccessPrincipal {
	return &AccessPrincipal{Source: AccessPrincipalSourceManagedToken, TokenID: "token-" + ci.RunID, Scopes: []string{ScopeFleetOperate}, CI: ci}
}

func (e *etcdLifecycleHarness) startOrRecover(plan fleettest.PlanRef, run fleettest.RunRef, resume bool) fleettest.Resp {
	e.t.Helper()
	dispatch, ok := e.dispatch[plan.ID]
	if !ok {
		e.t.Fatalf("plan %s has no seeded dispatch", plan.ID)
	}
	ci := etcdConformanceCI(run)
	req := fleet.RunnerAttemptCreateRequest{
		SchemaVersion: fleet.RunnerAttemptSchemaVersion, RunnerAttemptID: canonicalRunnerAttemptID(ci),
		CommitSHA: run.SHA, PlanSHA256: dispatch.planSHA256, WorkflowURL: canonicalWorkflowRunURL(ci.Repository, ci.RunID),
		DispatchNonce: dispatch.rawNonce, SourceDispatchRunID: strconv.FormatInt(dispatch.runID, 10),
		Resume: resume, HeartbeatTimeoutSeconds: 30,
	}
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/attempts", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", uuid.NewString())
	httpReq = WithAccessPrincipal(httpReq, e.principal(ci))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httpReq)
	resp := etcdAttemptResp(rec)
	if resp.Attempt != nil {
		e.ciForAttempt[resp.Attempt.ID] = ci
	}
	return resp
}

func (e *etcdLifecycleHarness) Start(plan fleettest.PlanRef, run fleettest.RunRef) fleettest.Resp {
	e.fleetHandler.github = nil
	return e.startOrRecover(plan, run, false)
}

func (e *etcdLifecycleHarness) Recover(plan fleettest.PlanRef, run fleettest.RunRef, stop *fleettest.StopEvidence) fleettest.Resp {
	dispatch, ok := e.dispatch[plan.ID]
	if !ok {
		e.t.Fatalf("plan %s has no seeded dispatch", plan.ID)
	}
	if stop == nil {
		// D6: no server-observed predecessor stop proof is available.
		e.fleetHandler.github = nil
	} else {
		e.fleetHandler.github = fakeFleetApplyRunObserver{observation: &githubapp.ApplyRunObservation{
			RunID: dispatch.runID, RunAttempt: 1, Status: stop.Status, Conclusion: stop.Conclusion, ObservedAt: stop.ObservedAt,
		}}
	}
	return e.startOrRecover(plan, run, true)
}

func (e *etcdLifecycleHarness) ownerCI(attemptID string) *CIIdentity {
	ci, ok := e.ciForAttempt[attemptID]
	if !ok {
		e.t.Fatalf("no owning CI identity tracked for attempt %s", attemptID)
	}
	return ci
}

func (e *etcdLifecycleHarness) Heartbeat(attempt *fleet.RunnerAttempt, sequence, revision int64) fleettest.Resp {
	req := fleet.RunnerAttemptHeartbeatRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, Phase: attempt.CurrentPhase, Sequence: sequence, Revision: revision, Message: "alive"}
	return e.mutate(attempt, "heartbeat", req)
}

func (e *etcdLifecycleHarness) Advance(attempt *fleet.RunnerAttempt, expectedPhase string, revision int64) fleettest.Resp {
	req := fleet.RunnerAttemptAdvanceRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ExpectedPhase: expectedPhase, Revision: revision}
	return e.mutate(attempt, "advance", req)
}

func (e *etcdLifecycleHarness) Cancel(attempt *fleet.RunnerAttempt, revision int64, reason string) fleettest.Resp {
	req := fleet.RunnerAttemptCancelRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, Revision: revision, Reason: reason}
	return e.mutate(attempt, "cancel", req)
}

func (e *etcdLifecycleHarness) mutate(attempt *fleet.RunnerAttempt, verb string, body interface{}) fleettest.Resp {
	e.t.Helper()
	ci := e.ownerCI(attempt.ID)
	encoded, _ := json.Marshal(body)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+attempt.PlanID+"/attempts/"+attempt.ID+"/"+verb, bytes.NewReader(encoded))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq = WithAccessPrincipal(httpReq, e.principal(ci))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httpReq)
	return etcdAttemptResp(rec)
}

func (e *etcdLifecycleHarness) Checkpoint(attempt *fleet.RunnerAttempt, phase, status string) fleettest.Resp {
	ci := e.ownerCI(attempt.ID)
	req := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, AttemptID: attempt.ID, Phase: phase, Status: status,
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, StateSerial: 1,
		EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
	}
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+attempt.PlanID+"/reconciliations", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", "checkpoint:"+attempt.ID+":"+phase+":"+status)
	httpReq = WithAccessPrincipal(httpReq, e.principal(ci))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httpReq)
	return conformanceProblemResp(rec)
}

func (e *etcdLifecycleHarness) Get(attempt *fleet.RunnerAttempt) fleettest.Resp {
	ci := e.ownerCI(attempt.ID)
	httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/plans/"+attempt.PlanID+"/attempts/"+attempt.ID, nil)
	httpReq = WithAccessPrincipal(httpReq, e.principal(ci))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httpReq)
	return etcdAttemptResp(rec)
}

// ExpireAttempt waits out the attempt's (minimum, 30s) heartbeat lease. etcd
// only ever projects expiry (D8); there is no durable state to backdate from
// this package without reaching into etcdstore's private key encoding, and a
// real wait keeps this harness honest about what the read path actually does.
func (e *etcdLifecycleHarness) ExpireAttempt(*fleet.RunnerAttempt) {
	time.Sleep(31 * time.Second)
}

func etcdAttemptResp(rec *httptest.ResponseRecorder) fleettest.Resp {
	if rec.Code >= http.StatusBadRequest {
		return fleettest.Resp{HTTPStatus: rec.Code, Code: decodeProblemCode(rec)}
	}
	var attempt fleet.RunnerAttempt
	if err := json.Unmarshal(rec.Body.Bytes(), &attempt); err != nil {
		return fleettest.Resp{HTTPStatus: rec.Code}
	}
	return fleettest.Resp{HTTPStatus: rec.Code, Attempt: &attempt}
}

// The FenceHarness methods below are WP8b's real bodies. There is no signed
// `fleet.target.fence-release`/registration operation or HTTP route yet
// (those are WP9a/b, which land after WP8a/WP8b per plan.md §3's order), so
// RegisterTarget/Release/AbandonPlan call the storage-level transitions this
// WP adds directly (etcdstore/v3_fleet_fence.go), the same way SeedDispatch
// above already calls AcceptFleetGitHubDispatch/FinishFleetGitHubDispatch
// directly rather than through etcd_fleet_github_dispatch.go's HTTP handler
// (package main, which this test package cannot import). Dispatch exercises
// that same real acquire transition for real: it is the one FenceHarness
// method that must observe the fence's actual behavior, not a shortcut.

// fenceErrResp maps a fence-domain error to a backend-neutral Resp. Every
// refusal this WP's storage functions return is either a *lifecycle.FenceError
// (acquire/bind/evidence/release decisions) or a *store.FleetRunnerAttemptAdmissionError
// (the Create-path abandonment check, wrapped the same way every other bind
// refusal in that file is); anything else is an unexpected store failure.
func fenceErrResp(err error, successStatus int) fleettest.Resp {
	if err == nil {
		return fleettest.Resp{HTTPStatus: successStatus}
	}
	var fenceErr *lifecycle.FenceError
	if errors.As(err, &fenceErr) {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: fenceErr.Code}
	}
	var runnerAdmission *store.FleetRunnerAttemptAdmissionError
	if errors.As(err, &runnerAdmission) {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: runnerAdmission.Code}
	}
	return fleettest.Resp{HTTPStatus: http.StatusInternalServerError, Code: "fleet_target_fence_failed"}
}

// etcdConformanceListingSnapshotSHA256 stands in for the fake GitHub run
// listing snapshot digest a real abandon release stores as evidence
// (DecideRelease's ListingSnapshotSHA256).
var etcdConformanceListingSnapshotSHA256 = "sha256:" + strings.Repeat("9", 64)

func (e *etcdLifecycleHarness) RegisterTarget(identity lifecycle.TargetIdentity, aliases []string) fleettest.TargetResp {
	e.t.Helper()
	mapped := make([]string, len(aliases))
	for i, alias := range aliases {
		if kind, value, ok := lifecycle.ParseAlias(alias); ok && kind == lifecycle.AliasKindEnvironment {
			alias = kind + ":" + etcdConformanceLane(value)
		}
		mapped[i] = alias
	}
	target, err := e.operations.RegisterFleetTarget(context.Background(), identity, mapped, uuid.NewString())
	resp := fenceErrResp(err, http.StatusCreated)
	out := fleettest.TargetResp{HTTPStatus: resp.HTTPStatus, Code: resp.Code}
	if err == nil {
		out.TargetID = target.TargetID
	}
	return out
}

func runKey(runID int64, runAttempt int) string { return fmt.Sprintf("%d:%d", runID, runAttempt) }

// runIDAttemptFromRunnerID parses "<kind>:<repo>:<runID>:<runAttempt>"
// (canonicalRunnerAttemptID's shape) into its trailing run identity.
func runIDAttemptFromRunnerID(runnerID string) (int64, int, bool) {
	parts := strings.Split(strings.TrimSpace(runnerID), ":")
	if len(parts) < 2 {
		return 0, 0, false
	}
	runAttempt, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, 0, false
	}
	runID, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return runID, int(runAttempt), true
}

func (e *etcdLifecycleHarness) SetRunTerminal(run fleettest.RunRef, completed bool) {
	e.runTerminal[runKey(run.RunID, run.RunAttempt)] = completed
}

// buildTerminalProof assembles lifecycle.TerminalProof for a terminal
// release from the fake observer state SetRunTerminal configured: the bound
// apply run (dispatch, if any) plus every one of holderPlanID's attempts
// whose own (runID, runAttempt) was marked terminal.
func (e *etcdLifecycleHarness) buildTerminalProof(ctx context.Context, holderPlanID string) (*lifecycle.TerminalProof, error) {
	proof := &lifecycle.TerminalProof{CompletedRunnerAttemptIDs: map[string]bool{}}
	if dispatch, ok := e.dispatch[holderPlanID]; ok {
		proof.BoundApplyRunCompleted = e.runTerminal[runKey(dispatch.runID, 1)]
	}
	if holderPlanID == "" {
		return proof, nil
	}
	attempts, err := e.operations.ListFleetRunnerAttempts(ctx, holderPlanID)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		if runID, runAttempt, ok := runIDAttemptFromRunnerID(a.RunnerAttemptID); ok && e.runTerminal[runKey(runID, runAttempt)] {
			proof.CompletedRunnerAttemptIDs[a.RunnerAttemptID] = true
		}
	}
	return proof, nil
}

func (e *etcdLifecycleHarness) Dispatch(plan fleettest.PlanRef, startedAt time.Time, outcome fleettest.GitHubOutcome) (fleettest.Resp, fleettest.DispatchRef) {
	e.t.Helper()
	ctx := context.Background()
	if err := e.operations.SetFleetCapacityPlanStartedAt(ctx, plan.ID, startedAt); err != nil {
		e.t.Fatal(err)
	}
	fixture := e.fenceFixtures[plan.ID]
	if fixture == nil {
		e.t.Fatalf("plan %s was not seeded with SeedPlanOnCluster", plan.ID)
	}
	if fixture.runID != 0 {
		// Dispatch re-POST on an already-bound plan: etcd_fleet_github_dispatch.go's
		// "already bound" branch, which checks abandonment before replaying
		// the existing binding and never calls AcceptFleetGitHubDispatch again.
		if abandoned, err := e.operations.CheckFleetTargetPlanAbandoned(ctx, plan.ID); err != nil {
			e.t.Fatal(err)
		} else if abandoned {
			return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: lifecycle.CodeFleetTargetHolderAbandoned}, fleettest.DispatchRef{}
		}
		return fleettest.Resp{HTTPStatus: http.StatusOK}, fleettest.DispatchRef{RunID: fixture.runID, RunAttempt: 1, ApprovedHeadSHA: fixture.approvedHeadSHA, PlanSHA256: fixture.planSHA256}
	}
	// m11/B1-H7: the same pre-checks etcd_fleet_github_dispatch.go runs
	// before its signed reservation.
	if abandoned, err := e.operations.CheckFleetTargetPlanAbandoned(ctx, plan.ID); err != nil {
		e.t.Fatal(err)
	} else if abandoned {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: lifecycle.CodeFleetTargetHolderAbandoned}, fleettest.DispatchRef{}
	}
	if occupied, err := e.operations.FleetTargetOccupiedForPlan(ctx, fixture.cluster, fixture.environment, plan.ID); err != nil {
		e.t.Fatal(err)
	} else if occupied {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: lifecycle.CodeFleetTargetExecutionOccupied}, fleettest.DispatchRef{}
	}
	runID := nextEtcdConformanceRunID()
	approvedHeadSHA, planSHA256 := strings.Repeat("c", 40), strings.Repeat("b", 64)
	dispatchPayload := map[string]interface{}{
		"planId": plan.ID, "planDigest": e.planDigest[plan.ID], "sourceDigest": etcdConformanceSourceDigest,
		"planRunId": int64(1), "planSha256": planSHA256, "approvedHeadSha": approvedHeadSHA,
		"fleetEnvironment": fixture.environment, "allowDestructive": false,
	}
	dispatchOperation := model.Operation{
		ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test",
		Payload: map[string]interface{}{"fleetGitHub": dispatchPayload}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1,
	}
	dispatchAcceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: e.authority, Actor: store.OperationActor{Issuer: e.authority + "/fleet-github", Subject: plan.ID}, Kind: dispatchOperation.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"},
		Operation: dispatchOperation, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": dispatchPayload},
	}
	var err error
	dispatchAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(dispatchAcceptance)
	if err != nil {
		e.t.Fatal(err)
	}
	_, prepared, err := e.operations.AcceptFleetGitHubDispatch(ctx, dispatchAcceptance, etcdstore.FleetGitHubDispatchPreparation{
		PlanID: plan.ID, PlanRunID: 1, PlanSHA256: planSHA256, ApprovedHeadSHA: approvedHeadSHA, FleetEnvironment: fixture.environment,
	})
	if err != nil {
		return fenceErrResp(err, 0), fleettest.DispatchRef{}
	}
	fixture.nonceSHA256, fixture.approvedHeadSHA, fixture.planSHA256 = prepared.DispatchNonceSHA256, approvedHeadSHA, planSHA256
	if outcome == fleettest.GitHubOutcomeAmbiguous {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: "fleet_github_dispatch_ambiguous"}, fleettest.DispatchRef{}
	}
	workflowURL := fmt.Sprintf("https://github.com/%s/actions/runs/%d", etcdConformanceRepository, runID)
	if err := e.operations.FinishFleetGitHubDispatch(ctx, plan.ID, prepared.DispatchNonceSHA256, runID, workflowURL); err != nil {
		return fenceErrResp(err, 0), fleettest.DispatchRef{}
	}
	fixture.runID = runID
	e.dispatch[plan.ID] = &etcdConformanceDispatch{runID: runID, rawNonce: prepared.DispatchNonce, approvedHeadSHA: approvedHeadSHA, planSHA256: planSHA256}
	return fleettest.Resp{HTTPStatus: http.StatusCreated}, fleettest.DispatchRef{RunID: runID, RunAttempt: 1, RawNonce: prepared.DispatchNonce, ApprovedHeadSHA: approvedHeadSHA, PlanSHA256: planSHA256}
}

func (e *etcdLifecycleHarness) Epoch() int64 {
	e.t.Helper()
	epoch, err := e.operations.FleetAuthorityEpoch(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return epoch
}

func (e *etcdLifecycleHarness) AdvanceEpoch(expected int64, reason string) (int64, error) {
	return e.operations.AdvanceFleetAuthorityEpoch(context.Background(), expected, reason)
}

func (e *etcdLifecycleHarness) Release(targetID string, mode lifecycle.ReleaseMode, expectedGeneration int64) fleettest.Resp {
	e.t.Helper()
	ctx := context.Background()
	fence, _, err := e.operations.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		e.t.Fatal(err)
	}
	var proof *lifecycle.TerminalProof
	switch mode {
	case lifecycle.ReleaseModeTerminal:
		proof, err = e.buildTerminalProof(ctx, fence.HolderPlanID)
		if err != nil {
			e.t.Fatal(err)
		}
	case lifecycle.ReleaseModeAbandon:
		proof = &lifecycle.TerminalProof{ListingSnapshotSHA256: etcdConformanceListingSnapshotSHA256}
	}
	_, releaseErr := e.operations.ReleaseFleetTargetFence(ctx, targetID, expectedGeneration, mode, proof, uuid.NewString(), time.Now().UTC())
	return fenceErrResp(releaseErr, http.StatusOK)
}

func (e *etcdLifecycleHarness) AbandonPlan(plan fleettest.PlanRef) fleettest.Resp {
	e.t.Helper()
	fixture := e.fenceFixtures[plan.ID]
	cluster, environment, nonceSHA256 := "", "", ""
	if fixture != nil {
		cluster, environment, nonceSHA256 = fixture.cluster, fixture.environment, fixture.nonceSHA256
	}
	err := e.operations.AbandonFleetTargetPlan(context.Background(), plan.ID, nonceSHA256, cluster, environment, uuid.NewString(), etcdConformanceListingSnapshotSHA256, time.Now().UTC())
	return fenceErrResp(err, http.StatusOK)
}

func (e *etcdLifecycleHarness) AgeHolder(plan fleettest.PlanRef, by time.Duration) {
	e.t.Helper()
	if err := e.operations.AgeFleetTargetHolder(context.Background(), plan.ID, by); err != nil {
		e.t.Fatal(err)
	}
}

func (e *etcdLifecycleHarness) Occupancy(targetID string) (lifecycle.Occupancy, string, error) {
	return e.operations.FleetTargetOutcome(context.Background(), targetID, time.Now().UTC())
}

func (e *etcdLifecycleHarness) FenceFacts(targetID string) lifecycle.FenceFacts {
	e.t.Helper()
	facts, _, err := e.operations.GetFleetTargetFence(context.Background(), targetID)
	if err != nil {
		e.t.Fatal(err)
	}
	return facts
}

// TestFleetFenceConformanceEtcd runs the shared fence conformance suite
// (fleet/fleettest/fence.go) against the live etcd runner-attempt/reconciliation
// HTTP routes plus this WP's new fence storage transitions (WP8b, fixing
// plan-review.md B3 for the fence domain the same way WP6 fixed it for
// lifecycle). Each case gets its own factory and a fresh etcd prefix
// (plan.md §3's "Depends: WP4, WP6" and WP7's FenceHarness contract: "newHarness
// must sit on a fresh, isolated backend... for every case").
func TestFleetFenceConformanceEtcd(t *testing.T) {
	fleettest.RunFleetFenceConformance(t, func(t *testing.T) fleettest.FenceHarness {
		client, prefix := integrationtest.Etcd(t)
		return newEtcdLifecycleHarness(t, client, prefix)
	})
}
