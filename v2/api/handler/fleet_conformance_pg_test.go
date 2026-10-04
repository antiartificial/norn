package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// TestFleetLifecycleConformancePostgres drives the live legacy PG handlers
// (the routes main.go registers on the general router, plan.md §1.2) through
// httptest with a CI AccessPrincipal and real PG, fixing plan-review.md B3:
// the shared conformance suite now exercises a routed path, not the unrouted
// PG V3 signed path.
func TestFleetLifecycleConformancePostgres(t *testing.T) {
	db := integrationtest.PG(t)
	requireRoutesRegistered(t, "../main.go", pgLegacyFleetRunnerRoutes, func(route pgLegacyFleetRunnerRoute) string {
		return fmt.Sprintf("r.%s(%q, h.%s)", route.method, "/v1"+route.path, route.name)
	})
	h := newPGLifecycleHarness(t, db)
	fleettest.RunFleetLifecycleConformance(t, h)
}

// pgLegacyFleetRunnerRoute is one legacy runner route exactly as main.go
// registers it inside its r.Route("/api", ...) group. main.go is package main
// and cannot be imported here, so the harness mounts this table under the same
// "/api" group and middleware chain, and requireRoutesRegistered fails the test
// if main.go stops registering any row verbatim (e.g. a reroute to the PG V3
// handlers), so a routing change cannot leave this suite passing on dead code.
type pgLegacyFleetRunnerRoute struct {
	method, path, name string
	handler            func(*Handler) http.HandlerFunc
}

var pgLegacyFleetRunnerRoutes = []pgLegacyFleetRunnerRoute{
	{"Get", "/fleet/plans/{planID}/reconciliations", "ListFleetReconciliations", func(h *Handler) http.HandlerFunc { return h.ListFleetReconciliations }},
	{"Post", "/fleet/plans/{planID}/reconciliations", "RecordFleetReconciliation", func(h *Handler) http.HandlerFunc { return h.RecordFleetReconciliation }},
	{"Get", "/fleet/plans/{planID}/attempts", "ListFleetRunnerAttempts", func(h *Handler) http.HandlerFunc { return h.ListFleetRunnerAttempts }},
	{"Post", "/fleet/plans/{planID}/attempts", "StartFleetRunnerAttempt", func(h *Handler) http.HandlerFunc { return h.StartFleetRunnerAttempt }},
	{"Get", "/fleet/plans/{planID}/attempts/{attemptID}", "GetFleetRunnerAttempt", func(h *Handler) http.HandlerFunc { return h.GetFleetRunnerAttempt }},
	{"Post", "/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", "HeartbeatFleetRunnerAttempt", func(h *Handler) http.HandlerFunc { return h.HeartbeatFleetRunnerAttempt }},
	{"Post", "/fleet/plans/{planID}/attempts/{attemptID}/advance", "AdvanceFleetRunnerAttempt", func(h *Handler) http.HandlerFunc { return h.AdvanceFleetRunnerAttempt }},
	{"Post", "/fleet/plans/{planID}/attempts/{attemptID}/cancel", "CancelFleetRunnerAttempt", func(h *Handler) http.HandlerFunc { return h.CancelFleetRunnerAttempt }},
}

// requireRoutesRegistered fails t unless the production source file at path
// contains every route's registration line, as rendered by line, verbatim.
func requireRoutesRegistered[R any](t *testing.T, path string, routes []R, line func(R) string) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if want := line(route); !strings.Contains(string(source), want) {
			t.Fatalf("%s no longer registers %s; the conformance harness's route table has drifted from production", path, want)
		}
	}
}

var pgConformanceRunSeq atomic.Int64

func nextPGConformanceRunID() int64 {
	return 900_000_000 + pgConformanceRunSeq.Add(1)
}

const pgConformanceRepository = "acme/norn-fleet"
const pgConformanceEnvironment = "production/nyc3"

type pgConformanceDispatch struct {
	runID           int64
	rawNonce        string
	approvedHeadSHA string
	planSHA256      string
}

type pgLifecycleHarness struct {
	t         *testing.T
	db        *store.DB
	h         *Handler
	router    chi.Router
	dispatch  map[string]*pgConformanceDispatch // planID -> seeded dispatch binding
	ciForPlan map[string]*CIIdentity            // attemptID -> owning CI identity
	// github, planEnv and planCluster are set only by newPGFenceHarness;
	// every LifecycleHarness-only method above leaves them nil/empty and
	// does not read them (TestFleetLifecycleConformancePostgres is
	// unaffected).
	github      *fakeFleetGitHubServer
	planEnv     map[string]string // planID -> dispatch FleetEnvironment lane
	planCluster map[string]string // planID -> capacity-plan cluster
}

// var _ fleettest.FenceHarness asserts at compile time that the stub methods
// below satisfy FenceHarness, even though WP7 does not call
// RunFleetFenceConformance here (WP8a does).
var _ fleettest.FenceHarness = (*pgLifecycleHarness)(nil)

func newPGLifecycleHarness(t *testing.T, db *store.DB) *pgLifecycleHarness {
	t.Helper()
	cfg := &config.Config{AuditSigningKey: strings.Repeat("a", 32), GitHubActionsFleetAllowedRepository: pgConformanceRepository + "@1@2"}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	// main.go's general-router chain after bearerAuth (package main; the
	// harness injects the CI principal bearerAuth would have attached).
	router := chi.NewRouter()
	router.Use(h.MutationAuditMiddleware, h.ProductionMutationAdmissionMiddleware, h.EvidenceReserveAdmissionMiddleware, h.AccessMiddleware)
	router.Route("/api", func(r chi.Router) {
		for _, route := range pgLegacyFleetRunnerRoutes {
			r.MethodFunc(strings.ToUpper(route.method), "/v1"+route.path, route.handler(h))
		}
	})
	return &pgLifecycleHarness{
		t: t, db: db, h: h, router: router,
		dispatch:  map[string]*pgConformanceDispatch{},
		ciForPlan: map[string]*CIIdentity{},
	}
}

