package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// seedControllerResource creates resource on its own target with
// executionMode controller (the recorder records only for such a resource).
func seedControllerResource(t *testing.T, db *store.DB, resource string) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	target, err := db.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "rec-" + suffix, StateBackend: "s3://rec-" + suffix + "/state"}, []string{"cluster:rec-" + suffix}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureFleetResource(ctx, resource, target.TargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE fleet_resources SET document=jsonb_set(document, '{executionMode}', to_jsonb($2::text)) WHERE name=$1`, resource, executor.ModeController); err != nil {
		t.Fatal(err)
	}
}

// TestFleetConvergePGRecorderRefusesAfterModeSwitch: the watcher read
// controller mode, then the resource went back to github; the recorder's own
// transaction sees that and records nothing.
func TestFleetConvergePGRecorderRefusesAfterModeSwitch(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	const auditKey = "fleet-converge-acceptance-signing-key-01"
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: auditKey}, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	recorder := h.ConvergePlanRecorder()
	ctx := context.Background()
	resource := "converge-" + uuid.NewString()[:8]
	seedControllerResource(t, db, resource)
	if _, err := db.Pool.Exec(ctx, `UPDATE fleet_resources SET document=jsonb_set(document, '{executionMode}', '"github"') WHERE name=$1`, resource); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: resource, Cluster: "staging", Repository: "acme/norn-fleet",
		CommitSHA: strings.Repeat("b", 40), LastApplied: strings.Repeat("a", 40), PathClass: executor.PathClassConfig, Generation: 1, SigningKey: auditKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.RecordConvergePlan(ctx, plan, "key-github"); !errors.Is(err, controller.ErrNotWatched) {
		t.Fatalf("record for a github-mode resource = %v, want ErrNotWatched", err)
	}
	if newest, err := recorder.NewestConvergePlan(ctx, resource); err != nil || newest != nil {
		t.Fatalf("nothing is recorded: %+v %v", newest, err)
	}
}

// TestFleetConvergePGRecorderRecordsAndApproves runs the PG recorder and the
// approve route against PostgreSQL's signed acceptance path.
func TestFleetConvergePGRecorderRecordsAndApproves(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	const auditKey = "fleet-converge-acceptance-signing-key-01"
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: auditKey}, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	recorder := h.ConvergePlanRecorder()
	ctx := context.Background()
	resource := "converge-" + uuid.NewString()[:8]
	commit := strings.Repeat("b", 40)
	seedControllerResource(t, db, resource)

	if newest, err := recorder.NewestConvergePlan(ctx, resource); err != nil || newest != nil {
		t.Fatalf("empty newest = %+v err=%v", newest, err)
	}
	plan, err := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: resource, Cluster: "staging", Repository: "acme/norn-fleet",
		CommitSHA: commit, LastApplied: strings.Repeat("a", 40), PathClass: executor.PathClassExecutable, Generation: 3, SigningKey: auditKey})
	if err != nil {
		t.Fatal(err)
	}
	first, err := recorder.RecordConvergePlan(ctx, plan, "key-1")
	if err != nil || first.PlanID != plan.ID || !first.Admissible || !first.Converge.RequiresApproval {
		t.Fatalf("record = %+v err=%v", first, err)
	}
	// The same key replays the first plan; a second plan object is not stored.
	other, _ := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: resource, Cluster: "staging", Repository: "acme/norn-fleet",
		CommitSHA: commit, LastApplied: strings.Repeat("a", 40), PathClass: executor.PathClassExecutable, Generation: 3, SigningKey: auditKey})
	replayed, err := recorder.RecordConvergePlan(ctx, other, "key-1")
	if err != nil || replayed.PlanID != plan.ID {
		t.Fatalf("replay = %+v err=%v", replayed, err)
	}
	newest, err := recorder.NewestConvergePlan(ctx, resource)
	if err != nil || newest == nil || newest.PlanID != plan.ID || newest.Digest != plan.Digest {
		t.Fatalf("newest = %+v err=%v", newest, err)
	}
	read, err := recorder.ConvergePlan(ctx, plan.ID)
	if err != nil || read.Converge.CommitSHA != commit {
		t.Fatalf("read = %+v err=%v", read, err)
	}
	if _, err := recorder.ConvergePlan(ctx, uuid.NewString()); err != ErrConvergePlanNotFound {
		t.Fatalf("missing plan err = %v", err)
	}
	// A refused plan is reported inadmissible so a rescan can replace it.
	recorder.Admissible = func(context.Context, string) (bool, error) { return false, nil }
	if newest, _ := recorder.NewestConvergePlan(ctx, resource); newest == nil || newest.Admissible {
		t.Fatalf("newest after refusal = %+v", newest)
	}

	// Approve through the real mutation-audit and acceptance path.
	routes := h.FleetConvergeRoutes(&fakeRescanner{})
	approve := func(key string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(executor.ApproveRequest{CommitSHA: commit})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/converge/approve", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("planID", plan.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopePlatformOperate}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(routes.Approve)).ServeHTTP(rec, req)
		return rec
	}
	firstApprove := approve("approve-1")
	if firstApprove.Code != http.StatusCreated {
		t.Fatalf("approve: %d %s", firstApprove.Code, firstApprove.Body.String())
	}
	var op struct {
		ID, Kind, Ref string
		Payload       map[string]interface{}
	}
	if err := json.Unmarshal(firstApprove.Body.Bytes(), &op); err != nil || op.Kind != executor.KindConvergeApprove || op.Ref != plan.ID || op.Payload["commitSha"] != commit || op.Payload["planId"] != plan.ID {
		t.Fatalf("approve op = %+v err=%v", op, err)
	}
	if replay := approve("approve-1"); replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), op.ID) {
		t.Fatalf("approve replay: %d %s", replay.Code, replay.Body.String())
	}
}
