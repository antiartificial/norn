package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// executorLaneRig mounts the real legacy runner routes over PG with a signed
// converge plan on a registered target and an executor device.
type executorLaneRig struct {
	t        *testing.T
	p        *pgLifecycleHarness
	plan     fleettest.ExecutorPlanRef
	host     fleettest.ExecutorHost
	platform string
}

func newExecutorLaneRig(t *testing.T, platform string) *executorLaneRig {
	t.Helper()
	db := integrationtest.PG(t)
	p := newPGLifecycleHarness(t, db)
	ctx := context.Background()
	if _, err := db.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "lane", StateBackend: "s3://lane/state"}, []string{"cluster:lane"}, "op-lane"); err != nil {
		t.Fatal(err)
	}
	built, err := controller.BuildConvergePlan(controller.ConvergePlanInput{
		// A config range from an applied base auto-applies; a first converge
		// (no LastApplied) would require an approve (E4b).
		Resource: "lane", Cluster: "lane", Repository: "acme/norn-fleet", CommitSHA: strings.Repeat("a", 40),
		LastApplied: strings.Repeat("b", 40), PathClass: executor.PathClassConfig, Generation: 1, SigningKey: p.h.cfg.AuditSigningKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(built)
	var payload map[string]interface{}
	_ = json.Unmarshal(encoded, &payload)
	now := time.Now().UTC()
	if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: built.ID, Kind: "fleet.capacity-plan", Ref: "lane", Status: model.OperationSucceeded, Payload: payload, StartedAt: now.Add(-time.Hour), UpdatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	host := fleettest.NewExecutorHost()
	if err := db.CreateAccessDevice(ctx, &store.AccessDevice{ID: host.DeviceID, Name: "exec", Platform: platform, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return &executorLaneRig{t: t, p: p, host: host, platform: platform,
		plan: fleettest.ExecutorPlanRef{ID: built.ID, CommitSHA: strings.Repeat("a", 40), PlanSHA256: built.PlanSHA256()}}
}

func (r *executorLaneRig) principal() *AccessPrincipal {
	return &AccessPrincipal{Subject: "exec", Source: AccessPrincipalSourceManagedToken, TokenID: "jti", DeviceID: r.host.DeviceID,
		Scopes: []string{ScopeFleetExecute}, ExpiresAt: time.Now().Add(time.Hour)}
}

func (r *executorLaneRig) do(principal *AccessPrincipal, path string, body interface{}) *httptest.ResponseRecorder {
	r.t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.p.router.ServeHTTP(rec, WithAccessPrincipal(req, principal))
	return rec
}

func (r *executorLaneRig) attemptsPath() string {
	return "/api/v1/fleet/plans/" + r.plan.ID + "/attempts"
}

func TestExecutorLaneStartHeartbeatAndOwnership(t *testing.T) {
	rig := newExecutorLaneRig(t, FleetExecutorPlatform)
	request := fleettest.ExecutorStartRequest(rig.plan, rig.host, false)
	rec := rig.do(rig.principal(), rig.attemptsPath(), request)
	if rec.Code != http.StatusCreated {
		t.Fatalf("executor start: %d %s", rec.Code, rec.Body.String())
	}
	var attempt model.FleetRunnerAttempt
	_ = json.Unmarshal(rec.Body.Bytes(), &attempt)
	if replay := rig.do(rig.principal(), rig.attemptsPath(), request); replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	hb := map[string]interface{}{"schemaVersion": model.FleetRunnerAttemptSchemaVersion, "phase": attempt.CurrentPhase, "sequence": 1, "revision": attempt.Revision}
	if rec := rig.do(rig.principal(), rig.attemptsPath()+"/"+attempt.ID+"/heartbeat", hb); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body.String())
	}
	// Another executor device does not own the attempt.
	other := rig.principal()
	other.DeviceID = "device-other"
	if rec := rig.do(other, rig.attemptsPath()+"/"+attempt.ID+"/heartbeat", hb); rec.Code != http.StatusForbidden {
		t.Fatalf("a foreign device must not heartbeat: %d", rec.Code)
	}
}

func TestExecutorLaneRefusesGitHubFieldsAndWrongPlatform(t *testing.T) {
	rig := newExecutorLaneRig(t, FleetExecutorPlatform)
	body, _ := json.Marshal(fleettest.ExecutorStartRequest(rig.plan, rig.host, false))
	withNonce := bytes.Replace(body, []byte(`{`), []byte(`{"dispatchNonce":"`+strings.Repeat("a", 64)+`","sourceDispatchRunId":7,`), 1)
	if rec := rig.do(rig.principal(), rig.attemptsPath(), withNonce); rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != executor.CodeInvalidFleetExecutorRequest {
		t.Fatalf("GitHub-lane fields must be unknown to the executor lane: %d %s", rec.Code, rec.Body.String())
	}
	wrong := newExecutorLaneRig(t, "ios")
	if rec := wrong.do(wrong.principal(), wrong.attemptsPath(), fleettest.ExecutorStartRequest(wrong.plan, wrong.host, false)); rec.Code != http.StatusForbidden {
		t.Fatalf("a non-executor device platform must be refused: %d", rec.Code)
	}
}

func TestCIPrincipalKeepsGitHubLaneForExecutorBody(t *testing.T) {
	rig := newExecutorLaneRig(t, FleetExecutorPlatform)
	ci := pgConformanceCI(fleettest.RunRef{RunID: 5, RunAttempt: 1, Intent: "apply", SHA: rig.plan.CommitSHA})
	ciPrincipal := rig.p.principal(ci)
	rec := rig.do(ciPrincipal, rig.attemptsPath(), fleettest.ExecutorStartRequest(rig.plan, rig.host, false))
	if rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != "invalid_fleet_runner_attempt" {
		t.Fatalf("CI principal must hit the unchanged GitHub decoder: %d %s", rec.Code, rec.Body.String())
	}
	// A CI principal is never an executor principal, and an executor principal
	// never owns a GitHub attempt.
	if IsFleetExecutorPrincipal(*ciPrincipal) {
		t.Fatal("CI principal selected the executor lane")
	}
	githubAttempt := &model.FleetRunnerAttempt{RunnerAttemptID: canonicalFleetRunnerAttemptID(ci), WorkflowURL: canonicalFleetWorkflowURL(ci), PrincipalSubject: "managed-device:" + rig.host.DeviceID, SourceDispatchRunID: 5}
	if fleetRunnerPrincipalOwnsAttempt(*rig.principal(), githubAttempt) {
		t.Fatal("executor principal owns a GitHub-lane attempt")
	}
}

func (r *executorLaneRig) checkpoint(principal *AccessPrincipal, attempt *model.FleetRunnerAttempt, attemptID string) *httptest.ResponseRecorder {
	r.t.Helper()
	body, _ := json.Marshal(fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, AttemptID: attemptID, Phase: attempt.CurrentPhase, Status: "succeeded",
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, StateSerial: 1, EvidenceDigest: "sha256:" + strings.Repeat("c", 64),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+r.plan.ID+"/reconciliations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "checkpoint:"+attemptID+":"+attempt.CurrentPhase)
	rec := httptest.NewRecorder()
	r.p.router.ServeHTTP(rec, WithAccessPrincipal(req, principal))
	return rec
}

func (r *executorLaneRig) listCheckpoints(principal *AccessPrincipal, attemptID string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/plans/"+r.plan.ID+"/reconciliations?attemptId="+attemptID, nil)
	rec := httptest.NewRecorder()
	r.p.router.ServeHTTP(rec, WithAccessPrincipal(req, principal))
	return rec
}

// TestExecutorLaneCheckpointsAndCompletes drives the executor lane through
// the existing reconciliation and advance routes to complete: the executor
// branch of RecordFleetReconciliation and ListFleetReconciliations, with the
// device ownership and platform checks.
func TestExecutorLaneCheckpointsAndCompletes(t *testing.T) {
	rig := newExecutorLaneRig(t, FleetExecutorPlatform)
	rec := rig.do(rig.principal(), rig.attemptsPath(), fleettest.ExecutorStartRequest(rig.plan, rig.host, false))
	if rec.Code != http.StatusCreated {
		t.Fatalf("executor start: %d %s", rec.Code, rec.Body.String())
	}
	var attempt model.FleetRunnerAttempt
	_ = json.Unmarshal(rec.Body.Bytes(), &attempt)

	// Refusals: no attemptId, a foreign device, a non-executor device platform.
	if rec := rig.checkpoint(rig.principal(), &attempt, ""); rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != "invalid_fleet_reconciliation" {
		t.Fatalf("an executor checkpoint must name its attempt: %d %s", rec.Code, rec.Body.String())
	}
	other := rig.principal()
	other.DeviceID = "device-other"
	if err := rig.p.db.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: other.DeviceID, Name: "other", Platform: FleetExecutorPlatform, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if rec := rig.checkpoint(other, &attempt, attempt.ID); rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "fleet_runner_attempt_binding_conflict" {
		t.Fatalf("a foreign executor device must not checkpoint: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.listCheckpoints(other, attempt.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("a foreign executor device must not read evidence: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.listCheckpoints(rig.principal(), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("an executor read must name its attempt: %d %s", rec.Code, rec.Body.String())
	}
	wrongPlatform := rig.principal()
	wrongPlatform.DeviceID = "device-ios"
	if err := rig.p.db.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: wrongPlatform.DeviceID, Name: "ios", Platform: "ios", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if rec := rig.checkpoint(wrongPlatform, &attempt, attempt.ID); rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "insufficient_scope" {
		t.Fatalf("a non-executor platform must not checkpoint: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.listCheckpoints(wrongPlatform, attempt.ID); rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "insufficient_scope" {
		t.Fatalf("a non-executor platform must not read evidence: %d %s", rec.Code, rec.Body.String())
	}

	for i := 0; attempt.Status != model.FleetRunnerAttemptSucceeded; i++ {
		if i > 10 {
			t.Fatalf("attempt never completed: %+v", attempt)
		}
		if rec := rig.checkpoint(rig.principal(), &attempt, attempt.ID); rec.Code != http.StatusCreated {
			t.Fatalf("checkpoint %s: %d %s", attempt.CurrentPhase, rec.Code, rec.Body.String())
		}
		if rec := rig.checkpoint(rig.principal(), &attempt, attempt.ID); rec.Code != http.StatusOK {
			t.Fatalf("checkpoint replay %s: %d %s", attempt.CurrentPhase, rec.Code, rec.Body.String())
		}
		list := rig.listCheckpoints(rig.principal(), attempt.ID)
		var listed struct {
			Count int `json:"count"`
		}
		_ = json.Unmarshal(list.Body.Bytes(), &listed)
		if list.Code != http.StatusOK || listed.Count != i+1 {
			t.Fatalf("list checkpoints: %d count=%d %s", list.Code, listed.Count, list.Body.String())
		}
		advance := map[string]interface{}{"schemaVersion": model.FleetRunnerAttemptSchemaVersion, "expectedPhase": attempt.CurrentPhase, "revision": attempt.Revision}
		rec := rig.do(rig.principal(), rig.attemptsPath()+"/"+attempt.ID+"/advance", advance)
		if rec.Code != http.StatusOK {
			t.Fatalf("advance %s: %d %s", attempt.CurrentPhase, rec.Code, rec.Body.String())
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &attempt)
	}
}