// newPGFenceHarness extends newPGLifecycleHarness with everything
// RunFleetFenceConformance needs beyond the attempt/reconciliation routes:
// a fake GitHub App server wired as this Handler's real *githubapp.Client
// (the same seam TestDispatchPreSubmitFailureLeavesSubmittingHTTP uses), a
// per-case fleet.yaml as the handler's configured fleet root, and the real
// chi dispatch route. Each case gets its own fresh harness
// (RunFleetFenceConformance's newHarness(t) contract), so github's in-memory
// state, the fleet root and the database schema never leak across cases.
func newPGFenceHarness(t *testing.T) *pgLifecycleHarness {
	t.Helper()
	db := integrationtest.PG(t)
	p := newPGLifecycleHarness(t, db)
	p.planEnv = map[string]string{}
	p.planCluster = map[string]string{}

	dir := t.TempDir()
	gh := newFakeFleetGitHubServer(t)
	p.github = gh
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "github-app.pem")
	encodedKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if err := os.WriteFile(keyPath, encodedKey, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := githubapp.New(githubapp.Config{
		AppID: "9999", InstallationID: 1, PrivateKeyFile: keyPath, Repository: pgConformanceRepository,
		DefaultBranch: "main", Environment: "production", ConfigPath: "environments/production/nyc3/cluster.yaml",
		PlanWorkflow: "plan.yml", ApplyWorkflow: "apply.yml", RecoverWorkflow: "recover.yml", APIBaseURL: gh.server.URL,
	}, gh.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	p.h.fleetGitHub, p.h.fleetGitHubConfigError = client, nil

	// The handler's configured fleet root resolves to pgConformanceEnvironment
	// (production/nyc3), the lane requireMatchingFleetEnvironment returns.
	fleetConfig := filepath.Join(dir, "fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata: {environment: production}
cluster: {name: fence-conformance, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 3
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(fleetConfig, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	p.h.cfg.FleetConfig = fleetConfig
	p.router.Post("/api/v1/fleet/plans/{planID}/github/dispatch", p.h.DispatchFleetGitHubApply)
	mountFleetTargetRoutes(p.router, p.h.FleetTargetRoutes())
	return p
}

func (p *pgLifecycleHarness) Profile() lifecycle.Profile { return lifecycle.PostgresLegacy }

func (p *pgLifecycleHarness) SeedPlan(action string) fleettest.PlanRef {
	return p.seedPlanOnCluster(action, "fleet", "")
}

func (p *pgLifecycleHarness) SeedPlanOnCluster(action, cluster, environment string) fleettest.PlanRef {
	return p.seedPlanOnCluster(action, cluster, environment)
}

func (p *pgLifecycleHarness) seedPlanOnCluster(action, cluster, environment string) fleettest.PlanRef {
	p.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	finished := now
	planID := uuid.NewString()
	typed := fleet.CapacityPlan{
		SchemaVersion: "norn.fleet-capacity-plan/v1", ID: planID, Cluster: cluster, Pool: "app",
		Current: fleet.NodePool{Desired: 3}, Proposed: fleet.NodePool{Desired: 3}, Action: action,
		SourceDigest: "sha256:" + strings.Repeat("1", 64),
	}
	p.signCapacityPlan(&typed)
	encodedPlan, _ := json.Marshal(typed)
	var payload map[string]interface{}
	if err := json.Unmarshal(encodedPlan, &payload); err != nil {
		p.t.Fatal(err)
	}
	plan := &model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		Payload: payload, StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
	}
	if err := p.db.InsertCompletedOperation(ctx, plan); err != nil {
		p.t.Fatal(err)
	}
	if p.planEnv != nil {
		p.planEnv[planID] = environment
		p.planCluster[planID] = cluster
	}
	return fleettest.PlanRef{ID: planID, RequiresDrain: false}
}

func (p *pgLifecycleHarness) signCapacityPlan(plan *fleet.CapacityPlan) {
	canonical, err := canonicalCapacityPlan(plan)
	if err != nil {
		p.t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(p.h.cfg.AuditSigningKey))
	_, _ = mac.Write([]byte(plan.Digest))
	plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func (p *pgLifecycleHarness) SeedDispatch(plan fleettest.PlanRef) fleettest.DispatchRef {
	p.t.Helper()
	ctx := context.Background()
	rawNonce, nonceHash, err := newFleetDispatchNonce()
	if err != nil {
		p.t.Fatal(err)
	}
	runID := nextPGConformanceRunID()
	approvedHeadSHA := strings.Repeat("a", 40)
	planSHA256 := strings.Repeat("b", 64)
	if _, err := p.db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{
		PlanID: plan.ID, PlanRunID: 1, PlanSHA256: planSHA256, ApprovedHeadSHA: approvedHeadSHA,
		FleetEnvironment: pgConformanceEnvironment, DispatchNonceSHA256: nonceHash, DispatchState: "prepared",
	}); err != nil {
		p.t.Fatal(err)
	}
	workflowURL := fmt.Sprintf("https://github.com/%s/actions/runs/%d", pgConformanceRepository, runID)
	if _, err := p.db.FinishFleetGitHubDispatch(ctx, plan.ID, nonceHash, runID, 1, workflowURL); err != nil {
		p.t.Fatal(err)
	}
	p.dispatch[plan.ID] = &pgConformanceDispatch{runID: runID, rawNonce: rawNonce, approvedHeadSHA: approvedHeadSHA, planSHA256: planSHA256}
	return fleettest.DispatchRef{RunID: runID, RunAttempt: 1, RawNonce: rawNonce, ApprovedHeadSHA: approvedHeadSHA, PlanSHA256: planSHA256}
}

func pgConformanceCI(run fleettest.RunRef) *CIIdentity {
	return &CIIdentity{
		Provider: "github-actions", Repository: pgConformanceRepository, RepositoryOwnerID: "1", RepositoryID: "2",
		RunID: strconv.FormatInt(run.RunID, 10), RunAttempt: strconv.Itoa(run.RunAttempt),
		Environment: "production", SHA: run.SHA, RefProtected: true, Intent: run.Intent,
	}
}

func (p *pgLifecycleHarness) principal(ci *CIIdentity) *AccessPrincipal {
	return &AccessPrincipal{Subject: "runner", Source: AccessPrincipalSourceManagedToken, TokenID: "token-" + ci.RunID, Scopes: []string{ScopeFleetOperate}, CI: ci}
}

func (p *pgLifecycleHarness) start(plan fleettest.PlanRef, run fleettest.RunRef, resume bool) fleettest.Resp {
	p.t.Helper()
	dispatch, ok := p.dispatch[plan.ID]
	if !ok {
		p.t.Fatalf("plan %s has no seeded dispatch", plan.ID)
	}
	ci := pgConformanceCI(run)
	req := fleet.RunnerAttemptStartRequest{
		SchemaVersion: model.FleetRunnerAttemptSchemaVersion, RunnerAttemptID: canonicalFleetRunnerAttemptID(ci),
		CommitSHA: run.SHA, PlanSHA256: dispatch.planSHA256, DispatchNonce: dispatch.rawNonce,
		SourceDispatchRunID: dispatch.runID, WorkflowURL: canonicalFleetWorkflowURL(ci),
		HeartbeatTimeoutSeconds: 30, Resume: resume,
	}
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/attempts", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq = WithAccessPrincipal(httpReq, p.principal(ci))
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, httpReq)
	resp := pgAttemptResp(rec)
	if resp.Attempt != nil {
		p.ciForPlan[resp.Attempt.ID] = ci
	}
	return resp
}

func (p *pgLifecycleHarness) Start(plan fleettest.PlanRef, run fleettest.RunRef) fleettest.Resp {
	return p.start(plan, run, false)
}

func (p *pgLifecycleHarness) Recover(plan fleettest.PlanRef, run fleettest.RunRef, _ *fleettest.StopEvidence) fleettest.Resp {
	// Q11 (plan.md §5): PG legacy requires no server-observed predecessor
	// stop proof, so StopEvidence is accepted but ignored here. When this
	// harness has a fake GitHub server (newPGFenceHarness), register the
	// recover run it names so a later Q11 check or terminal release can
	// observe it; LifecycleHarness-only use (p.github == nil) is unaffected.
	if p.github != nil {
		if dispatch, ok := p.dispatch[plan.ID]; ok {
			p.github.registerRecoverRun(run, dispatch.runID)
		}
	}
	return p.start(plan, run, true)
}

func (p *pgLifecycleHarness) ownerCI(attemptID string) *CIIdentity {
	ci, ok := p.ciForPlan[attemptID]
	if !ok {
		p.t.Fatalf("no owning CI identity tracked for attempt %s", attemptID)
	}
	return ci
}

func (p *pgLifecycleHarness) Heartbeat(attempt *fleet.RunnerAttempt, sequence, revision int64) fleettest.Resp {
	req := fleet.RunnerHeartbeatRequest{SchemaVersion: model.FleetRunnerAttemptSchemaVersion, Phase: attempt.CurrentPhase, Sequence: sequence, Revision: revision, Message: "alive"}
	return p.mutate(attempt, "heartbeat", req)
}

func (p *pgLifecycleHarness) Advance(attempt *fleet.RunnerAttempt, expectedPhase string, revision int64) fleettest.Resp {
	req := fleet.RunnerAdvanceRequest{SchemaVersion: model.FleetRunnerAttemptSchemaVersion, ExpectedPhase: expectedPhase, Revision: revision}
	return p.mutate(attempt, "advance", req)
}

func (p *pgLifecycleHarness) Cancel(attempt *fleet.RunnerAttempt, revision int64, reason string) fleettest.Resp {
	req := fleet.RunnerCancelRequest{SchemaVersion: model.FleetRunnerAttemptSchemaVersion, Revision: revision, Reason: reason}
	return p.mutate(attempt, "cancel", req)
}

func (p *pgLifecycleHarness) mutate(attempt *fleet.RunnerAttempt, verb string, body interface{}) fleettest.Resp {
	p.t.Helper()
	ci := p.ownerCI(attempt.ID)
	encoded, _ := json.Marshal(body)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+attempt.PlanID+"/attempts/"+attempt.ID+"/"+verb, bytes.NewReader(encoded))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq = WithAccessPrincipal(httpReq, p.principal(ci))
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, httpReq)
	return pgAttemptResp(rec)
}

