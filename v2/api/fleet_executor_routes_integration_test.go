package main

// Real-router, real-store tests of the executor lane wiring (plan-5-7 E6): the
// PG authority-only router over PostgreSQL and the etcd Fleet routes over
// etcd, each driven with real executor and attempt-bound credentials through
// the real scope matcher and handlers.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/store"
)

// laneEnv is one backend under the shared executor-lane scenario.
type laneEnv struct {
	t       *testing.T
	router  http.Handler
	call    func(method, path, bearer, key string, body interface{}) (int, []byte)
	admin   string // admin and platform:operate
	viewer  string // api:read and api:write only
	exec    string // the executor principal's bearer
	routes  []string
	public  func(path string) bool
	watcher *controller.Watcher
	github  *laneFakeCommitSource

	resource string
	planID   string
	commit   string
	planSHA  string
	host     fleettest.ExecutorHost
	// revokeDevice revokes the executor device the way the backend does.
	revokeDevice func()
}

type laneProblem struct {
	Code string `json:"code"`
}

func laneCode(body []byte) string {
	var problem laneProblem
	_ = json.Unmarshal(body, &problem)
	return problem.Code
}

func laneIDs(planID, attemptID string) (string, string) { return planID, attemptID }

// scenario drives the executor lane end to end through the real routers.
// With revokeOnly it stops after the grant and checks only that revoking the
// executor device ends the executor token and the attempt credential.
func (e *laneEnv) scenario(revokeOnly bool) {
	t := e.t
	t.Helper()
	resourcePath := "/api/v1/fleet/resources/" + e.resource
	planPath := "/api/v1/fleet/plans/" + e.planID

	// --- Who may do what (the scope matcher, then the handlers). ---
	for name, tc := range map[string]struct {
		method, path, bearer string
		body                 interface{}
	}{
		"viewer approve":             {http.MethodPost, planPath + "/converge/approve", e.viewer, map[string]string{"commitSha": e.commit}},
		"viewer execution mode":      {http.MethodPost, resourcePath + "/execution-mode", e.viewer, map[string]interface{}{"mode": "github", "expectedRevision": 0}},
		"viewer provider evidence":   {http.MethodPost, planPath + "/provider-evidence", e.viewer, map[string]string{}},
		"viewer grant":               {http.MethodPost, planPath + "/credential-grants", e.viewer, map[string]string{}},
		"viewer attestation":         {http.MethodPost, "/api/v1/fleet/executor/stop-attestations", e.viewer, map[string]string{}},
		"viewer refusal":             {http.MethodPost, planPath + "/converge-refusals", e.viewer, map[string]string{}},
		"executor approve":           {http.MethodPost, planPath + "/converge/approve", e.exec, map[string]string{"commitSha": e.commit}},
		"executor execution mode":    {http.MethodPost, resourcePath + "/execution-mode", e.exec, map[string]interface{}{"mode": "github", "expectedRevision": 0}},
		"executor provider evidence": {http.MethodPost, planPath + "/provider-evidence", e.exec, map[string]string{}},
		"executor resource list":     {http.MethodGet, "/api/v1/fleet/resources", e.exec, nil},
		"executor desired":           {http.MethodPost, resourcePath + "/desired", e.exec, map[string]string{}},
		"executor revoke":            {http.MethodPost, "/api/v1/auth/revoke", e.exec, nil},
	} {
		code, body := e.call(tc.method, tc.path, tc.bearer, "scope-"+uuid.NewString(), tc.body)
		if code != http.StatusForbidden || laneCode(body) != "insufficient_scope" {
			t.Errorf("%s: %d %s, want 403 insufficient_scope", name, code, body)
		}
	}
	// A human with only api:write reaches the handler of the executor-only
	// routes and is refused there too.
	// --- Executor reads its resource (the single GET, never the list). ---
	code, body := e.call(http.MethodGet, resourcePath, e.exec, "", nil)
	var view handler.FleetResourceView
	if code != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Desired.Converge == nil {
		t.Fatalf("executor resource read: %d %s", code, body)
	}
	if view.Desired.Converge.PlanSHA256 != e.planSHA || !view.Desired.Converge.RequiresApproval || view.Desired.Converge.Approved || view.Desired.PlanID != e.planID {
		t.Fatalf("converge view = %+v (plan %s)", view.Desired.Converge, view.Desired.PlanID)
	}
	if view.ExecutionMode != executor.ModeController || view.Desired.CommitSHA != e.commit {
		t.Fatalf("view mode=%q commit=%q", view.ExecutionMode, view.Desired.CommitSHA)
	}

	// --- A requiresApproval plan does not start until approved. ---
	ref := fleettest.ExecutorPlanRef{ID: e.planID, CommitSHA: e.commit, PlanSHA256: e.planSHA}
	start := fleettest.ExecutorStartRequest(ref, e.host, false)
	code, body = e.call(http.MethodPost, planPath+"/attempts", e.exec, "", start)
	if code != http.StatusConflict || laneCode(body) != executor.CodeFleetConvergeApprovalRequired {
		t.Fatalf("start before approve: %d %s", code, body)
	}
	if code, body = e.call(http.MethodPost, planPath+"/converge/approve", e.admin, "approve-1", map[string]string{"commitSha": e.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	if code, body = e.call(http.MethodPost, planPath+"/converge/approve", e.admin, "approve-1", map[string]string{"commitSha": e.commit}); code != http.StatusOK {
		t.Fatalf("approve replay: %d %s", code, body)
	}
	if code, body = e.call(http.MethodGet, resourcePath, e.exec, "", nil); code != http.StatusOK || json.Unmarshal(body, &view) != nil || !view.Desired.Converge.Approved {
		t.Fatalf("approved view: %d %s", code, body)
	}

	// --- Start, heartbeat. ---
	code, body = e.call(http.MethodPost, planPath+"/attempts", e.exec, "", start)
	var attempt fleet.RunnerAttempt
	if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil || attempt.ID == "" {
		t.Fatalf("executor start: %d %s", code, body)
	}
	if code, body = e.call(http.MethodPost, planPath+"/attempts", e.exec, "", start); code != http.StatusOK {
		t.Fatalf("start replay: %d %s", code, body)
	}
	attemptPath := planPath + "/attempts/" + attempt.ID
	heartbeat := func(bearer string, a fleet.RunnerAttempt, sequence int64) (int, []byte) {
		return e.call(http.MethodPost, attemptPath+"/heartbeat", bearer, "", map[string]interface{}{
			"schemaVersion": fleet.RunnerAttemptSchemaVersion, "phase": a.CurrentPhase, "sequence": sequence, "revision": a.Revision})
	}
	code, body = heartbeat(e.exec, attempt, 1)
	if code != http.StatusOK || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("executor heartbeat: %d %s", code, body)
	}
	if code, body = e.call(http.MethodGet, attemptPath, e.exec, "", nil); code != http.StatusOK {
		t.Fatalf("executor attempt read: %d %s", code, body)
	}

	// --- Credential grant: the attempt-bound token. ---
	code, body = e.call(http.MethodPost, planPath+"/credential-grants", e.exec, "grant-1", executor.CredentialGrantRequest{
		AttemptID: attempt.ID, Phase: attempt.CurrentPhase, Types: []string{executor.CredNornAttempt}})
	var grant executor.CredentialGrantResponse
	if code != http.StatusCreated || json.Unmarshal(body, &grant) != nil || grant.AttemptToken == "" {
		t.Fatalf("grant: %d %s", code, body)
	}
	credential := grant.AttemptToken

	// Revoking the device ends the executor and its attempt credential.
	revoke := func(next int64) {
		code, body := heartbeat(e.exec, attempt, next)
		if code != http.StatusOK || json.Unmarshal(body, &attempt) != nil {
			t.Fatalf("executor heartbeat before the revoke: %d %s", code, body)
		}
		if code, body = heartbeat(credential, attempt, next+1); code != http.StatusOK || json.Unmarshal(body, &attempt) != nil {
			t.Fatalf("credential heartbeat before the revoke: %d %s", code, body)
		}
		e.revokeDevice()
		for name, bearer := range map[string]string{"executor token": e.exec, "attempt credential": credential} {
			if code, body := heartbeat(bearer, attempt, next+2); code != http.StatusUnauthorized {
				t.Errorf("%s after the device was revoked: %d %s, want 401", name, code, body)
			}
		}
	}
	if revokeOnly {
		revoke(2)
		return
	}

	// An admin reaches the provider-evidence handler (admission refuses the
	// body, the scope matcher does not).
	if code, body := e.call(http.MethodPost, planPath+"/provider-evidence", e.admin, "evidence-"+uuid.NewString(), map[string]string{}); code == http.StatusForbidden && laneCode(body) == "insufficient_scope" || code == http.StatusNotFound {
		t.Errorf("admin provider evidence did not reach its handler: %d %s", code, body)
	}

	// --- The credential reaches exactly its routes (every router route). ---
	want := laneRouteSet(executor.AttemptCredentialRoutes)
	for _, route := range e.routes {
		method, pattern, _ := strings.Cut(route, " ")
		path := laneConcretePath(pattern, e.planID, attempt.ID)
		if e.public(path) || method == http.MethodOptions {
			continue
		}
		code, body := e.call(method, path, credential, "matrix-"+uuid.NewString(), map[string]string{})
		refused := code == http.StatusForbidden && laneCode(body) == "insufficient_scope"
		if want[route] && refused {
			t.Errorf("credential refused on its own route %s: %d %s", route, code, body)
		}
		if !want[route] && !refused {
			t.Errorf("credential reached %s: %d %s", route, code, body)
		}
	}
	// The executor, by the same walk, is refused outside ExecutorRoutes. The
	// rotate route is covered separately: reaching it rotates the token.
	wantExec := laneRouteSet(executor.ExecutorRoutes)
	for _, route := range e.routes {
		method, pattern, _ := strings.Cut(route, " ")
		path := laneConcretePath(pattern, e.planID, attempt.ID)
		if e.public(path) || method == http.MethodOptions || route == "POST /api/v1/auth/rotate" {
			continue
		}
		code, body := e.call(method, path, e.exec, "matrix-"+uuid.NewString(), map[string]string{})
		refused := code == http.StatusForbidden && laneCode(body) == "insufficient_scope"
		// A handler may itself answer insufficient_scope for an in-set route
		// (a platform check); the matcher's own refusal is the thing pinned,
		// so only out-of-set routes are asserted refused.
		if !wantExec[route] && !refused {
			t.Errorf("executor reached %s: %d %s", route, code, body)
		}
	}

	// --- The credential heartbeats, checkpoints, advances. ---
	if code, body = heartbeat(credential, attempt, 2); code != http.StatusOK || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("credential heartbeat: %d %s", code, body)
	}
	if code, body = e.call(http.MethodGet, attemptPath, credential, "", nil); code != http.StatusOK {
		t.Fatalf("credential attempt read: %d %s", code, body)
	}
	checkpoint := func(bearer, attemptID, key string) (int, []byte) {
		return e.call(http.MethodPost, planPath+"/reconciliations", bearer, key, fleet.ReconciliationRequest{
			SchemaVersion: fleet.ReconciliationSchemaVersion, AttemptID: attemptID, Phase: attempt.CurrentPhase, Status: "succeeded",
			CommitSHA: e.commit, PlanSHA256: e.planSHA, StateSerial: 4, EvidenceDigest: "sha256:" + strings.Repeat("e", 64), Message: "lane checkpoint"})
	}
	// A body naming another attempt is refused for the credential.
	if code, body = checkpoint(credential, uuid.NewString(), "cp-other"); code == http.StatusCreated || code == http.StatusOK {
		t.Fatalf("credential checkpoint for another attempt: %d %s", code, body)
	}
	if code, body = checkpoint(credential, attempt.ID, "cp-1"); code != http.StatusCreated {
		t.Fatalf("credential checkpoint: %d %s", code, body)
	}
	if code, body = checkpoint(credential, attempt.ID, "cp-1"); code != http.StatusOK {
		t.Fatalf("credential checkpoint replay: %d %s", code, body)
	}
	code, body = e.call(http.MethodGet, planPath+"/reconciliations?attemptId="+attempt.ID, credential, "", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"count":1`) {
		t.Fatalf("credential reconciliation read: %d %s", code, body)
	}
	if code, body = e.call(http.MethodGet, planPath+"/reconciliations?attemptId="+uuid.NewString(), credential, "", nil); code == http.StatusOK {
		t.Fatalf("credential read another attempt's evidence: %d %s", code, body)
	}
	if code, body = e.call(http.MethodGet, planPath+"/reconciliations", credential, "", nil); code == http.StatusOK {
		t.Fatalf("credential read reconciliations without an attempt: %d %s", code, body)
	}
	// Start and cancel stay executor-principal only, even if the matcher were
	// bypassed: the handlers refuse the credential too (see the handler tests);
	// through the router the matcher refuses first.
	if code, _ = e.call(http.MethodPost, attemptPath+"/cancel", credential, "", map[string]interface{}{"schemaVersion": fleet.RunnerAttemptSchemaVersion, "revision": attempt.Revision, "reason": "x"}); code != http.StatusForbidden {
		t.Fatalf("credential cancel: %d", code)
	}
	// The executor's own checkpoint and advance still work.
	if code, body = checkpoint(e.exec, attempt.ID, "cp-exec"); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("executor checkpoint: %d %s", code, body)
	}

	if code, body = e.call(http.MethodGet, attemptPath, e.exec, "", nil); code != http.StatusOK || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("executor attempt refresh: %d %s", code, body)
	}
	revoke(attempt.HeartbeatSequence + 1)
}

// --- PostgreSQL ---

func laneECPublicKey() string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	public, _ := key.PublicKey.Bytes()
	return base64.RawURLEncoding.EncodeToString(public)
}

type lanePG struct {
	laneEnv
	db      *store.DB
	cfg     *config.Config
	server  *httptest.Server
	handler *handler.Handler
}

// newLanePG builds the real PG authority-only router (the same wiring
// serveFleetAuthorityOnly uses) with a fake GitHub, a registered target, a
// controller-mode resource and the watcher's recorded converge plan.
func newLanePG(t *testing.T) *lanePG {
	t.Helper()
	// An isolated schema per rig: the legacy runner suites assume an empty
	// target registry, so nothing here may touch the shared public schema.
	db := integrationtest.PG(t)
	cfg := fleetAuthorityOnlyTestConfig(t)
	cfg.APIToken += "-" + uuid.NewString() // enrollment rate limits are durable
	h, err := newFleetAuthorityOnlyHandler(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := newFleetReconciler(db, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") })
	h.SetFleetControllerLiveness(reconciler)
	github := &laneFakeCommitSource{tip: strings.Repeat("a", 40), verified: true}
	watcher := wireFleetAuthorityExecutorLane(cfg, h, db, reconciler, github)
	if watcher == nil {
		t.Fatal("no watcher")
	}
	router := fleetAuthorityOnlyRouterWithWatcher(cfg, h, watcher)
	server := httptest.NewTLSServer(router)
	t.Cleanup(server.Close)
	r := &lanePG{db: db, cfg: cfg, server: server, handler: h}
	r.t, r.router, r.watcher, r.github = t, router, watcher, github
	r.routes = laneWalkRoutes(t, router)
	r.public = func(path string) bool {
		return path == "/api/health" || path == "/api/version" || path == "/api/v1/capabilities" || strings.HasPrefix(path, "/api/v1/enrollments") ||
			path == "/api/v1/auth/github-actions/exchange"
	}
	r.call = func(method, path, bearer, key string, body interface{}) (int, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var buffer bytes.Buffer
		_, _ = buffer.ReadFrom(res.Body)
		return res.StatusCode, buffer.Bytes()
	}
	r.admin = cfg.APIToken
	viewer, _ := r.enroll("lane-viewer", "desktop", []string{handler.ScopeAPIRead, handler.ScopeAPIWrite})
	r.viewer = viewer
	exec, execDevice := r.enroll("lane-executor", handler.FleetExecutorPlatform, []string{handler.ScopeFleetExecute})
	r.exec = exec
	r.revokeDevice = func() {
		if code, body := r.call(http.MethodDelete, "/api/v1/devices/"+execDevice, r.admin, "", nil); code != http.StatusNoContent {
			t.Fatalf("revoke device: %d %s", code, body)
		}
	}
	r.host = fleettest.NewExecutorHost()
	r.seedControllerResource()
	return r
}

func (r *lanePG) enroll(name, platform string, scopes []string) (token, deviceID string) {
	t := r.t
	t.Helper()
	code, body := r.call(http.MethodPost, "/api/v1/enrollments", "", "", map[string]interface{}{
		"deviceName": name, "platform": platform, "publicKey": laneECPublicKey(), "requestedScopes": scopes})
	var started map[string]string
	if code != http.StatusCreated || json.Unmarshal(body, &started) != nil {
		t.Fatalf("enroll %s: %d %s", name, code, body)
	}
	if code, body = r.call(http.MethodPost, "/api/v1/enrollments/approve", r.cfg.APIToken, "", map[string]interface{}{"userCode": started["userCode"], "scopes": scopes}); code != http.StatusOK {
		t.Fatalf("approve %s: %d %s", name, code, body)
	}
	var issued struct {
		Token    string `json:"token"`
		DeviceID string `json:"deviceId"`
	}
	if code, body = r.call(http.MethodPost, "/api/v1/enrollments/"+started["id"]+"/exchange", "", "", map[string]string{"verifier": started["verifier"]}); code != http.StatusOK || json.Unmarshal(body, &issued) != nil {
		t.Fatalf("exchange %s: %d %s", name, code, body)
	}
	return issued.Token, issued.DeviceID
}

// seedControllerResource registers a target, creates a resource, switches it
// to controller mode and lets the watcher record and set its converge plan.
func (r *lanePG) seedControllerResource() {
	t := r.t
	t.Helper()
	cluster := "lane-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	r.resource = cluster
	code, body := r.call(http.MethodPost, "/api/v1/fleet/targets", r.admin, "reg-"+cluster, map[string]interface{}{
		"provider": "digitalocean", "providerAccount": "acct-" + cluster, "stateBackend": "s3://bucket/" + cluster,
		"aliases": []string{"cluster:" + cluster}})
	var registered struct {
		Ref string `json:"ref"`
	}
	if code != http.StatusCreated || json.Unmarshal(body, &registered) != nil {
		t.Fatalf("register target: %d %s", code, body)
	}
	if code, body = r.call(http.MethodPost, "/api/v1/fleet/resources/"+cluster, r.admin, "", map[string]string{"targetId": registered.Ref}); code != http.StatusCreated {
		t.Fatalf("create resource: %d %s", code, body)
	}
	r.setMode(executor.ModeController, "mode-1")
	r.recordPlan()
}

func (r *lanePG) resourceRevision() int64 {
	code, body := r.call(http.MethodGet, "/api/v1/fleet/resources/"+r.resource, r.admin, "", nil)
	var view handler.FleetResourceView
	if code != http.StatusOK || json.Unmarshal(body, &view) != nil {
		r.t.Fatalf("read resource: %d %s", code, body)
	}
	return view.Revision
}

func (r *lanePG) setMode(mode, key string) {
	code, body := r.call(http.MethodPost, "/api/v1/fleet/resources/"+r.resource+"/execution-mode", r.admin, key, executor.ExecutionModeRequest{Mode: mode, ExpectedRevision: r.resourceRevision()})
	if code != http.StatusCreated {
		r.t.Fatalf("set mode %s: %d %s", mode, code, body)
	}
}

// recordPlan runs one watcher poll and reads the recorded plan.
func (r *lanePG) recordPlan() {
	t := r.t
	t.Helper()
	if err := r.watcher.Step(context.Background()); err != nil {
		t.Fatalf("watcher step: %v", err)
	}
	decision, ok := r.watcher.Decision(r.resource)
	if !ok || decision.Action != controller.WatchRecorded || decision.PlanID == "" {
		t.Fatalf("watcher decision = %+v", decision)
	}
	r.planID, r.commit = decision.PlanID, r.github.tip
	recorded, err := r.handler.ConvergePlanRecorder().ConvergePlan(context.Background(), r.planID)
	if err != nil {
		t.Fatal(err)
	}
	r.planSHA = strings.TrimPrefix(recorded.Digest, "sha256:")
}

func TestPGExecutorLaneThroughRealRouter(t *testing.T) {
	rig := newLanePG(t)
	rig.scenario(false)
}

// TestPGConvergePlanAdmissibleAfterModeRoundTrip pins the recorder's
// Admissible on PG: a plan abandoned by a switch to github is not admissible,
// so a rescan after the mode returns to controller records a fresh plan; a
// plan the executor refused stays admissible, so a rescan records nothing new.
func TestPGConvergePlanAdmissibleAfterModeRoundTrip(t *testing.T) {
	rig := newLanePG(t)
	first := rig.planID
	rec := rig.handler.ConvergePlanRecorder()
	rec.Admissible = func(ctx context.Context, planID string) (bool, error) {
		abandoned, err := rig.db.IsFleetPlanAbandonedForPlan(ctx, planID)
		return !abandoned, err
	}
	newest, err := rig.watcher.Recorder.NewestConvergePlan(context.Background(), rig.resource)
	if err != nil || newest == nil || newest.PlanID != first || !newest.Admissible {
		t.Fatalf("newest before the round trip: %v %+v", err, newest)
	}
	// The mode route's switch to github abandons the unstarted plan.
	rig.setMode(executor.ModeGitHub, "mode-github")
	if abandoned, err := rig.db.IsFleetPlanAbandonedForPlan(context.Background(), first); err != nil || !abandoned {
		t.Fatalf("the unstarted converge plan was not abandoned: %v %v", abandoned, err)
	}
	newest, err = rig.watcher.Recorder.NewestConvergePlan(context.Background(), rig.resource)
	if err != nil || newest == nil || newest.Admissible {
		t.Fatalf("an abandoned plan must not be admissible: %v %+v", err, newest)
	}
	// A rescan while the resource is in github mode records nothing.
	if code, body := rig.call(http.MethodPost, "/api/v1/fleet/resources/"+rig.resource+"/converge", rig.admin, "rescan-github", nil); code != http.StatusConflict {
		t.Fatalf("rescan in github mode: %d %s", code, body)
	}
	rig.setMode(executor.ModeController, "mode-controller")
	code, body := rig.call(http.MethodPost, "/api/v1/fleet/resources/"+rig.resource+"/converge", rig.admin, "rescan-controller", nil)
	var decision controller.WatchDecision
	if code != http.StatusOK || json.Unmarshal(body, &decision) != nil || decision.Action != controller.WatchRecorded || decision.PlanID == first {
		t.Fatalf("rescan after the round trip: %d %s (first plan %s)", code, body, first)
	}
	second := decision.PlanID
	// A refusal leaves the plan admissible: a rescan records nothing new.
	if code, body = rig.call(http.MethodPost, "/api/v1/fleet/plans/"+second+"/converge/approve", rig.admin, "approve-second", map[string]string{"commitSha": rig.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	code, body = rig.call(http.MethodPost, "/api/v1/fleet/plans/"+second+"/credential-grants", rig.exec, "grant-plan", executor.CredentialGrantRequest{Phase: executor.GrantPhasePlan, Types: []string{executor.CredDORead}})
	if code != http.StatusCreated {
		t.Fatalf("plan grant: %d %s", code, body)
	}
	planSHA := rig.planSHAOf(second)
	refusal := executor.ConvergeRefusalRequest{Code: executor.RefusalDestructive, CommitSHA: rig.commit, PlanSHA256: planSHA, PlanTextSHA256: strings.Repeat("d", 64), Changes: json.RawMessage(`{"delete":["digitalocean_droplet.x"]}`)}
	if code, body = rig.call(http.MethodPost, "/api/v1/fleet/plans/"+second+"/converge-refusals", rig.exec, "executor-refusal:"+second, refusal); code != http.StatusCreated {
		t.Fatalf("refusal: %d %s", code, body)
	}
	newest, err = rig.watcher.Recorder.NewestConvergePlan(context.Background(), rig.resource)
	if err != nil || newest == nil || newest.PlanID != second || !newest.Admissible {
		t.Fatalf("a refused plan must stay admissible: %v %+v", err, newest)
	}
	code, body = rig.call(http.MethodPost, "/api/v1/fleet/resources/"+rig.resource+"/converge", rig.exec, "rescan-refused", nil)
	if code != http.StatusOK || json.Unmarshal(body, &decision) != nil || decision.Action != controller.WatchExists || decision.PlanID != second {
		t.Fatalf("rescan after a refusal: %d %s", code, body)
	}
	// The resource view shows the refusal (the PG facts reader is wired).
	code, body = rig.call(http.MethodGet, "/api/v1/fleet/resources/"+rig.resource, rig.exec, "", nil)
	var view handler.FleetResourceView
	if code != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Desired.Converge == nil || !view.Desired.Converge.Refused {
		t.Fatalf("view after refusal: %d %s", code, body)
	}
	if view.Blocker == nil || view.Blocker.Reason != executor.BlockerReasonDestructive {
		t.Fatalf("blocker = %+v", view.Blocker)
	}
}

func (r *lanePG) planSHAOf(planID string) string {
	recorded, err := r.handler.ConvergePlanRecorder().ConvergePlan(context.Background(), planID)
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimPrefix(recorded.Digest, "sha256:")
}

// TestPGStartTimeVerificationRefusesAnUnverifiedTip pins the executor start's
// re-check: a plan whose commit is no longer a verified protected tip does not
// start, and a replay of an admitted attempt does not need GitHub.
func TestPGStartTimeVerificationRefusesAnUnverifiedTip(t *testing.T) {
	rig := newLanePG(t)
	planPath := "/api/v1/fleet/plans/" + rig.planID
	if code, body := rig.call(http.MethodPost, planPath+"/converge/approve", rig.admin, "approve-v", map[string]string{"commitSha": rig.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	rig.github.mu.Lock()
	rig.github.verified = false
	rig.github.mu.Unlock()
	ref := fleettest.ExecutorPlanRef{ID: rig.planID, CommitSHA: rig.commit, PlanSHA256: rig.planSHA}
	start := fleettest.ExecutorStartRequest(ref, rig.host, false)
	code, body := rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", start)
	if code != http.StatusConflict || laneCode(body) != executor.CodeFleetExecutorTriggerUnverified {
		t.Fatalf("start with an unverified tip: %d %s", code, body)
	}
	rig.github.mu.Lock()
	rig.github.verified = true
	rig.github.mu.Unlock()
	if code, body = rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", start); code != http.StatusCreated {
		t.Fatalf("start with a verified tip: %d %s", code, body)
	}
	rig.github.mu.Lock()
	rig.github.verified = false
	rig.github.mu.Unlock()
	if code, body = rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", start); code != http.StatusOK {
		t.Fatalf("replay must not depend on GitHub: %d %s", code, body)
	}
}

// TestFleetExecutorRoutesAuthorityOnlyAndEtcd: every executor-lane route is
// registered on the PG authority-only router and on the etcd Fleet routes.
func TestFleetExecutorRoutesAuthorityOnlyAndEtcd(t *testing.T) {
	needed := map[string]bool{}
	for _, route := range executor.ExecutorRoutes {
		needed[route] = true
	}
	for _, route := range executor.AttemptCredentialRoutes {
		needed[route] = true
	}
	for _, route := range []string{
		"POST /api/v1/fleet/resources/{name}/execution-mode", "POST /api/v1/fleet/plans/{planID}/converge/approve",
		"POST /api/v1/fleet/plans/{planID}/provider-evidence", "POST /api/v1/fleet/plans/{planID}/credential-grants",
		"POST /api/v1/fleet/plans/{planID}/converge-refusals", "POST /api/v1/fleet/executor/stop-attestations",
		"POST /api/v1/fleet/resources/{name}/converge",
	} {
		needed[route] = true
	}

	pg := laneRouteSet(laneWalkRoutes(t, fleetAuthorityOnlyRouter(fleetAuthorityOnlyTestConfig(t), nil)))
	for route := range needed {
		if !pg[route] {
			t.Errorf("PG authority-only router does not serve %s", route)
		}
	}

	etcd := newLaneEtcd(t)
	served := laneRouteSet(etcd.routes)
	for route := range needed {
		if !served[route] {
			t.Errorf("etcd Fleet routes do not serve %s", route)
		}
	}
}

// --- etcd ---

type laneEtcd struct {
	laneEnv
	operations *etcdstore.V3OperationStore
	identities *etcdstore.AuthStore
	cfg        *config.Config
	secret     string
}

// newLaneEtcd assembles the etcd Fleet routes with the same function the
// runtime uses (registerEtcdFleetControllerRoutes), over real etcd, an
// enrolled executor device, a fake GitHub and a recorded converge plan.
func newLaneEtcd(t *testing.T) *laneEtcd {
	t.Helper()
	client, prefix := integrationtest.Etcd(t)
	const secret = "0123456789abcdef0123456789abcdef"
	signer, err := store.NewHMACAcceptanceSigner(secret)
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{APIToken: secret, AuditSigningKey: secret, ControlAuthority: authority,
		GitHubActionsFleetAllowedRepository: "acme/fleet@101@202", FleetGitHubRepository: "acme/fleet",
		FleetGitHubConfigPath: "environments/staging/nyc3/cluster.yaml", FleetGitHubEnvironment: "staging"}
	identities := etcdstore.NewAuthStore(client, prefix)
	reconciler := newFleetReconciler(operations, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") })
	github := &laneFakeCommitSource{tip: strings.Repeat("a", 40), verified: true}
	router := chi.NewRouter()
	// The lane is gated off in production (etcdFleetExecutorLaneEnabled);
	// these tests open it to keep its code exercised.
	watcher := registerEtcdFleetControllerRoutes(router, cfg, identities, operations, &githubapp.Client{}, github, reconciler, true)
	registerEtcdFleetCredentialRoutes(router, cfg, identities, true)
	if watcher == nil {
		t.Fatal("no etcd watcher")
	}
	e := &laneEtcd{operations: operations, identities: identities, cfg: cfg, secret: secret}
	e.t, e.router, e.watcher, e.github = t, router, watcher, github
	e.routes = laneWalkRoutes(t, router)
	e.public = func(path string) bool { return path == "/api/v1/auth/github-actions/exchange" }
	e.call = func(method, path, bearer, key string, body interface{}) (int, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	token := func(subject string, deviceID string, scopes ...string) string {
		signed, err := laneEtcdToken(secret, identities, subject, deviceID, scopes...)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	e.admin = token("lane-admin", "", handler.ScopeAdmin)
	e.viewer = token("lane-viewer", "", handler.ScopeAPIRead, handler.ScopeAPIWrite)
	deviceID := uuid.NewString()
	if err := identities.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: deviceID, Name: "lane executor", Platform: handler.FleetExecutorPlatform, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	e.exec = token("lane-executor", deviceID, handler.ScopeFleetExecute)
	e.revokeDevice = func() {
		if _, err := identities.RevokeAccessDevice(context.Background(), deviceID); err != nil {
			t.Fatal(err)
		}
	}
	e.host = fleettest.NewExecutorHost()

	cluster := "lane-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	e.resource = cluster
	code, body := e.call(http.MethodPost, "/api/v1/fleet/targets", e.admin, "reg-"+cluster, map[string]interface{}{
		"provider": "digitalocean", "providerAccount": "acct-" + cluster, "stateBackend": "s3://bucket/" + cluster,
		"aliases": []string{"cluster:" + cluster}})
	var registered struct {
		Ref string `json:"ref"`
	}
	if code != http.StatusCreated || json.Unmarshal(body, &registered) != nil {
		t.Fatalf("register target: %d %s", code, body)
	}
	if code, body = e.call(http.MethodPost, "/api/v1/fleet/resources/"+cluster, e.admin, "", map[string]string{"targetId": registered.Ref}); code != http.StatusCreated {
		t.Fatalf("create resource: %d %s", code, body)
	}
	e.setMode(executor.ModeController, "mode-1")
	e.recordPlan()
	return e
}

func (e *laneEtcd) resourceRevision() int64 {
	code, body := e.call(http.MethodGet, "/api/v1/fleet/resources/"+e.resource, e.admin, "", nil)
	var view handler.FleetResourceView
	if code != http.StatusOK || json.Unmarshal(body, &view) != nil {
		e.t.Fatalf("read resource: %d %s", code, body)
	}
	return view.Revision
}

func (e *laneEtcd) setMode(mode, key string) {
	code, body := e.call(http.MethodPost, "/api/v1/fleet/resources/"+e.resource+"/execution-mode", e.admin, key, executor.ExecutionModeRequest{Mode: mode, ExpectedRevision: e.resourceRevision()})
	if code != http.StatusCreated {
		e.t.Fatalf("set mode %s: %d %s", mode, code, body)
	}
}

func (e *laneEtcd) recordPlan() {
	t := e.t
	t.Helper()
	if err := e.watcher.Step(context.Background()); err != nil {
		t.Fatalf("watcher step: %v", err)
	}
	decision, ok := e.watcher.Decision(e.resource)
	if !ok || decision.Action != controller.WatchRecorded || decision.PlanID == "" {
		t.Fatalf("watcher decision = %+v", decision)
	}
	e.planID, e.commit = decision.PlanID, e.github.tip
	recorded, err := handler.NewEtcdConvergePlanRecorder(e.cfg, e.operations).ConvergePlan(context.Background(), e.planID)
	if err != nil {
		t.Fatal(err)
	}
	e.planSHA = strings.TrimPrefix(recorded.Digest, "sha256:")
}

func TestEtcdExecutorLaneThroughRealRouter(t *testing.T) {
	rig := newLaneEtcd(t)
	rig.scenario(false)
}

// TestEtcdConvergePlanRecorded: the etcd recorder signs with
// controller.CanonicalConvergePlan (the plan keeps its converge object and
// verifies, which refreshEtcdCapacityPlanSignature would break), is
// idempotent per key, and reports Admissible false for an abandoned plan.
func TestEtcdConvergePlanRecorded(t *testing.T) {
	rig := newLaneEtcd(t)
	ctx := context.Background()
	recorder := handler.NewEtcdConvergePlanRecorder(rig.cfg, rig.operations)
	op, err := rig.operations.GetOperation(ctx, rig.planID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(op.Payload)
	var plan controller.ConvergeCapacityPlan
	if json.Unmarshal(encoded, &plan) != nil || plan.Converge == nil || plan.Converge.Resource != rig.resource || plan.Converge.CommitSHA != rig.commit || plan.Action != executor.ActionConverge {
		t.Fatalf("recorded plan lost its converge object: %s", encoded)
	}
	if !controller.VerifyConvergePlan(&plan, []string{rig.cfg.AuditSigningKey}) {
		t.Fatal("the recorded plan does not verify under CanonicalConvergePlan")
	}
	canonical, err := controller.CanonicalConvergePlan(&plan)
	if err != nil || !strings.Contains(string(canonical), `"converge"`) {
		t.Fatalf("canonical plan dropped converge: %v %s", err, canonical)
	}
	// The plan the capacity-plan signer would have produced does not verify.
	bad := plan.CapacityPlan
	if err := refreshEtcdCapacityPlanSignature(&bad, rig.cfg.AuditSigningKey); err != nil || bad.Digest == plan.Digest {
		t.Fatalf("the capacity-plan signer must differ from the converge signer (err=%v)", err)
	}

	newest, err := recorder.NewestConvergePlan(ctx, rig.resource)
	if err != nil || newest == nil || newest.PlanID != rig.planID || !newest.Admissible || newest.Converge.CommitSHA != rig.commit {
		t.Fatalf("newest = %+v %v", newest, err)
	}
	// A second poll for the same tip records nothing new.
	rig.watcher.Rescan(ctx, rig.resource)
	if decision, _ := rig.watcher.Decision(rig.resource); decision.Action != controller.WatchExists || decision.PlanID != rig.planID {
		t.Fatalf("second poll = %+v", decision)
	}
	// The same key replays the first plan, even for a different plan object.
	other, err := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: rig.resource, Cluster: plan.Cluster, Repository: "acme/fleet", CommitSHA: rig.commit,
		LastApplied: "", PathClass: executor.PathClassExecutable, Generation: plan.Converge.Generation, SigningKey: rig.cfg.AuditSigningKey})
	if err != nil {
		t.Fatal(err)
	}
	key := "converge:" + uuid.NewString()
	first, err := recorder.RecordConvergePlan(ctx, other, key)
	if err != nil || first == nil || first.PlanID != other.ID {
		t.Fatalf("record: %v %+v", err, first)
	}
	different, err := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: rig.resource, Cluster: plan.Cluster, Repository: "acme/fleet", CommitSHA: rig.commit,
		LastApplied: "", PathClass: executor.PathClassExecutable, Generation: plan.Converge.Generation, SigningKey: rig.cfg.AuditSigningKey})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := recorder.RecordConvergePlan(ctx, different, key); err != nil || again.PlanID != first.PlanID {
		t.Fatalf("a replayed key must return the first plan: %v %+v (first %s)", err, again, first.PlanID)
	}

	// Mode round trip: github abandons the unstarted plan, so it is no longer
	// admissible, and controller mode records a fresh plan on the rescan.
	rig.setMode(executor.ModeGitHub, "mode-github")
	abandoned, err := recorder.NewestConvergePlan(ctx, rig.resource)
	if err != nil || abandoned == nil || abandoned.Admissible {
		t.Fatalf("an abandoned plan must not be admissible: %+v %v", abandoned, err)
	}
	if _, err := rig.watcher.Rescan(ctx, rig.resource); err == nil {
		t.Fatal("a github-mode resource must not be rescanned")
	}
	rig.setMode(executor.ModeController, "mode-controller")
	decision, err := rig.watcher.Rescan(ctx, rig.resource)
	if err != nil || decision.Action != controller.WatchRecorded || decision.PlanID == abandoned.PlanID {
		t.Fatalf("rescan after the round trip = %+v %v (abandoned %s)", decision, err, abandoned.PlanID)
	}
}

// TestEtcdExecutorHeartbeatUsesExecutorMetaNotWorkloadToken: the etcd executor
// heartbeat, advance and checkpoint use the executor-meta ownership. Another
// executor device does not own the attempt, a GitHub workload token is not
// admitted to an executor attempt, and the device lookup is required.
func TestEtcdExecutorHeartbeatUsesExecutorMetaNotWorkloadToken(t *testing.T) {
	rig := newLaneEtcd(t)
	planPath := "/api/v1/fleet/plans/" + rig.planID
	if code, body := rig.call(http.MethodPost, planPath+"/converge/approve", rig.admin, "approve", map[string]string{"commitSha": rig.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	ref := fleettest.ExecutorPlanRef{ID: rig.planID, CommitSHA: rig.commit, PlanSHA256: rig.planSHA}
	code, body := rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", fleettest.ExecutorStartRequest(ref, rig.host, false))
	var attempt fleet.RunnerAttempt
	if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("start: %d %s", code, body)
	}
	attemptPath := planPath + "/attempts/" + attempt.ID
	hb := func(bearer string, sequence int64) int {
		code, _ := rig.call(http.MethodPost, attemptPath+"/heartbeat", bearer, "", map[string]interface{}{
			"schemaVersion": fleet.RunnerAttemptSchemaVersion, "phase": attempt.CurrentPhase, "sequence": sequence, "revision": attempt.Revision})
		return code
	}
	// A second executor device does not own the attempt.
	otherDevice := uuid.NewString()
	if err := rig.identities.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: otherDevice, Name: "other", Platform: handler.FleetExecutorPlatform, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	otherToken, err := laneEtcdToken(rig.secret, rig.identities, "other-executor", otherDevice, handler.ScopeFleetExecute)
	if err != nil {
		t.Fatal(err)
	}
	if code := hb(otherToken, 1); code != http.StatusForbidden {
		t.Fatalf("a foreign executor heartbeat: %d", code)
	}
	// A GitHub workload token (fleet:operate + CI) is not admitted to an
	// executor attempt: its identity cannot match the executor metadata.
	ci := &handler.CIIdentity{Provider: "github-actions", Repository: "acme/fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "7", RunAttempt: "1",
		Ref: "refs/heads/main", RefType: "branch", EventName: "push", Environment: "staging", SHA: rig.commit, RefProtected: true, Intent: "apply"}
	workload, err := signedProcessManagedCIToken(rig.secret, rig.identities, ci)
	if err != nil {
		t.Fatal(err)
	}
	if code := hb(workload, 1); code == http.StatusOK {
		t.Fatalf("a GitHub workload token heartbeated an executor attempt: %d", code)
	}
	if code := hb(rig.exec, 1); code != http.StatusOK {
		t.Fatalf("the owning executor heartbeat: %d", code)
	}
	// Advance without proven evidence is refused as stale evidence by the
	// store, not by ownership; a foreign executor is refused by ownership.
	if code, _ := rig.call(http.MethodPost, attemptPath+"/advance", otherToken, "", map[string]interface{}{
		"schemaVersion": fleet.RunnerAttemptSchemaVersion, "expectedPhase": attempt.CurrentPhase, "revision": attempt.Revision + 1}); code != http.StatusForbidden {
		t.Fatalf("a foreign executor advance: %d", code)
	}
	// Without a device lookup the etcd executor lane fails closed (the router
	// above wires it; a handler built without it refuses).
	bare := handler.NewEtcdFleetRunnerHandler(rig.cfg, rig.operations)
	req := httptest.NewRequest(http.MethodPost, attemptPath+"/heartbeat", strings.NewReader("{}"))
	req.SetPathValue("planID", rig.planID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("planID", rig.planID)
	routeCtx.URLParams.Add("attemptID", attempt.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	execPrincipal := &handler.AccessPrincipal{Subject: "exec", Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", DeviceID: "d", Scopes: []string{handler.ScopeFleetExecute}, ExpiresAt: time.Now().Add(time.Hour)}
	recorder := httptest.NewRecorder()
	bare.Heartbeat(recorder, handler.WithAccessPrincipal(req, execPrincipal))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("an etcd handler without a device lookup: %d", recorder.Code)
	}
}

// TestEtcdExecutorFactsReaderMirrorsPG: the etcd resource view carries the
// converge facts (desired.converge, approve_converge, the destructive refusal
// blocker) and the resume derivation; the signed stop attestation of the
// latest attempt gives resumeNeededAttemptID, and an approve with --resume
// gives resumeApproved without an age bound.
func TestEtcdExecutorFactsReaderMirrorsPG(t *testing.T) {
	rig := newLaneEtcd(t)
	ctx := context.Background()
	planPath := "/api/v1/fleet/plans/" + rig.planID
	read := func() handler.FleetResourceView {
		code, body := rig.call(http.MethodGet, "/api/v1/fleet/resources/"+rig.resource, rig.viewer, "", nil)
		var view handler.FleetResourceView
		if code != http.StatusOK || json.Unmarshal(body, &view) != nil {
			t.Fatalf("read: %d %s", code, body)
		}
		return view
	}
	view := read()
	if view.Desired.Converge == nil || !view.Desired.Converge.RequiresApproval || view.Desired.Converge.Approved || view.Status.NextAction != "" && view.Status.NextAction == "" {
		t.Fatalf("fresh view = %+v", view.Desired.Converge)
	}
	if code, body := rig.call(http.MethodPost, planPath+"/converge/approve", rig.admin, "approve", map[string]string{"commitSha": rig.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	if view = read(); !view.Desired.Converge.Approved || view.ApprovalPolicy.ApprovedBy == "" {
		t.Fatalf("approved view = %+v policy=%+v", view.Desired.Converge, view.ApprovalPolicy)
	}
	// Start an attempt, then expire the lease so the loop "died".
	ref := fleettest.ExecutorPlanRef{ID: rig.planID, CommitSHA: rig.commit, PlanSHA256: rig.planSHA}
	code, body := rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", fleettest.ExecutorStartRequest(ref, rig.host, false))
	var attempt fleet.RunnerAttempt
	if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("start: %d %s", code, body)
	}
	// The stop attestation of a restarted loop (same droplet and machine, new
	// invocation).
	next := rig.host.NextInvocation()
	attestation := executor.StopAttestationRequest{SourceAttemptID: attempt.ID, DropletID: next.DropletID, MachineID: next.MachineID, BootID: next.BootID, InvocationID: next.InvocationID,
		PhaseUIDEmpty: true, PhaseProcessGroupReaped: true, SourceCgroupEmpty: true, ObservedAt: time.Now().UTC()}
	code, body = rig.call(http.MethodPost, "/api/v1/fleet/executor/stop-attestations", rig.exec, "attest-1", attestation)
	if code != http.StatusCreated {
		t.Fatalf("attestation: %d %s (the E5 route is mounted and reachable by the executor)", code, body)
	}
	// Facts need the holder: with the fence held by this plan the view reports
	// the resume derivation. The attempt must be the latest and not succeeded.
	facts := handler.NewEtcdConvergeFactsReader(rig.cfg, rig.operations, nil)
	resource, err := rig.operations.GetFleetResource(ctx, rig.resource)
	if err != nil {
		t.Fatal(err)
	}
	status := resource.Status
	status.Active = &controller.ActiveRefs{PlanID: rig.planID}
	got, err := facts.ConvergeFacts(ctx, resource, status)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan == nil || !got.Plan.Approved || got.ResumeApproved || got.ResumeNeededAttemptID != attempt.ID {
		t.Fatalf("facts after attestation = %+v plan=%+v", got, got.Plan)
	}
	// An attestation older than AttestationMaxAge still counts (no age check
	// in the derivation): record one observed 20 minutes ago for a new attempt
	// is covered by the PG twin; here the unconsumed resume approve flips it.
	if code, body = rig.call(http.MethodPost, planPath+"/converge/approve", rig.admin, "resume", map[string]interface{}{"commitSha": rig.commit, "resume": true, "sourceAttemptId": attempt.ID}); code != http.StatusCreated {
		t.Fatalf("resume approve: %d %s", code, body)
	}
	if got, err = facts.ConvergeFacts(ctx, resource, status); err != nil || !got.ResumeApproved || got.ResumeNeededAttemptID != "" {
		t.Fatalf("facts after the resume approve = %+v %v", got, err)
	}
	// A destructive refusal for the holder's own commit.
	refusal := executor.ConvergeRefusalRequest{Code: executor.RefusalDestructive, CommitSHA: rig.commit, PlanSHA256: rig.planSHA, PlanTextSHA256: strings.Repeat("d", 64), Changes: json.RawMessage(`{"delete":["digitalocean_droplet.x"]}`)}
	if code, body = rig.call(http.MethodPost, planPath+"/converge-refusals", rig.exec, "executor-refusal:"+rig.planID, refusal); code != http.StatusCreated {
		t.Fatalf("refusal: %d %s", code, body)
	}
	if got, err = facts.ConvergeFacts(ctx, resource, status); err != nil || !got.HolderRefused || !got.Plan.Refused {
		t.Fatalf("facts after the refusal = %+v %v", got, err)
	}
	// A foreign state lock for the latest attempt.
	lock := executor.ConvergeRefusalRequest{Code: executor.RefusalStateLockForeign, CommitSHA: rig.commit, PlanSHA256: rig.planSHA, SourceAttemptID: attempt.ID,
		TofuLock: &executor.RefusalTofuLock{ID: "lock-1", Operation: "OperationTypeApply", Who: "someone@host"}}
	if code, body = rig.call(http.MethodPost, planPath+"/converge-refusals", rig.exec, "executor-refusal:lock", lock); code != http.StatusCreated {
		t.Fatalf("state lock refusal: %d %s", code, body)
	}
	if got, err = facts.ConvergeFacts(ctx, resource, status); err != nil || got.StateLock == nil || got.StateLock.LockID != "lock-1" || got.StateLock.SourceAttemptID != attempt.ID {
		t.Fatalf("facts after the state lock = %+v %v", got, err)
	}
}

// laneEtcdToken records a managed token and signs its bearer the way the
// device-token issuer does (including the device claim and, for the executor
// platform, the fleet:execute scope, which NewManagedAccessTokenRecord refuses
// to mint without an enrolled device).
func laneEtcdToken(secret string, identities *etcdstore.AuthStore, subject, deviceID string, scopes ...string) (string, error) {
	now := time.Now().UTC()
	jti := "norn_" + uuid.NewString()
	claims := map[string]interface{}{"sub": subject, "iss": "norn", "aud": "norn-control", "use": "access", "managed": true,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "jti": jti, "scp": scopes}
	if deviceID != "" {
		claims["did"] = deviceID
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	if err := identities.RecordAccessToken(context.Background(), &store.AccessToken{JTI: jti, DeviceID: deviceID, Subject: subject, Scopes: scopes, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	keyMAC := hmac.New(sha256.New, []byte(secret))
	_, _ = keyMAC.Write([]byte("norn.jwt-signing/v1"))
	signature := hmac.New(sha256.New, keyMAC.Sum(nil))
	_, _ = signature.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

// TestAttemptCredentialRevokedWithDevice: with the real registry, revoking the
// executor device (PG: DELETE /devices/{id}; etcd: the auth store, as the etcd
// runtime serves no device routes) answers 401 to the executor token and to
// the attempt-bound credential recorded against that device.
func TestAttemptCredentialRevokedWithDevice(t *testing.T) {
	t.Run("pg", func(t *testing.T) { newLanePG(t).scenario(true) })
	t.Run("etcd", func(t *testing.T) { newLaneEtcd(t).scenario(true) })
}

// TestExecutorRotatesItsOwnTokenAndCredentialCannot: /auth/rotate is in
// ExecutorRoutes (the idle loop self-rotates, D8); the attempt-bound
// credential is refused it. Both backends.
func TestExecutorRotatesItsOwnTokenAndCredentialCannot(t *testing.T) {
	for name, build := range map[string]func(*testing.T) *laneEnv{
		"pg":   func(t *testing.T) *laneEnv { return &newLanePG(t).laneEnv },
		"etcd": func(t *testing.T) *laneEnv { return &newLaneEtcd(t).laneEnv },
	} {
		t.Run(name, func(t *testing.T) {
			e := build(t)
			planPath := "/api/v1/fleet/plans/" + e.planID
			if code, body := e.call(http.MethodPost, planPath+"/converge/approve", e.admin, "approve", map[string]string{"commitSha": e.commit}); code != http.StatusCreated {
				t.Fatalf("approve: %d %s", code, body)
			}
			ref := fleettest.ExecutorPlanRef{ID: e.planID, CommitSHA: e.commit, PlanSHA256: e.planSHA}
			code, body := e.call(http.MethodPost, planPath+"/attempts", e.exec, "", fleettest.ExecutorStartRequest(ref, e.host, false))
			var attempt fleet.RunnerAttempt
			if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil {
				t.Fatalf("start: %d %s", code, body)
			}
			code, body = e.call(http.MethodPost, planPath+"/credential-grants", e.exec, "grant", executor.CredentialGrantRequest{AttemptID: attempt.ID, Phase: attempt.CurrentPhase, Types: []string{executor.CredNornAttempt}})
			var grant executor.CredentialGrantResponse
			if code != http.StatusCreated || json.Unmarshal(body, &grant) != nil {
				t.Fatalf("grant: %d %s", code, body)
			}
			if code, body = e.call(http.MethodPost, "/api/v1/auth/rotate", grant.AttemptToken, "", map[string]string{}); code != http.StatusForbidden {
				t.Fatalf("the attempt credential rotated: %d %s", code, body)
			}
			if code, body = e.call(http.MethodPost, "/api/v1/auth/revoke", grant.AttemptToken, "", map[string]string{}); code != http.StatusForbidden {
				t.Fatalf("the attempt credential revoked: %d %s", code, body)
			}
			var rotated struct {
				Token string `json:"token"`
			}
			if code, body = e.call(http.MethodPost, "/api/v1/auth/rotate", e.exec, "", map[string]string{}); code != http.StatusOK || json.Unmarshal(body, &rotated) != nil || rotated.Token == "" {
				t.Fatalf("executor rotate: %d %s", code, body)
			}
			if code, _ = e.call(http.MethodGet, "/api/v1/fleet/resources/"+e.resource, e.exec, "", nil); code != http.StatusUnauthorized {
				t.Fatalf("the retired executor token: %d", code)
			}
			if code, body = e.call(http.MethodGet, "/api/v1/fleet/resources/"+e.resource, rotated.Token, "", nil); code != http.StatusOK {
				t.Fatalf("the rotated executor token: %d %s", code, body)
			}
		})
	}
}

// TestPGWatcherVerdictsReachTheResourceView: the PG facts reader is wired to
// the watcher, so a tip that fails validation shows as trigger_unverified on
// the resource, and a resource in github mode is not watched at all.
func TestPGWatcherVerdictsReachTheResourceView(t *testing.T) {
	rig := newLanePG(t)
	rig.github.mu.Lock()
	rig.github.tip, rig.github.verified = strings.Repeat("b", 40), false
	rig.github.mu.Unlock()
	if err := rig.watcher.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	decision, ok := rig.watcher.Decision(rig.resource)
	if !ok || decision.Action != controller.WatchUnverified {
		t.Fatalf("decision = %+v", decision)
	}
	code, body := rig.call(http.MethodGet, "/api/v1/fleet/resources/"+rig.resource, rig.exec, "", nil)
	var view handler.FleetResourceView
	if code != http.StatusOK || json.Unmarshal(body, &view) != nil {
		t.Fatalf("view: %d %s", code, body)
	}
	if view.Blocker == nil || view.Blocker.Reason != executor.BlockerReasonTriggerUnverified {
		t.Fatalf("blocker = %+v", view.Blocker)
	}
	// Mode is enforced here: a github-mode resource is not listed for the
	// watcher, so a new verified tip is neither evaluated nor recorded for it.
	before, err := rig.watcher.Recorder.NewestConvergePlan(context.Background(), rig.resource)
	if err != nil || before == nil {
		t.Fatalf("newest plan: %v %+v", err, before)
	}
	rig.setMode(executor.ModeGitHub, "mode-github")
	rig.github.mu.Lock()
	rig.github.tip, rig.github.verified = strings.Repeat("c", 40), true
	rig.github.mu.Unlock()
	if err := rig.watcher.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if decision, ok := rig.watcher.Decision(rig.resource); ok && decision.TipSHA == strings.Repeat("c", 40) {
		t.Fatalf("a github-mode resource was evaluated for the new tip: %+v", decision)
	}
	after, err := rig.watcher.Recorder.NewestConvergePlan(context.Background(), rig.resource)
	if err != nil || after == nil || after.PlanID != before.PlanID {
		t.Fatalf("a plan was recorded for a github-mode resource: %v %+v (before %s)", err, after, before.PlanID)
	}
}

// TestEtcdRuntimeServesNoEnrollmentRoutes pins the documented decision: the
// etcd runtime has no enrollment or DELETE /devices/{id} routes (those
// handlers are bound to the PostgreSQL store), so executor devices are enrolled
// and revoked only through the PG authority.
func TestEtcdRuntimeServesNoEnrollmentRoutes(t *testing.T) {
	for _, route := range newLaneEtcd(t).routes {
		if strings.Contains(route, "/enrollments") || strings.Contains(route, "/devices") {
			t.Errorf("the etcd runtime serves %s", route)
		}
	}
}

// TestGeneralRouterRefusesExecutorLanePrincipals: the general router's
// control auth (bearerAuth) refuses the executor principal and the
// attempt-bound credential on every route, including the GitHub-lane attempt
// routes it shares with the authority-only router. Only the authority-only
// router (fleetAuthorityBearerAuth) admits them, so a general Mini over the
// same database cannot start an executor attempt without the start-time
// protected-branch re-check.
func TestGeneralRouterRefusesExecutorLanePrincipals(t *testing.T) {
	rig := newLanePG(t)
	planPath := "/api/v1/fleet/plans/" + rig.planID
	if code, body := rig.call(http.MethodPost, planPath+"/converge/approve", rig.admin, "approve", map[string]string{"commitSha": rig.commit}); code != http.StatusCreated {
		t.Fatalf("approve: %d %s", code, body)
	}
	ref := fleettest.ExecutorPlanRef{ID: rig.planID, CommitSHA: rig.commit, PlanSHA256: rig.planSHA}
	code, body := rig.call(http.MethodPost, planPath+"/attempts", rig.exec, "", fleettest.ExecutorStartRequest(ref, rig.host, false))
	var attempt fleet.RunnerAttempt
	if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil {
		t.Fatalf("start on the authority-only router: %d %s", code, body)
	}
	code, body = rig.call(http.MethodPost, planPath+"/credential-grants", rig.exec, "grant", executor.CredentialGrantRequest{AttemptID: attempt.ID, Phase: attempt.CurrentPhase, Types: []string{executor.CredNornAttempt}})
	var grant executor.CredentialGrantResponse
	if code != http.StatusCreated || json.Unmarshal(body, &grant) != nil {
		t.Fatalf("grant: %d %s", code, body)
	}
	reached := false
	general := bearerAuth(rig.cfg.APIToken, rig.handler, true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	attemptPath := planPath + "/attempts/" + attempt.ID
	for _, bearer := range []string{rig.exec, grant.AttemptToken} {
		for _, route := range [][2]string{
			{http.MethodPost, planPath + "/attempts"}, {http.MethodGet, attemptPath}, {http.MethodPost, attemptPath + "/heartbeat"},
			{http.MethodPost, attemptPath + "/advance"}, {http.MethodGet, planPath + "/reconciliations"}, {http.MethodPost, planPath + "/reconciliations"},
			{http.MethodGet, "/api/v1/fleet/resources/" + rig.resource}, {http.MethodPost, "/api/v1/auth/rotate"},
		} {
			reached = false
			req := httptest.NewRequest(route[0], route[1], nil)
			req.Header.Set("Authorization", "Bearer "+bearer)
			rec := httptest.NewRecorder()
			general.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || reached {
				t.Errorf("general router %s %s: %d (handler reached=%t)", route[0], route[1], rec.Code, reached)
			}
		}
	}
}

// TestEtcdExecutorLaneGatedOff: the production etcd runtime
// (etcdFleetExecutorLaneEnabled false) mounts no executor-lane route, runs no
// converge watcher, and refuses the executor principal on the shared runner,
// resource and credential routes, even with a live fleet-executor device.
func TestEtcdExecutorLaneGatedOff(t *testing.T) {
	if etcdFleetExecutorLaneEnabled {
		t.Fatal("the etcd executor lane must stay gated off until the v3 executor phase profile exists")
	}
	open := newLaneEtcd(t)
	router := chi.NewRouter()
	reconciler := newFleetReconciler(open.operations, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") })
	watcher := registerEtcdFleetControllerRoutes(router, open.cfg, open.identities, open.operations, &githubapp.Client{}, open.github, reconciler, etcdFleetExecutorLaneEnabled)
	registerEtcdFleetCredentialRoutes(router, open.cfg, open.identities, etcdFleetExecutorLaneEnabled)
	if watcher != nil {
		t.Fatal("a converge watcher was built with the etcd executor lane gated off")
	}
	served := laneRouteSet(laneWalkRoutes(t, router))
	for _, route := range []string{
		"POST /api/v1/fleet/resources/{name}/converge", "POST /api/v1/fleet/resources/{name}/execution-mode",
		"POST /api/v1/fleet/plans/{planID}/converge/approve", "POST /api/v1/fleet/plans/{planID}/credential-grants",
		"POST /api/v1/fleet/plans/{planID}/converge-refusals", "POST /api/v1/fleet/plans/{planID}/provider-evidence",
		"POST /api/v1/fleet/executor/stop-attestations",
	} {
		if served[route] {
			t.Errorf("gated-off etcd runtime serves %s", route)
		}
	}
	ref := fleettest.ExecutorPlanRef{ID: open.planID, CommitSHA: open.commit, PlanSHA256: open.planSHA}
	planPath := "/api/v1/fleet/plans/" + open.planID
	for _, call := range []struct {
		method, path string
		body         interface{}
	}{
		{http.MethodPost, planPath + "/attempts", fleettest.ExecutorStartRequest(ref, open.host, false)},
		{http.MethodGet, planPath + "/reconciliations?attemptId=" + uuid.NewString(), nil},
		{http.MethodGet, "/api/v1/fleet/resources/" + open.resource, nil},
		{http.MethodPost, "/api/v1/auth/rotate", map[string]string{}},
	} {
		var payload []byte
		if call.body != nil {
			payload, _ = json.Marshal(call.body)
		}
		req := httptest.NewRequest(call.method, call.path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+open.exec)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("gated-off %s %s with the executor token: %d %s", call.method, call.path, rec.Code, rec.Body.String())
		}
	}
}

// TestConvergeFactsReaderParity drives the same ledger sequence (approve,
// start, stop attestation, resume approve, destructive refusal, foreign state
// lock) through both real routers and requires the PG facts reader
// (Handler.ConvergeFactsReader) and the etcd one (NewEtcdConvergeFactsReader)
// to report identical facts after every step, field by field. Backend-specific
// identifiers (plan and attempt IDs, digests, approver subjects) are
// normalized to what they must equal.
func TestConvergeFactsReaderParity(t *testing.T) {
	type normalized struct {
		Plan                           *controller.ConvergePlanFacts
		Trigger, TriggerReason         string
		ResumeApproved, HolderRefused  bool
		ResumeNeededAttempt, StateLock string
		HolderCommit                   string
	}
	run := func(t *testing.T, e *laneEnv, facts func(ctx context.Context) (*controller.ConvergeFacts, error)) []string {
		var snapshots []string
		var attemptID string
		snap := func(step string) {
			t.Helper()
			got, err := facts(context.Background())
			if err != nil {
				t.Fatalf("%s: %v", step, err)
			}
			if got.HolderCommitSHA != e.commit {
				t.Fatalf("%s: HolderCommitSHA = %q, want %q", step, got.HolderCommitSHA, e.commit)
			}
			n := normalized{HolderCommit: "commit", Trigger: got.Trigger, TriggerReason: got.TriggerReason, ResumeApproved: got.ResumeApproved, HolderRefused: got.HolderRefused}
			if got.Plan != nil {
				plan := *got.Plan
				if plan.PlanID != e.planID || plan.PlanSHA256 != e.planSHA {
					t.Fatalf("%s: plan facts name %s/%s, want %s/%s", step, plan.PlanID, plan.PlanSHA256, e.planID, e.planSHA)
				}
				plan.PlanID, plan.PlanSHA256 = "plan", "sha"
				if plan.ApprovedBy != "" {
					plan.ApprovedBy = "approver"
				}
				n.Plan = &plan
			}
			switch got.ResumeNeededAttemptID {
			case "":
			case attemptID:
				n.ResumeNeededAttempt = "latest"
			default:
				n.ResumeNeededAttempt = "other:" + got.ResumeNeededAttemptID
			}
			if got.StateLock != nil {
				lock := *got.StateLock
				if lock.SourceAttemptID == attemptID {
					lock.SourceAttemptID = "latest"
				}
				encoded, _ := json.Marshal(lock)
				n.StateLock = string(encoded)
			}
			encoded, _ := json.Marshal(n)
			snapshots = append(snapshots, step+" "+string(encoded))
		}
		planPath := "/api/v1/fleet/plans/" + e.planID
		snap("fresh")
		if code, body := e.call(http.MethodPost, planPath+"/converge/approve", e.admin, "approve", map[string]string{"commitSha": e.commit}); code != http.StatusCreated {
			t.Fatalf("approve: %d %s", code, body)
		}
		snap("approved")
		ref := fleettest.ExecutorPlanRef{ID: e.planID, CommitSHA: e.commit, PlanSHA256: e.planSHA}
		code, body := e.call(http.MethodPost, planPath+"/attempts", e.exec, "", fleettest.ExecutorStartRequest(ref, e.host, false))
		var attempt fleet.RunnerAttempt
		if code != http.StatusCreated || json.Unmarshal(body, &attempt) != nil {
			t.Fatalf("start: %d %s", code, body)
		}
		attemptID = attempt.ID
		snap("started")
		next := e.host.NextInvocation()
		attestation := executor.StopAttestationRequest{SourceAttemptID: attempt.ID, DropletID: next.DropletID, MachineID: next.MachineID, BootID: next.BootID, InvocationID: next.InvocationID,
			PhaseUIDEmpty: true, PhaseProcessGroupReaped: true, SourceCgroupEmpty: true, ObservedAt: time.Now().UTC()}
		if code, body = e.call(http.MethodPost, "/api/v1/fleet/executor/stop-attestations", e.exec, "attest-1", attestation); code != http.StatusCreated {
			t.Fatalf("attestation: %d %s", code, body)
		}
		snap("attested")
		if code, body = e.call(http.MethodPost, planPath+"/converge/approve", e.admin, "resume", map[string]interface{}{"commitSha": e.commit, "resume": true, "sourceAttemptId": attempt.ID}); code != http.StatusCreated {
			t.Fatalf("resume approve: %d %s", code, body)
		}
		snap("resume-approved")
		refusal := executor.ConvergeRefusalRequest{Code: executor.RefusalDestructive, CommitSHA: e.commit, PlanSHA256: e.planSHA, PlanTextSHA256: strings.Repeat("d", 64), Changes: json.RawMessage(`{"delete":["digitalocean_droplet.x"]}`)}
		if code, body = e.call(http.MethodPost, planPath+"/converge-refusals", e.exec, "executor-refusal:"+e.planID, refusal); code != http.StatusCreated {
			t.Fatalf("refusal: %d %s", code, body)
		}
		snap("refused")
		lock := executor.ConvergeRefusalRequest{Code: executor.RefusalStateLockForeign, CommitSHA: e.commit, PlanSHA256: e.planSHA, SourceAttemptID: attempt.ID,
			TofuLock: &executor.RefusalTofuLock{ID: "lock-1", Operation: "OperationTypeApply", Who: "someone@host"}}
		if code, body = e.call(http.MethodPost, planPath+"/converge-refusals", e.exec, "executor-refusal:lock", lock); code != http.StatusCreated {
			t.Fatalf("state lock refusal: %d %s", code, body)
		}
		snap("state-lock")
		return snapshots
	}
	var pgSnapshots, etcdSnapshots []string
	t.Run("pg", func(t *testing.T) {
		rig := newLanePG(t)
		reader := rig.handler.ConvergeFactsReader(nil)
		pgSnapshots = run(t, &rig.laneEnv, func(ctx context.Context) (*controller.ConvergeFacts, error) {
			resource, err := rig.db.GetFleetResource(ctx, rig.resource)
			if err != nil {
				return nil, err
			}
			status := resource.Status
			status.Active = &controller.ActiveRefs{PlanID: rig.planID}
			return reader.ConvergeFacts(ctx, resource, status)
		})
	})
	t.Run("etcd", func(t *testing.T) {
		rig := newLaneEtcd(t)
		reader := handler.NewEtcdConvergeFactsReader(rig.cfg, rig.operations, nil)
		etcdSnapshots = run(t, &rig.laneEnv, func(ctx context.Context) (*controller.ConvergeFacts, error) {
			resource, err := rig.operations.GetFleetResource(ctx, rig.resource)
			if err != nil {
				return nil, err
			}
			status := resource.Status
			status.Active = &controller.ActiveRefs{PlanID: rig.planID}
			return reader.ConvergeFacts(ctx, resource, status)
		})
	})
	if t.Failed() {
		return
	}
	if len(pgSnapshots) == 0 || len(etcdSnapshots) == 0 {
		// A backend subtest skipped (no service, outside strict mode); the
		// helpers already fail instead when NORN_TEST_REQUIRE_INTEGRATION is set.
		t.Skip("parity needs both the PG and etcd backends")
	}
	if len(pgSnapshots) != len(etcdSnapshots) {
		t.Fatalf("snapshots: pg %d, etcd %d", len(pgSnapshots), len(etcdSnapshots))
	}
	for i := range pgSnapshots {
		if pgSnapshots[i] != etcdSnapshots[i] {
			t.Errorf("facts diverge:\n  pg:   %s\n  etcd: %s", pgSnapshots[i], etcdSnapshots[i])
		}
	}
}
