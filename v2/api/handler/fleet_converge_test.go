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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
)

type fakeRescanner struct {
	keys     []string
	decision controller.WatchDecision
	err      error
}

func (f *fakeRescanner) Rescan(_ context.Context, resource string) (controller.WatchDecision, error) {
	f.keys = append(f.keys, resource)
	d := f.decision
	d.Resource = resource
	return d, f.err
}

type fakeConvergePlans struct {
	plan *controller.RecordedConverge
	err  error
}

func (f *fakeConvergePlans) ConvergePlan(context.Context, string) (*controller.RecordedConverge, error) {
	return f.plan, f.err
}

type fakeApprovals struct {
	got  []executor.ApproveAdmission
	keys []string
}

func (f *fakeApprovals) RecordApproval(_ http.ResponseWriter, _ *http.Request, key string, a executor.ApproveAdmission) (model.Operation, bool, bool) {
	replayed := false
	for _, k := range f.keys {
		replayed = replayed || k == key
	}
	f.keys = append(f.keys, key)
	f.got = append(f.got, a)
	return model.Operation{ID: uuid.NewString(), Kind: executor.KindConvergeApprove, Ref: a.PlanID, Status: model.OperationSucceeded, StartedAt: time.Now(), UpdatedAt: time.Now()}, replayed, true
}