func (p *pgLifecycleHarness) Checkpoint(attempt *fleet.RunnerAttempt, phase, status string) fleettest.Resp {
	ci := p.ownerCI(attempt.ID)
	req := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, AttemptID: attempt.ID, Phase: phase, Status: status,
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, StateSerial: 1,
		EvidenceDigest: "sha256:" + strings.Repeat("c", 64),
	}
	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+attempt.PlanID+"/reconciliations", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", "checkpoint:"+attempt.ID+":"+phase+":"+status)
	httpReq = WithAccessPrincipal(httpReq, p.principal(ci))
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, httpReq)
	return conformanceProblemResp(rec)
}

func (p *pgLifecycleHarness) Get(attempt *fleet.RunnerAttempt) fleettest.Resp {
	ci := p.ownerCI(attempt.ID)
	httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/plans/"+attempt.PlanID+"/attempts/"+attempt.ID, nil)
	httpReq = WithAccessPrincipal(httpReq, p.principal(ci))
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, httpReq)
	return pgAttemptResp(rec)
}

func (p *pgLifecycleHarness) ExpireAttempt(attempt *fleet.RunnerAttempt) {
	p.t.Helper()
	if _, err := p.db.Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_at = now() - interval '1 hour', heartbeat_expires_at = now() - interval '1 hour' + (heartbeat_timeout_seconds * interval '1 second') WHERE id = $1`, attempt.ID); err != nil {
		p.t.Fatal(err)
	}
}

func pgAttemptResp(rec *httptest.ResponseRecorder) fleettest.Resp {
	if rec.Code >= http.StatusBadRequest {
		return fleettest.Resp{HTTPStatus: rec.Code, Code: decodeProblemCode(rec)}
	}
	var attempt model.FleetRunnerAttempt
	if err := json.Unmarshal(rec.Body.Bytes(), &attempt); err != nil {
		return fleettest.Resp{HTTPStatus: rec.Code}
	}
	converted := lifecycle.FromLegacy(&attempt)
	return fleettest.Resp{HTTPStatus: rec.Code, Attempt: &converted}
}

// conformanceProblemResp decodes a response that carries no attempt body: a
// checkpoint receipt on success, or a control problem on failure. Shared by
// both backends' harnesses.
func conformanceProblemResp(rec *httptest.ResponseRecorder) fleettest.Resp {
	if rec.Code >= http.StatusBadRequest {
		return fleettest.Resp{HTTPStatus: rec.Code, Code: decodeProblemCode(rec)}
	}
	return fleettest.Resp{HTTPStatus: rec.Code}
}

func decodeProblemCode(rec *httptest.ResponseRecorder) string {
	var problem Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	return problem.Code
}

// TestDispatchPreSubmitFailureLeavesSubmittingHTTP adds the handler-level
// check that WP1's store-level pin (TestDispatchPreSubmitFailureLeavesSubmitting,
// handler/fleet_github_presubmit_test.go) could not do, because that test's
// *githubapp.Client has no test seam: it could only replay the handler's store
// calls, not drive DispatchFleetGitHubApply itself. This suite's httptest
// GitHub server (the same seam githubapp's own tests use) closes that gap.
// Per decision H4 (plan.md §5), the defect is pinned, not fixed: the dispatch
// row is left stuck in "submitting" with no bound run.
func TestDispatchPreSubmitFailureLeavesSubmittingHTTP(t *testing.T) {
	db := integrationtest.PG(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata: {environment: production}
cluster: {name: production-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 3
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	planSHA := strings.Repeat("a", 64)
	headSHA := strings.Repeat("b", 40)
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, _ := zipWriter.Create("fleet-plan.sha256")
	_, _ = entry.Write([]byte(planSHA + "\n"))
	_ = zipWriter.Close()
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if r.URL.Path == "/app/installations/5678/access_tokens" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"installation-token","expires_at":"2027-01-15T09:00:00Z"}`)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/runs"):
			fmt.Fprint(w, `{"workflow_runs":[]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, `[{"number":8,"html_url":"https://github.com/acme/norn-fleet/pull/8","state":"closed","merged_at":"2027-01-15T08:00:00Z"}]`)
		case strings.HasSuffix(r.URL.Path, "/pulls/8"):
			fmt.Fprintf(w, `{"merge_commit_sha":%q,"merged_at":"2027-01-15T08:00:00Z"}`, headSHA)
		case strings.Contains(r.URL.Path, "/actions/workflows/plan.yml/runs"):
			fmt.Fprintf(w, `{"workflow_runs":[{"id":91,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, headSHA)
		case strings.HasSuffix(r.URL.Path, "/actions/runs/91/artifacts"):
			fmt.Fprint(w, `{"artifacts":[{"id":92,"name":"fleet-plan-production-nyc3-91","expired":false}]}`)
		case strings.HasSuffix(r.URL.Path, "/actions/artifacts/92/zip"):
			_, _ = w.Write(archive.Bytes())
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/dispatches"):
			// The one permitted GitHub POST fails with a client error, which
			// githubapp.dispatchBoundPlan wraps as ErrDispatchPreSubmit.
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"workflow does not accept dispatch inputs"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeGitHub.Close)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "github-app.pem")
	encodedKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if err := os.WriteFile(keyPath, encodedKey, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := githubapp.New(githubapp.Config{
		AppID: "1234", InstallationID: 5678, PrivateKeyFile: keyPath, Repository: pgConformanceRepository,
		DefaultBranch: "main", Environment: "production", ConfigPath: "environments/production/nyc3/cluster.yaml",
		PlanWorkflow: "plan.yml", ApplyWorkflow: "apply.yml", APIBaseURL: fakeGitHub.URL,
	}, fakeGitHub.Client())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuditSigningKey: strings.Repeat("f", 32), FleetConfig: configPath, GitHubActionsFleetAllowedRepository: pgConformanceRepository + "@1@2"}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	h.fleetGitHub, h.fleetGitHubConfigError = client, nil

	desired := 3
	planBody, _ := json.Marshal(fleet.PlanRequest{Desired: &desired, Reason: "presubmit defect fixture"})
	planRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/node-pools/app/plan", bytes.NewReader(planBody))
	planRequest.Header.Set("Content-Type", "application/json")
	planRequest.Header.Set("Idempotency-Key", "presubmit-plan")
	planRoute := chi.NewRouteContext()
	planRoute.URLParams.Add("pool", "app")
	planRequest = planRequest.WithContext(context.WithValue(planRequest.Context(), chi.RouteCtxKey, planRoute))
	planRequest = WithAccessPrincipal(planRequest, &AccessPrincipal{Subject: "operator", TokenID: "token-device-presubmit", DeviceID: "device-presubmit", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAdmin}})
	planResponse := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(h.PlanFleetCapacity)).ServeHTTP(planResponse, planRequest)
	if planResponse.Code != http.StatusCreated {
		t.Fatalf("capacity plan status=%d body=%s", planResponse.Code, planResponse.Body.String())
	}
	var plan model.Operation
	if err := json.Unmarshal(planResponse.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	planID := plan.ID

	router := chi.NewRouter()
	router.Use(h.MutationAuditMiddleware)
	router.Post("/api/v1/fleet/plans/{planID}/github/dispatch", h.DispatchFleetGitHubApply)

	body, _ := json.Marshal(fleetGitHubDispatchRequest{AllowDestructive: false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/dispatch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-presubmit", DeviceID: "device-presubmit", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("dispatch status=%d body=%s", rec.Code, rec.Body.String())
	}

	stuck, err := db.GetFleetGitHubDispatch(context.Background(), planID)
	if err != nil || stuck.DispatchState != "submitting" || stuck.RunID != 0 {
		t.Fatalf("handler-level pre-submit defect did not reproduce: dispatch=%+v err=%v", stuck, err)
	}
}

// The FenceHarness methods below are WP8a's real bodies. RegisterTarget,
// Release and AbandonPlan drive the real signed WP9b routes, so the server
// gathers its own release evidence through the WP5 observers against the
// fake GitHub server; Dispatch and Start/Recover (inherited from
// LifecycleHarness) drive the real HTTP dispatch/attempt routes.

func (p *pgLifecycleHarness) RegisterTarget(identity lifecycle.TargetIdentity, aliases []string) fleettest.TargetResp {
	p.t.Helper()
	return fleetTargetRegisterResp(p.router, identity, aliases)
}

// Dispatch is the fence Acquire transition (plan.md §2.2). It writes plan's
// StartedAt directly (the one direct storage write fleet/fleettest/fence.go
// allows here), configures the fake GitHub server's outcome, and then drives
// the real chi route DispatchFleetGitHubApply for every plan on the
// handler's own lane.
//
// GitHubOutcomeAmbiguous: the legacy route reports the ambiguous POST itself
// as 502 fleet_github_dispatch_failed (unchanged behavior) and leaves the row
// submitting; the suite's 409 fleet_github_dispatch_ambiguous is what the
// same route reports for the follow-up submit of that plan, so the harness
// makes that second call and returns its result.
//
// The one exception is a plan seeded with a non-default environment lane
// (testAliasDoesNotBypass): the route derives the lane from the configured
// fleet root, which only admits <staging|production>/nyc3, so such a lane
// can never reach the route. dispatchDirect replays the route's store and
// githubapp sequence for that case only.
func (p *pgLifecycleHarness) Dispatch(plan fleettest.PlanRef, startedAt time.Time, outcome fleettest.GitHubOutcome) (fleettest.Resp, fleettest.DispatchRef) {
	p.t.Helper()
	ctx := context.Background()
	if _, err := p.db.Pool.Exec(ctx, `UPDATE operations SET started_at=$1 WHERE id=$2`, startedAt, plan.ID); err != nil {
		p.t.Fatal(err)
	}
	if env := p.planEnv[plan.ID]; env != "" && env != pgConformanceEnvironment {
		return p.dispatchDirect(plan, startedAt, outcome)
	}
	p.github.configure(plan.ID, pgConformanceEnvironment, outcome)
	rec := p.postDispatch(plan.ID)
	if outcome == fleettest.GitHubOutcomeAmbiguous && rec.Code == http.StatusBadGateway && decodeProblemCode(rec) == "fleet_github_dispatch_failed" {
		rec = p.postDispatch(plan.ID)
	}
	if rec.Code >= http.StatusBadRequest {
		return conformanceProblemResp(rec), fleettest.DispatchRef{}
	}
	binding, err := p.db.GetFleetGitHubDispatch(ctx, plan.ID)
	if err != nil || binding.RunID == 0 {
		p.t.Fatalf("dispatch route returned %d without a bound run: %+v, %v", rec.Code, binding, err)
	}
	rawNonce := p.github.rawNonce(plan.ID)
	p.dispatch[plan.ID] = &pgConformanceDispatch{runID: binding.RunID, rawNonce: rawNonce, approvedHeadSHA: binding.ApprovedHeadSHA, planSHA256: binding.PlanSHA256}
	return fleettest.Resp{HTTPStatus: rec.Code}, fleettest.DispatchRef{RunID: binding.RunID, RunAttempt: binding.RunAttempt, RawNonce: rawNonce, ApprovedHeadSHA: binding.ApprovedHeadSHA, PlanSHA256: binding.PlanSHA256}
}

func (p *pgLifecycleHarness) postDispatch(planID string) *httptest.ResponseRecorder {
	body, err := json.Marshal(fleetGitHubDispatchRequest{AllowDestructive: false})
	if err != nil {
		p.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/dispatch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-fence-dispatch", DeviceID: "device-fence-dispatch", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	return rec
}

// dispatchDirect replays DispatchFleetGitHubApply's store and githubapp
// sequence for a lane the route cannot produce (see Dispatch).
func (p *pgLifecycleHarness) dispatchDirect(plan fleettest.PlanRef, startedAt time.Time, outcome fleettest.GitHubOutcome) (fleettest.Resp, fleettest.DispatchRef) {
	p.t.Helper()
	ctx := context.Background()
	cluster := p.planCluster[plan.ID]
	// fenceEnvironment is the dispatch lane the fence domain resolves
	// aliases against (what a real deployment's requireMatchingFleetEnvironment
	// would have returned for that deployment's own fleet.yaml).
	// githubEnvironment is pgConformanceEnvironment unconditionally: every
	// githubapp.Client call requires fleetEnvironment == c.fleetRoot(),
	// which is fixed per client (one process, one fleet.yaml root in real
	// deployments too). Decoupling the two lets this single-process harness
	// exercise the fence's distinct environment-alias lane
	// (testAliasDoesNotBypass) without weakening either check: the GitHub
	// identity verification always runs against the client's real,
	// unmodified root.
	fenceEnvironment := p.planEnv[plan.ID]
	if fenceEnvironment == "" {
		fenceEnvironment = pgConformanceEnvironment
	}
	const githubEnvironment = pgConformanceEnvironment
	p.github.configure(plan.ID, fenceEnvironment, outcome)

	binding, err := p.db.GetFleetGitHubDispatch(ctx, plan.ID)
	if err == nil && binding.RunID > 0 {
		if abandoned, abErr := p.db.IsFleetPlanAbandonedForPlan(ctx, plan.ID); abErr == nil && abandoned {
			return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: lifecycle.CodeFleetTargetHolderAbandoned}, fleettest.DispatchRef{}
		}
		return fleettest.Resp{HTTPStatus: http.StatusCreated}, fleettest.DispatchRef{RunID: binding.RunID, RunAttempt: binding.RunAttempt, ApprovedHeadSHA: binding.ApprovedHeadSHA, PlanSHA256: binding.PlanSHA256}
	}
	var nonce string
	switch {
	case err == pgx.ErrNoRows:
		approved, resolveErr := p.h.fleetGitHub.ResolveApprovedPlan(ctx, plan.ID, githubEnvironment)
		if resolveErr != nil {
			p.t.Fatalf("resolve approved plan: %v", resolveErr)
		}
		var nonceHash string
		nonce, nonceHash, err = newFleetDispatchNonce()
		if err != nil {
			p.t.Fatal(err)
		}
		binding, err = p.db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{
			PlanID: plan.ID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA,
			FleetEnvironment: fenceEnvironment, DispatchNonceSHA256: nonceHash, DispatchState: "prepared",
		})
		if err != nil {
			p.t.Fatal(err)
		}
	case err != nil:
		p.t.Fatal(err)
	case binding.DispatchState == "submitting":
		// A prior call left the fence acquired with no bound run (B1's
		// ambiguous state, or a late retry). Recover by nonce hash only,
		// exactly as the handler's own "submitting" branch does.
		approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA}
		result, recoverErr := p.h.fleetGitHub.RecoverBoundPlan(ctx, plan.ID, githubEnvironment, approved, binding.DispatchNonceSHA256)
		if recoverErr != nil {
			return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: "fleet_github_dispatch_ambiguous"}, fleettest.DispatchRef{}
		}
		if _, finishErr := p.db.FinishFleetGitHubDispatch(ctx, plan.ID, binding.DispatchNonceSHA256, result.RunID, result.RunAttempt, result.URL); finishErr != nil {
			if abandoned, abErr := p.db.IsFleetPlanAbandonedForPlan(ctx, plan.ID); abErr == nil && abandoned {
				return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: lifecycle.CodeFleetTargetHolderAbandoned}, fleettest.DispatchRef{}
			}
			p.t.Fatal(finishErr)
		}
		p.dispatch[plan.ID] = &pgConformanceDispatch{runID: result.RunID, approvedHeadSHA: result.ApprovedHeadSHA, planSHA256: result.PlanSHA}
		return fleettest.Resp{HTTPStatus: http.StatusCreated}, fleettest.DispatchRef{RunID: result.RunID, RunAttempt: result.RunAttempt, ApprovedHeadSHA: result.ApprovedHeadSHA, PlanSHA256: result.PlanSHA}
	}
	nonceHash := binding.DispatchNonceSHA256

	if preflightErr := p.db.CheckFleetTargetAcquirePreflight(ctx, cluster, fenceEnvironment, plan.ID, nonceHash, startedAt); preflightErr != nil {
		if fe, ok := preflightErr.(*lifecycle.FenceError); ok {
			return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: fe.Code}, fleettest.DispatchRef{}
		}
		p.t.Fatal(preflightErr)
	}
	if _, err := p.db.MarkFleetGitHubDispatchSubmittingFenced(ctx, plan.ID, nonceHash, cluster, fenceEnvironment, startedAt); err != nil {
		if fe, ok := err.(*lifecycle.FenceError); ok {
			return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: fe.Code}, fleettest.DispatchRef{}
		}
		p.t.Fatal(err)
	}
	approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA}
	result, dispatchErr := p.h.fleetGitHub.DispatchBoundPlan(ctx, plan.ID, githubEnvironment, false, approved, nonce)
	if errors.Is(dispatchErr, githubapp.ErrDispatchAmbiguous) {
		return fleettest.Resp{HTTPStatus: http.StatusConflict, Code: "fleet_github_dispatch_ambiguous"}, fleettest.DispatchRef{}
	}
	if dispatchErr != nil {
		p.t.Fatalf("dispatch bound plan: %v", dispatchErr)
	}
	if _, err := p.db.FinishFleetGitHubDispatch(ctx, plan.ID, nonceHash, result.RunID, result.RunAttempt, result.URL); err != nil {
		p.t.Fatal(err)
	}
	p.dispatch[plan.ID] = &pgConformanceDispatch{runID: result.RunID, rawNonce: nonce, approvedHeadSHA: result.ApprovedHeadSHA, planSHA256: result.PlanSHA}
	return fleettest.Resp{HTTPStatus: http.StatusCreated}, fleettest.DispatchRef{RunID: result.RunID, RunAttempt: result.RunAttempt, ApprovedHeadSHA: result.ApprovedHeadSHA, PlanSHA256: result.PlanSHA}
}

func (p *pgLifecycleHarness) Epoch() int64 {
	p.t.Helper()
	var epoch int64
	if err := p.db.Pool.QueryRow(context.Background(), `SELECT epoch FROM fleet_authority_epoch WHERE singleton`).Scan(&epoch); err != nil {
		p.t.Fatal(err)
	}
	return epoch
}

func (p *pgLifecycleHarness) AdvanceEpoch(expected int64, reason string) (int64, error) {
	return p.db.AdvanceFleetAuthorityEpoch(context.Background(), expected, reason)
}

// Release drives POST targets/{id}/fence/release. The request carries intent
// only; the server gathers the proof (plan.md WP9b, B2).
func (p *pgLifecycleHarness) Release(targetID string, mode lifecycle.ReleaseMode, expectedGeneration int64) fleettest.Resp {
	p.t.Helper()
	return fleetTargetReleaseResp(p.router, targetID, mode, expectedGeneration)
}

// AbandonPlan drives POST targets/abandon-plan (H7), which works whether or
// not the plan's cluster is registered.
func (p *pgLifecycleHarness) AbandonPlan(plan fleettest.PlanRef) fleettest.Resp {
	p.t.Helper()
	return fleetTargetAbandonPlanResp(p.router, plan.ID)
}

// AgeHolder backdates every timestamp DecideRelease's abandon baseline
// reads for plan, by direct storage write, never a sleep.
func (p *pgLifecycleHarness) AgeHolder(plan fleettest.PlanRef, by time.Duration) {
	p.t.Helper()
	ctx := context.Background()
	seconds := by.Seconds()
	if _, err := p.db.Pool.Exec(ctx, `
		UPDATE fleet_github_dispatches
		SET submission_started_at = submission_started_at - ($2 * interval '1 second'),
		    created_at = created_at - ($2 * interval '1 second'),
		    rerun_started_at = CASE WHEN rerun_started_at IS NOT NULL THEN rerun_started_at - ($2 * interval '1 second') ELSE NULL END
		WHERE plan_id=$1
	`, plan.ID, seconds); err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.db.Pool.Exec(ctx, `UPDATE fleet_runner_attempts SET heartbeat_at = heartbeat_at - ($2 * interval '1 second'), heartbeat_expires_at = heartbeat_expires_at - ($2 * interval '1 second') WHERE plan_id=$1`, plan.ID, seconds); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pgLifecycleHarness) Occupancy(targetID string) (lifecycle.Occupancy, string, error) {
	ctx := context.Background()
	tx, err := p.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fence, err := store.GetFleetTargetFence(ctx, tx, targetID, false)
	if err != nil {
		return "", "", err
	}
	epoch, err := store.FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		return "", "", err
	}
	holder, err := store.FleetTargetHolderFacts(ctx, tx, fence.HolderPlanID)
	if err != nil {
		return "", "", err
	}
	occupancy, reason := lifecycle.Outcome(fence, holder, epoch, time.Now().UTC())
	return occupancy, reason, nil
}