func convergeRouter(routes *FleetConvergeRoutes, principal *AccessPrincipal) http.Handler {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal != nil {
				r = WithAccessPrincipal(r, principal)
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Post("/api/v1/fleet/resources/{name}/converge", routes.Rescan)
	router.Post("/api/v1/fleet/plans/{planID}/converge/approve", routes.Approve)
	return router
}

func operatorPrincipal(scopes ...string) *AccessPrincipal {
	return &AccessPrincipal{Subject: "op", Scopes: scopes, Source: AccessPrincipalSourceManagedToken, TokenID: "t", ExpiresAt: time.Now().Add(time.Hour)}
}

func doConverge(router http.Handler, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func problemCode(rec *httptest.ResponseRecorder) string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

func TestFleetConvergeRescanRoute(t *testing.T) {
	rescan := &fakeRescanner{decision: controller.WatchDecision{Action: controller.WatchExists, TipSHA: strings.Repeat("b", 40)}}
	routes := NewFleetConvergeRoutes(rescan, &fakeConvergePlans{}, &fakeApprovals{})
	path := "/api/v1/fleet/resources/staging/converge"

	if rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), path, "", ""); rec.Code != http.StatusBadRequest || problemCode(rec) != "invalid_idempotency_key" {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), path, strings.Repeat("k", 201), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("long key: %d", rec.Code)
	}
	if len(rescan.keys) != 0 {
		t.Fatal("rescan ran without a valid key")
	}
	for name, p := range map[string]*AccessPrincipal{
		"read only": operatorPrincipal(ScopeAPIRead),
		"ci":        {Subject: "ci", Scopes: []string{ScopePlatformOperate}, CI: &CIIdentity{}},
		"anonymous": nil,
	} {
		if rec := doConverge(convergeRouter(routes, p), path, "k", ""); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
	}
	if len(rescan.keys) != 0 {
		t.Fatal("rescan ran for an unauthorized caller")
	}
	if rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), "/api/v1/fleet/resources/Bad_Name/converge", "k", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", rec.Code)
	}

	rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), path, "k1", "")
	var got controller.WatchDecision
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Resource != "staging" || got.Action != controller.WatchExists {
		t.Fatalf("operator rescan: %d %s", rec.Code, rec.Body.String())
	}
	executorP := executorPrincipal()
	if rec := doConverge(convergeRouter(routes, &executorP), path, "k1", ""); rec.Code != http.StatusOK {
		t.Fatalf("executor rescan: %d %s", rec.Code, rec.Body.String())
	}
	// No caller-derived key or client SHA reaches the watcher.
	if len(rescan.keys) != 2 || rescan.keys[0] != "staging" || rescan.keys[1] != "staging" {
		t.Fatalf("rescans = %q", rescan.keys)
	}

	rescan.err = controller.ErrNotWatched
	if rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), path, "k", ""); rec.Code != http.StatusConflict || problemCode(rec) != executor.CodeFleetExecutionModeNotController {
		t.Fatalf("not watched: %d %s", rec.Code, rec.Body.String())
	}
	rescan.err = errors.New("github down")
	if rec := doConverge(convergeRouter(routes, operatorPrincipal(ScopePlatformOperate)), path, "k", ""); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "github down") {
		t.Fatalf("failure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetConvergeResumeApproveShape(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	commit := strings.Repeat("b", 40)
	plans := &fakeConvergePlans{plan: &controller.RecordedConverge{PlanID: planID, Converge: executor.ConvergePlan{CommitSHA: commit}}}
	approvals := &fakeApprovals{}
	routes := NewFleetConvergeRoutes(&fakeRescanner{}, plans, approvals)
	router := convergeRouter(routes, operatorPrincipal(ScopePlatformOperate))
	path := "/api/v1/fleet/plans/" + planID + "/converge/approve"
	resume := `{"commitSha":"` + commit + `","resume":true,"sourceAttemptId":"` + attemptID + `"}`

	cases := []struct {
		name, path, key, body string
		principal             *AccessPrincipal
		status                int
		code                  string
	}{
		{"no key", path, "", resume, operatorPrincipal(ScopePlatformOperate), 400, "invalid_idempotency_key"},
		{"executor principal", path, "k", resume, func() *AccessPrincipal { p := executorPrincipal(); return &p }(), 403, "insufficient_scope"},
		{"api:write only", path, "k", resume, operatorPrincipal(ScopeAPIWrite), 403, "insufficient_scope"},
		{"ci", path, "k", resume, &AccessPrincipal{Subject: "ci", Scopes: []string{ScopePlatformOperate}, CI: &CIIdentity{}}, 403, "insufficient_scope"},
		{"plan id not uuid", "/api/v1/fleet/plans/nope/converge/approve", "k", resume, operatorPrincipal(ScopePlatformOperate), 400, executor.CodeInvalidFleetExecutorRequest},
		{"resume without source", path, "k", `{"commitSha":"` + commit + `","resume":true}`, operatorPrincipal(ScopePlatformOperate), 400, executor.CodeInvalidFleetExecutorRequest},
		{"source without resume", path, "k", `{"commitSha":"` + commit + `","sourceAttemptId":"` + attemptID + `"}`, operatorPrincipal(ScopePlatformOperate), 400, executor.CodeInvalidFleetExecutorRequest},
		{"short sha", path, "k", `{"commitSha":"abc"}`, operatorPrincipal(ScopePlatformOperate), 400, executor.CodeInvalidFleetExecutorRequest},
		{"unknown field", path, "k", `{"commitSha":"` + commit + `","planId":"x"}`, operatorPrincipal(ScopePlatformOperate), 400, executor.CodeInvalidFleetExecutorRequest},
		{"commit mismatch", path, "k", `{"commitSha":"` + strings.Repeat("c", 40) + `"}`, operatorPrincipal(ScopePlatformOperate), 409, executor.CodeFleetConvergeCommitMismatch},
	}
	for _, c := range cases {
		rec := doConverge(convergeRouter(routes, c.principal), c.path, c.key, c.body)
		if rec.Code != c.status || (c.code != "" && problemCode(rec) != c.code) {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if len(approvals.got) != 0 {
		t.Fatalf("a refused approve was recorded: %+v", approvals.got)
	}

	plans.err = ErrConvergePlanNotFound
	if rec := doConverge(router, path, "k", resume); rec.Code != http.StatusNotFound {
		t.Fatalf("missing plan: %d", rec.Code)
	}
	plans.err = ErrNotConvergePlan
	if rec := doConverge(router, path, "k", resume); rec.Code != http.StatusConflict || problemCode(rec) != executor.CodeFleetPlanNotConverge {
		t.Fatalf("not converge: %d %s", rec.Code, rec.Body.String())
	}
	plans.err = ErrConvergePlanInvalid
	if rec := doConverge(router, path, "k", resume); rec.Code != http.StatusConflict || problemCode(rec) != "fleet_plan_invalid" {
		t.Fatalf("invalid plan: %d %s", rec.Code, rec.Body.String())
	}
	plans.err = nil

	rec := doConverge(router, path, "resume-1", resume)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") == "" {
		t.Fatalf("resume approve: %d %s", rec.Code, rec.Body.String())
	}
	want := executor.ApproveAdmission{PlanID: planID, CommitSHA: commit, Resume: true, SourceAttemptID: attemptID}
	if len(approvals.got) != 1 || approvals.got[0] != want {
		t.Fatalf("admission = %+v, want %+v", approvals.got, want)
	}
	// Pinned ledger payload shape.
	encoded, _ := json.Marshal(approvals.got[0])
	if string(encoded) != `{"planId":"`+planID+`","commitSha":"`+commit+`","resume":true,"sourceAttemptId":"`+attemptID+`"}` {
		t.Fatalf("payload = %s", encoded)
	}
	// A non-resume approve omits both optional fields.
	if rec := doConverge(router, path, "plain-1", `{"commitSha":"`+commit+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("plain approve: %d %s", rec.Code, rec.Body.String())
	}
	encoded, _ = json.Marshal(approvals.got[1])
	if string(encoded) != `{"planId":"`+planID+`","commitSha":"`+commit+`"}` {
		t.Fatalf("plain payload = %s", encoded)
	}
	// A key replay returns the first response with 200, not a second 201.
	if rec := doConverge(router, path, "resume-1", resume); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
}

// TestFleetConvergePlanFingerprintsUnchangedForCapacityPlans pins the handler
// side: the converge wrapper reproduces canonicalCapacityPlan byte for byte for
// an ordinary plan, and a converge plan fails closed in the GitHub lane's
// verifier (which decodes into a plain CapacityPlan and would drop converge).
func TestFleetConvergePlanFingerprintsUnchangedForCapacityPlans(t *testing.T) {
	pool := fleet.NodePool{Size: "small", Min: 2, Desired: 2, Max: 8}
	inventory := &fleet.Inventory{Digest: "sha256:source", Document: &fleet.Document{Cluster: fleet.Cluster{Name: "staging"}}}
	plan, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Desired: intPointer(3)}, "audit-key")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := canonicalCapacityPlan(plan)
	got, err := controller.CanonicalConvergePlan(&controller.ConvergeCapacityPlan{CapacityPlan: *plan})
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", want, got)
	}

	converge, err := controller.BuildConvergePlan(controller.ConvergePlanInput{Resource: "staging", Cluster: "staging", Repository: "acme/norn-fleet",
		CommitSHA: strings.Repeat("b", 40), PathClass: executor.PathClassConfig, Generation: 1, SigningKey: "audit-key"})
	if err != nil {
		t.Fatal(err)
	}
	op := &model.Operation{ID: converge.ID, Kind: "fleet.capacity-plan", Ref: "staging"}
	encoded, _ := json.Marshal(converge)
	_ = json.Unmarshal(encoded, &op.Payload)
	if decoded, ok := decodeConvergePlan(op); !ok || !controller.VerifyConvergePlan(decoded, []string{"audit-key"}) {
		t.Fatal("converge plan did not round trip through the operation payload")
	}
	typed, err := typedCapacityPlan(op)
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, nil, nil, nil, &config.Config{Profile: "production", AuditSigningKey: "audit-key"}, nil, nil, nil, nil, nil, nil)
	if h.verifyCapacityPlan(typed) {
		t.Fatal("a converge plan must not verify as a plain capacity plan")
	}
}