func (p *pgLifecycleHarness) FenceFacts(targetID string) lifecycle.FenceFacts {
	p.t.Helper()
	ctx := context.Background()
	tx, err := p.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		p.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fence, err := store.GetFleetTargetFence(ctx, tx, targetID, false)
	if err != nil {
		p.t.Fatal(err)
	}
	return fence
}

func (p *pgLifecycleHarness) SetRunTerminal(run fleettest.RunRef, completed bool) {
	p.github.setTerminal(run.RunID, completed)
}

// TestFleetFenceConformancePostgres is WP8a's fence suite on PG legacy: a
// per-case harness factory (newPGFenceHarness), matching
// RunFleetFenceConformance's "fresh backend per case" contract.
func TestFleetFenceConformancePostgres(t *testing.T) {
	requireRoutesRegistered(t, "../main.go", pgFleetTargetRoutes, func(route [3]string) string {
		return fmt.Sprintf("r.%s(%q, fleetTargets.%s)", route[0], route[1], route[2])
	})
	fleettest.RunFleetFenceConformance(t, func(t *testing.T) fleettest.FenceHarness {
		return newPGFenceHarness(t)
	})
}

// --- fake GitHub App server ---
//
// This is a minimal, stateful stand-in for the GitHub REST surface
// githubapp.Client calls, scoped to exactly the endpoints the fence suite's
// flows reach: app/installation auth, the merged-plan-PR resolution chain
// (pulls, plan.yml runs, artifacts), the apply.yml dispatch/listing/run
// endpoints DispatchBoundPlan/RecoverBoundPlan/ObserveApplyRunByNonceHash
// use, and ObserveRecoverRun's run/attempt lookup. Recover runs are
// registered directly (registerRecoverRun), never dispatched over HTTP,
// since Norn never dispatches them itself.

type fakeGHRun struct {
	id           int64
	event        string
	headSHA      string
	headBranch   string
	path         string
	name         string
	displayTitle string
	status       string
	conclusion   string
	actorLogin   string
	actorType    string
	runAttempt   int
	kind         string // "apply" | "recover"; only "apply" is listed under apply.yml
	inputs       map[string]string
}

type fakePlanFixture struct {
	headSHA    string
	planSHA256 string
	prNumber   int
	planRunID  int64
	artifactID int64
}

type fakeFleetGitHubServer struct {
	t      *testing.T
	server *httptest.Server
	repo   string

	mu        sync.Mutex
	nextID    int64
	runs      map[int64]*fakeGHRun
	plans     map[string]*fakePlanFixture
	prToPlan  map[int]string
	runToPlan map[int64]string // plan.yml run ID or artifact ID -> planID
	ambiguous map[string]bool
	// pendingNonce remembers the raw nonce of every apply dispatch POST,
	// bound or not, so a later configure(..., Submitted) can materialize the
	// run an earlier ambiguous POST may have created on GitHub's side.
	pendingNonce map[string]string
}

var (
	fakeGHPullNumberRe = regexp.MustCompile(`/pulls/(\d+)$`)
	fakeGHArtifactsRe  = regexp.MustCompile(`/runs/(\d+)/artifacts$`)
	fakeGHArtifactZip  = regexp.MustCompile(`/artifacts/(\d+)/zip$`)
	fakeGHRunByIDRe    = regexp.MustCompile(`/runs/(\d+)(?:/attempts/\d+)?$`)
)

func newFakeFleetGitHubServer(t *testing.T) *fakeFleetGitHubServer {
	t.Helper()
	s := &fakeFleetGitHubServer{
		t: t, repo: pgConformanceRepository, nextID: 1,
		runs: map[int64]*fakeGHRun{}, plans: map[string]*fakePlanFixture{},
		prToPlan: map[int]string{}, runToPlan: map[int64]string{}, ambiguous: map[string]bool{}, pendingNonce: map[string]string{},
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *fakeFleetGitHubServer) nextSeq() int64 {
	s.nextID++
	return s.nextID
}

// configure ensures a plan fixture exists and records whether the next
// apply dispatch POST for plan should bind a run or stay ambiguous.
func (s *fakeFleetGitHubServer) configure(planID, environment string, outcome fleettest.GitHubOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFixtureLocked(planID)
	ambiguous := outcome == fleettest.GitHubOutcomeAmbiguous
	s.ambiguous[planID] = ambiguous
	if !ambiguous {
		if nonce, ok := s.pendingNonce[planID]; ok && !s.hasApplyRunLocked(planID) {
			s.createApplyRunLocked(planID, nonce)
		}
	}
}

// rawNonce returns the raw dispatch nonce the route POSTed for planID: PG
// never persists it (D10), so the harness reads it where a real runner would
// (the workflow's dispatch inputs).
func (s *fakeFleetGitHubServer) rawNonce(planID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingNonce[planID]
}

func (s *fakeFleetGitHubServer) hasApplyRunLocked(planID string) bool {
	for _, run := range s.runs {
		if run.kind == "apply" && run.inputs["norn_plan_id"] == planID {
			return true
		}
	}
	return false
}

// createApplyRunLocked creates an apply run that is NOT yet completed: a run
// is reported completed only after SetRunTerminal(run, true).
func (s *fakeFleetGitHubServer) createApplyRunLocked(planID, nonce string) int64 {
	fx := s.ensureFixtureLocked(planID)
	id := s.nextSeq()
	s.runs[id] = &fakeGHRun{
		id: id, event: "workflow_dispatch", headSHA: fx.headSHA, headBranch: "main",
		path: ".github/workflows/apply.yml", name: "apply",
		displayTitle: fmt.Sprintf("Apply %s Norn plan %s nonce %s", pgConformanceEnvironment, planID, nonce),
		status:       "in_progress", actorLogin: "norn[bot]", actorType: "Bot", runAttempt: 1, kind: "apply",
		inputs: map[string]string{"norn_plan_id": planID, "dispatch_nonce": nonce, "fleet_environment": pgConformanceEnvironment},
	}
	return id
}

func (s *fakeFleetGitHubServer) ensureFixtureLocked(planID string) *fakePlanFixture {
	if fx, ok := s.plans[planID]; ok {
		return fx
	}
	sum := sha256.Sum256([]byte("head:" + planID))
	fx := &fakePlanFixture{
		headSHA:    hex.EncodeToString(sum[:])[:40],
		planSHA256: hex.EncodeToString(sum[:]),
		prNumber:   int(s.nextSeq()),
		planRunID:  s.nextSeq(),
		artifactID: s.nextSeq(),
	}
	s.plans[planID] = fx
	s.prToPlan[fx.prNumber] = planID
	s.runToPlan[fx.planRunID] = planID
	s.runToPlan[fx.artifactID] = planID
	return fx
}

func (s *fakeFleetGitHubServer) setTerminal(runID int64, completed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return
	}
	if completed {
		run.status, run.conclusion = "completed", "success"
	} else {
		run.status, run.conclusion = "in_progress", ""
	}
}

// registerRecoverRun registers run as a "workflow_run"-triggered recover
// workflow run bound to boundApplyRunID (ObserveRecoverRun's identity, which
// accepts workflow_run unconditionally with no actor restriction).
func (s *fakeFleetGitHubServer) registerRecoverRun(run fleettest.RunRef, boundApplyRunID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = &fakeGHRun{
		id: run.RunID, event: "workflow_run", headSHA: run.SHA, headBranch: "main",
		path: ".github/workflows/recover.yml", name: "recover",
		displayTitle: fmt.Sprintf("Recover Fleet apply %d", boundApplyRunID),
		status:       "in_progress", runAttempt: run.RunAttempt, kind: "recover",
	}
}

func (s *fakeFleetGitHubServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/app":
		fmt.Fprint(w, `{"slug":"norn"}`)
	case strings.HasSuffix(path, "/access_tokens"):
		fmt.Fprint(w, `{"token":"installation-token","expires_at":"2027-01-15T09:00:00Z"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/pulls"):
		s.handlePullsListLocked(w, r)
	case r.Method == http.MethodGet && fakeGHPullNumberRe.MatchString(path):
		s.handlePullShowLocked(w, fakeGHPullNumberRe.FindStringSubmatch(path)[1])
	case strings.Contains(path, "/actions/workflows/plan.yml/runs"):
		s.handlePlanRunsLocked(w)
	case r.Method == http.MethodPost && strings.Contains(path, "/actions/workflows/apply.yml/dispatches"):
		s.handleApplyDispatchLocked(w, r)
	case strings.Contains(path, "/actions/workflows/apply.yml/runs"):
		s.handleApplyRunsListLocked(w)
	case fakeGHArtifactZip.MatchString(path):
		s.handleArtifactZipLocked(w, fakeGHArtifactZip.FindStringSubmatch(path)[1])
	case fakeGHArtifactsRe.MatchString(path):
		s.handleArtifactsListLocked(w, fakeGHArtifactsRe.FindStringSubmatch(path)[1])
	case fakeGHRunByIDRe.MatchString(path):
		s.handleRunShowLocked(w, fakeGHRunByIDRe.FindStringSubmatch(path)[1])
	default:
		http.NotFound(w, r)
	}
}

func (s *fakeFleetGitHubServer) handlePullsListLocked(w http.ResponseWriter, r *http.Request) {
	head := r.URL.Query().Get("head")
	_, branch, _ := strings.Cut(head, ":")
	planID := strings.TrimPrefix(branch, "norn/plan-")
	fx := s.ensureFixtureLocked(planID)
	fmt.Fprintf(w, `[{"number":%d,"html_url":"https://github.com/%s/pull/%d","state":"closed","merged_at":"2027-01-15T08:00:00Z","head":{"ref":%q,"sha":%q},"base":{"ref":"main"}}]`,
		fx.prNumber, s.repo, fx.prNumber, branch, fx.headSHA)
}

func (s *fakeFleetGitHubServer) handlePullShowLocked(w http.ResponseWriter, numberStr string) {
	number, _ := strconv.Atoi(numberStr)
	fx := s.plans[s.prToPlan[number]]
	fmt.Fprintf(w, `{"merge_commit_sha":%q,"merged_at":"2027-01-15T08:00:00Z"}`, fx.headSHA)
}

func (s *fakeFleetGitHubServer) handlePlanRunsLocked(w http.ResponseWriter) {
	items := make([]string, 0, len(s.plans))
	for _, fx := range s.plans {
		items = append(items, fmt.Sprintf(`{"id":%d,"head_sha":%q,"status":"completed","conclusion":"success"}`, fx.planRunID, fx.headSHA))
	}
	fmt.Fprintf(w, `{"workflow_runs":[%s]}`, strings.Join(items, ","))
}

func (s *fakeFleetGitHubServer) handleArtifactsListLocked(w http.ResponseWriter, runIDStr string) {
	runID, _ := strconv.ParseInt(runIDStr, 10, 64)
	fx := s.plans[s.runToPlan[runID]]
	name := fmt.Sprintf("fleet-plan-%s-%d", strings.ReplaceAll(pgConformanceEnvironment, "/", "-"), runID)
	fmt.Fprintf(w, `{"artifacts":[{"id":%d,"name":%q,"expired":false}]}`, fx.artifactID, name)
}

func (s *fakeFleetGitHubServer) handleArtifactZipLocked(w http.ResponseWriter, artifactIDStr string) {
	artifactID, _ := strconv.ParseInt(artifactIDStr, 10, 64)
	fx := s.plans[s.runToPlan[artifactID]]
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, _ := zw.Create("fleet-plan.sha256")
	_, _ = entry.Write([]byte(fx.planSHA256 + "\n"))
	_ = zw.Close()
	_, _ = w.Write(buf.Bytes())
}

func (s *fakeFleetGitHubServer) handleApplyDispatchLocked(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Inputs map[string]string `json:"inputs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	planID := body.Inputs["norn_plan_id"]
	nonce := body.Inputs["dispatch_nonce"]
	s.pendingNonce[planID] = nonce
	if s.ambiguous[planID] {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"transient dispatch failure"}`)
		return
	}
	id := s.createApplyRunLocked(planID, nonce)
	fmt.Fprintf(w, `{"workflow_run_id":%d,"html_url":%q}`, id, fmt.Sprintf("https://github.com/%s/actions/runs/%d", s.repo, id))
}

func (s *fakeFleetGitHubServer) handleApplyRunsListLocked(w http.ResponseWriter) {
	items := make([]string, 0, len(s.runs))
	for _, run := range s.runs {
		if run.kind != "apply" {
			continue
		}
		items = append(items, fmt.Sprintf(`{"id":%d}`, run.id))
	}
	fmt.Fprintf(w, `{"workflow_runs":[%s]}`, strings.Join(items, ","))
}

func (s *fakeFleetGitHubServer) handleRunShowLocked(w http.ResponseWriter, idStr string) {
	id, _ := strconv.ParseInt(idStr, 10, 64)
	run, ok := s.runs[id]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	encodedInputs, _ := json.Marshal(run.inputs)
	fmt.Fprintf(w, `{"id":%d,"html_url":%q,"event":%q,"head_sha":%q,"head_branch":%q,"path":%q,"name":%q,"display_title":%q,"status":%q,"conclusion":%q,"run_attempt":%d,"inputs":%s,"actor":{"login":%q,"type":%q}}`,
		run.id, fmt.Sprintf("https://github.com/%s/actions/runs/%d", s.repo, run.id), run.event, run.headSHA, run.headBranch, run.path, run.name, run.displayTitle, run.status, run.conclusion, run.runAttempt, encodedInputs, run.actorLogin, run.actorType)
}
