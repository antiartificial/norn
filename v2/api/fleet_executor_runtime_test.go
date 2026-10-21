package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
)

// --- scope matcher ---

func laneTestExecutorPrincipal() *handler.AccessPrincipal {
	return &handler.AccessPrincipal{Subject: "exec", Source: handler.AccessPrincipalSourceManagedToken, TokenID: "jti", DeviceID: "device-1",
		Scopes: []string{handler.ScopeFleetExecute}, ExpiresAt: time.Now().Add(time.Hour)}
}

func laneTestAttemptPrincipal(attemptID string) *handler.AccessPrincipal {
	return &handler.AccessPrincipal{Subject: "fleet-attempt:" + attemptID, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "jti-attempt",
		Scopes: []string{handler.ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
}

var laneRoutePlaceholder = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// laneConcretePath fills a route pattern's placeholders with valid-looking
// values (UUIDs for plan and attempt IDs, a resource name otherwise).
func laneConcretePath(pattern string, planID, attemptID string) string {
	return laneRoutePlaceholder.ReplaceAllStringFunc(pattern, func(match string) string {
		switch match {
		case "{planID}":
			return planID
		case "{attemptID}", "{id}":
			return attemptID
		case "{pool}":
			return "control"
		case "{targetID}":
			return "tgt_" + strings.Repeat("0", 64)
		case "{admissionId}":
			return uuid.NewString()
		default:
			return "prod"
		}
	})
}

// laneWalkRoutes returns "METHOD /pattern" for every route of router.
func laneWalkRoutes(t *testing.T, router http.Handler) []string {
	t.Helper()
	mux, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("router %T is not a chi router", router)
	}
	var routes []string
	if err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+strings.ReplaceAll(route, "/*", ""))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(routes)
	return routes
}

func laneRouteSet(routes []string) map[string]bool {
	out := map[string]bool{}
	for _, route := range routes {
		out[route] = true
	}
	return out
}

// TestExecutorPrincipalReachesExactlyExecutorRoutes pins the scope matcher:
// across every route the authority-only router serves (plus the contract's
// own lists and adversarial spellings) the fleet:execute principal is admitted
// exactly where executor.ExecutorRoutes lists the route, and nowhere else.
func TestExecutorPrincipalReachesExactlyExecutorRoutes(t *testing.T) {
	router := fleetAuthorityOnlyRouter(fleetAuthorityOnlyTestConfig(t), nil)
	universe := laneRouteSet(laneWalkRoutes(t, router))
	for _, route := range executor.ExecutorRoutes {
		universe[route] = true
	}
	for _, route := range executor.AttemptCredentialRoutes {
		universe[route] = true
	}
	for _, route := range []string{
		"GET /api/v1/fleet/resources", "GET /api/v1/fleet/plans/{planID}/attempts", "POST /api/v1/auth/revoke", "GET /api/v1/operations/{id}",
		"POST /api/v1/operations/{id}/cancel", "GET /api/v1/fleet/plans", "POST /api/v1/fleet/node-pools/{pool}/plan",
		"POST /api/v1/fleet/resources/{name}/execution-mode", "POST /api/v1/fleet/plans/{planID}/provider-evidence", "POST /api/v1/fleet/plans/{planID}/converge/approve",
		"GET /api/v1/fleet/resources/{name}/observations", "POST /api/v1/fleet/resources/{name}/desired", "DELETE /api/v1/devices/{id}", "GET /ws",
	} {
		universe[route] = true
	}
	want := laneRouteSet(executor.ExecutorRoutes)
	planID, attemptID := uuid.NewString(), uuid.NewString()
	principal := laneTestExecutorPrincipal()
	covered := map[string]bool{}
	for route := range universe {
		method, pattern, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, laneConcretePath(pattern, planID, attemptID), nil)
		restricted, allowed := fleetLaneAdmits(principal, req)
		if !restricted {
			t.Fatalf("%s: the executor principal was not restricted", route)
		}
		if allowed != want[route] {
			t.Errorf("%s: executor admitted=%t, ExecutorRoutes lists it=%t", route, allowed, want[route])
		}
		if allowed {
			covered[route] = true
		}
	}
	for route := range want {
		if !covered[route] {
			t.Errorf("ExecutorRoutes entry %s was never admitted", route)
		}
	}
	// Adversarial spellings of an executor route.
	for _, path := range []string{
		"/api/v1/fleet/resources/prod%2Fconverge", "/api/v1/fleet/resources/../converge", "/api/v1/fleet/resources/./converge",
		"/api/v1/fleet/resources/prod/", "/api/v1/fleet/resources/", "/api/v1/fleet/resources//converge",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, path, nil)
			if _, allowed := fleetLaneAdmits(principal, req); allowed {
				t.Errorf("%s %s admitted", method, path)
			}
		}
	}
	// The principal is restricted whatever the method.
	if _, allowed := fleetLaneAdmits(principal, httptest.NewRequest(http.MethodDelete, "/api/v1/fleet/resources/prod", nil)); allowed {
		t.Error("DELETE on an executor route admitted")
	}
	// Not the executor principal: admin, a CI principal and nil are left to
	// the generic check.
	for name, other := range map[string]*handler.AccessPrincipal{
		"admin":    {Scopes: []string{handler.ScopeAdmin}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", DeviceID: "d", ExpiresAt: time.Now().Add(time.Hour)},
		"ci":       {Scopes: []string{handler.ScopeFleetOperate}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", CI: &handler.CIIdentity{}, ExpiresAt: time.Now().Add(time.Hour)},
		"operator": {Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", DeviceID: "d", ExpiresAt: time.Now().Add(time.Hour)},
		"nil":      nil,
	} {
		if restricted, _ := fleetLaneAdmits(other, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/resources", nil)); restricted {
			t.Errorf("%s principal was lane-restricted", name)
		}
	}
	// A principal carrying fleet:execute that is not the executor principal
	// (legacy, CI, no device, another scope beside it) is refused everywhere.
	for name, other := range map[string]*handler.AccessPrincipal{
		"legacy":    {Scopes: []string{handler.ScopeFleetExecute}, Legacy: true},
		"ci":        {Scopes: []string{handler.ScopeFleetExecute}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", DeviceID: "d", CI: &handler.CIIdentity{}, ExpiresAt: time.Now().Add(time.Hour)},
		"no-device": {Scopes: []string{handler.ScopeFleetExecute}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", ExpiresAt: time.Now().Add(time.Hour)},
		"widened":   {Scopes: []string{handler.ScopeFleetExecute, handler.ScopeAPIRead}, Source: handler.AccessPrincipalSourceManagedToken, TokenID: "t", DeviceID: "d", ExpiresAt: time.Now().Add(time.Hour)},
	} {
		for _, req := range []*http.Request{httptest.NewRequest(http.MethodGet, "/api/v1/fleet/resources", nil), httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/attempts", nil)} {
			if restricted, allowed := fleetLaneAdmits(other, req); !restricted || allowed {
				t.Errorf("%s fleet:execute principal %s %s: restricted=%t allowed=%t", name, req.Method, req.URL.Path, restricted, allowed)
			}
		}
	}
}

// TestAttemptCredentialReachesExactlyItsRoutes pins the matcher half of
// the attempt-bound credential: exactly AttemptCredentialRoutes, for its own
// attempt only.
func TestAttemptCredentialReachesExactlyItsRoutes(t *testing.T) {
	router := fleetAuthorityOnlyRouter(fleetAuthorityOnlyTestConfig(t), nil)
	universe := laneRouteSet(laneWalkRoutes(t, router))
	for _, route := range executor.ExecutorRoutes {
		universe[route] = true
	}
	want := laneRouteSet(executor.AttemptCredentialRoutes)
	planID, attemptID := uuid.NewString(), uuid.NewString()
	principal := laneTestAttemptPrincipal(attemptID)
	for route := range universe {
		method, pattern, _ := strings.Cut(route, " ")
		restricted, allowed := fleetLaneAdmits(principal, httptest.NewRequest(method, laneConcretePath(pattern, planID, attemptID), nil))
		if !restricted {
			t.Fatalf("%s: the attempt credential was not restricted", route)
		}
		if allowed != want[route] {
			t.Errorf("%s: credential admitted=%t, AttemptCredentialRoutes lists it=%t", route, allowed, want[route])
		}
	}
	// Another attempt's path is refused.
	other := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/attempts/"+uuid.NewString()+"/heartbeat", nil)
	if _, allowed := fleetLaneAdmits(principal, other); allowed {
		t.Error("the credential reached another attempt")
	}
	// A credential carrying a device claim is not the credential, and is still
	// never generic.
	bad := laneTestAttemptPrincipal(attemptID)
	bad.DeviceID = "device-1"
	if restricted, allowed := fleetLaneAdmits(bad, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/resources", nil)); !restricted || allowed {
		t.Errorf("a fleet:attempt principal with a device claim: restricted=%t allowed=%t", restricted, allowed)
	}
}

// TestFleetExecutorScopeMapping pins the generic scope the new routes need for
// every other principal: provider evidence and execution mode are admin,
// approve and the rescan are platform:operate, the executor-only routes need
// fleet:execute, and the executor route set does not widen any other path.
func TestFleetExecutorScopeMapping(t *testing.T) {
	scope := func(method, path string) string {
		return controlScopeForRequest(httptest.NewRequest(method, path, nil))
	}
	plan := "/api/v1/fleet/plans/" + uuid.NewString()
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodPost, plan + "/provider-evidence", handler.ScopeAdmin},
		{http.MethodPost, "/api/v1/fleet/resources/prod/execution-mode", handler.ScopeAdmin},
		{http.MethodPost, plan + "/converge/approve", handler.ScopePlatformOperate},
		{http.MethodPost, "/api/v1/fleet/resources/prod/converge", handler.ScopePlatformOperate},
		{http.MethodPost, plan + "/credential-grants", handler.ScopeFleetExecute},
		{http.MethodPost, plan + "/converge-refusals", handler.ScopeFleetExecute},
		{http.MethodPost, "/api/v1/fleet/executor/stop-attestations", handler.ScopeFleetExecute},
		// Unchanged neighbours.
		{http.MethodPost, "/api/v1/fleet/resources/prod/desired", handler.ScopeAPIWrite},
		{http.MethodPost, "/api/v1/fleet/resources/prod", handler.ScopePlatformOperate},
		{http.MethodGet, "/api/v1/fleet/resources/prod", handler.ScopeAPIRead},
		{http.MethodGet, plan + "/provider-evidence", handler.ScopeAPIRead},
		{http.MethodPost, plan + "/attempts", ""},
		{http.MethodPost, "/api/v1/fleet/resources/prod/execution-mode/x", handler.ScopeAPIWrite},
	} {
		if got := scope(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s scope = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// --- capabilities and the access scope list ---

func TestFleetExecutorCapabilitiesOnBothProcesses(t *testing.T) {
	cfg := fleetAuthorityOnlyTestConfig(t)
	rec := httptest.NewRecorder()
	fleetAuthorityOnlyRouter(cfg, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	var pg struct {
		Features  []string          `json:"features"`
		Endpoints map[string]string `json:"endpoints"`
		Auth      struct {
			Scopes                     []string            `json:"scopes"`
			EnrollmentScopes           []string            `json:"enrollmentScopes"`
			EnrollmentScopesByPlatform map[string][]string `json:"enrollmentScopesByPlatform"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pg); err != nil {
		t.Fatal(err)
	}
	if !containsCapability(pg.Features, "fleet-executor-v1") {
		t.Fatalf("authority-only features: %v", pg.Features)
	}
	for name, want := range map[string]string{
		"fleetResourceConverge": "/api/v1/fleet/resources/{name}/converge", "fleetResourceExecutionMode": "/api/v1/fleet/resources/{name}/execution-mode",
		"fleetConvergeApprove": "/api/v1/fleet/plans/{planID}/converge/approve", "fleetCredentialGrants": "/api/v1/fleet/plans/{planID}/credential-grants",
		"fleetConvergeRefusals": "/api/v1/fleet/plans/{planID}/converge-refusals", "fleetProviderEvidence": "/api/v1/fleet/plans/{planID}/provider-evidence",
		"fleetExecutorStopAttestations": "/api/v1/fleet/executor/stop-attestations",
	} {
		if pg.Endpoints[name] != want {
			t.Errorf("endpoint %s = %q, want %q", name, pg.Endpoints[name], want)
		}
	}
	if got := pg.Auth.EnrollmentScopesByPlatform["fleet-executor"]; len(got) != 1 || got[0] != handler.ScopeFleetExecute {
		t.Errorf("enrollmentScopesByPlatform = %v", pg.Auth.EnrollmentScopesByPlatform)
	}
	// The ordinary enrollment scopes never grow fleet:execute: it is for the
	// fleet-executor platform only.
	for _, scope := range pg.Auth.EnrollmentScopes {
		if scope == handler.ScopeFleetExecute {
			t.Errorf("enrollmentScopes lists %s", scope)
		}
	}
	listed := false
	for _, scope := range pg.Auth.Scopes {
		listed = listed || scope == handler.ScopeFleetExecute
	}
	if !listed || !containsString(handler.AccessTokenScopeNames(), handler.ScopeFleetExecute) {
		t.Errorf("fleet:execute is not listed in AccessTokenScopeNames: %v", pg.Auth.Scopes)
	}

	// The etcd executor lane is gated off by default, so
	// the etcd runtime does not advertise it even with the GitHub App.
	etcd := etcdFleetCapabilities(&config.Config{}, false, true)
	features, _ := etcd["features"].([]string)
	endpoints, _ := etcd["endpoints"].(map[string]string)
	if containsCapability(features, "fleet-executor-v1") || endpoints["fleetCredentialGrants"] != "" {
		t.Errorf("etcd capabilities with the GitHub App: %#v", etcd)
	}
	enabled := etcdFleetCapabilities(&config.Config{FleetEtcdExecutorEnabled: true}, false, true)
	if !containsCapability(enabled["features"].([]string), "fleet-executor-v1") || enabled["endpoints"].(map[string]string)["fleetCredentialGrants"] == "" {
		t.Errorf("enabled etcd executor capabilities: %#v", enabled)
	}
	auth := enabled["auth"].(map[string]interface{})
	byPlatform := auth["enrollmentScopesByPlatform"].(map[string][]string)
	if got := byPlatform[handler.FleetExecutorPlatform]; len(got) != 1 || got[0] != handler.ScopeFleetExecute {
		t.Errorf("enabled etcd executor enrollment scopes: %#v", byPlatform)
	}
	// Without the GitHub App the etcd executor lane is not mounted, so it is
	// not advertised.
	without := etcdFleetCapabilities(&config.Config{}, false)
	if containsCapability(without["features"].([]string), "fleet-executor-v1") {
		t.Error("etcd advertised the executor lane without a Fleet GitHub App")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// --- the general router never serves the executor lane ---

// TestExecutorRoutesAbsentFromGeneralRouter parses main.go and proves that
// main() (the general Mini router) registers no executor-lane handler and no
// executor-lane path; the lane is wired only by fleetAuthorityOnlyRouterWithWatcher
// and the etcd Fleet runtime.
func TestExecutorRoutesAbsentFromGeneralRouter(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var mainBody *ast.BlockStmt
	registered := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "main":
			mainBody = fn.Body
		case "fleetAuthorityOnlyRouterWithWatcher":
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if value, err := strconv.Unquote(lit.Value); err == nil {
						registered[value] = true
					}
				}
				return true
			})
		}
	}
	if mainBody == nil {
		t.Fatal("main() not found")
	}
	// Positive control: the authority-only router does register them.
	for _, suffix := range []string{"/converge", "/converge/approve", "/execution-mode", "/credential-grants", "/converge-refusals", "/provider-evidence", "/fleet/executor/stop-attestations"} {
		found := false
		for path := range registered {
			found = found || strings.HasSuffix(path, suffix)
		}
		if !found {
			t.Errorf("fleetAuthorityOnlyRouterWithWatcher does not register %s", suffix)
		}
	}
	forbiddenPaths := []string{"/converge", "execution-mode", "credential-grants", "converge-refusals", "provider-evidence", "stop-attestations", "fleet/executor"}
	forbiddenNames := map[string]bool{
		"FleetExecutorOpsRoutes": true, "FleetConvergeRoutes": true, "SetExecutionMode": true, "Rescan": true, "Approve": true,
		"Grant": true, "Refuse": true, "ProviderEvidence": true, "StopAttestation": true, "ConvergeFactsReader": true,
		"ConvergePlanRecorder": true, "SetFleetExecutorStartVerifier": true, "fleetAuthorityOnlyRouterWithWatcher": true,
		"newFleetConvergeWatcher": true, "wireFleetAuthorityExecutorLane": true,
	}
	ast.Inspect(mainBody, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				value, _ := strconv.Unquote(node.Value)
				for _, fragment := range forbiddenPaths {
					if strings.Contains(value, fragment) {
						t.Errorf("main() mentions the executor-lane path fragment %q in %s", fragment, value)
					}
				}
			}
		case *ast.SelectorExpr:
			if forbiddenNames[node.Sel.Name] {
				t.Errorf("main() references %s", node.Sel.Name)
			}
		case *ast.Ident:
			if forbiddenNames[node.Name] {
				t.Errorf("main() references %s", node.Name)
			}
		}
		return true
	})
}

// --- watcher placement ---

type laneFakeCommitSource struct {
	mu       sync.Mutex
	tip      string
	verified bool
	verifies int
}

func (f *laneFakeCommitSource) ReadBranchTip(context.Context, string) (*githubapp.BranchTip, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &githubapp.BranchTip{SHA: f.tip, Protected: true, ETag: "etag"}, nil
}

func (f *laneFakeCommitSource) VerifyProtectedCommit(_ context.Context, sha, lastApplied string) (*githubapp.CommitVerification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifies++
	if !f.verified {
		return &githubapp.CommitVerification{SHA: sha, Verdict: githubapp.CommitUnverified, Code: executor.CodeFleetExecutorTriggerUnverified, Reason: "validation failed"}, nil
	}
	class := executor.PathClassConfig
	if lastApplied == "" {
		class = executor.PathClassExecutable
	}
	return &githubapp.CommitVerification{SHA: sha, Verdict: githubapp.CommitVerified, PathClass: class}, nil
}

type laneNilLister struct{}

func (laneNilLister) ListWatchedResources(context.Context) ([]controller.WatchedResource, error) {
	return nil, nil
}

type laneNilRecorder struct{}

func (laneNilRecorder) NewestConvergePlan(context.Context, string) (*controller.RecordedConverge, error) {
	return nil, nil
}
func (laneNilRecorder) RecordConvergePlan(context.Context, *controller.ConvergeCapacityPlan, string) (*controller.RecordedConverge, error) {
	return nil, errors.New("unused")
}

type laneNilDesired struct{ fleetResourceDesiredStore }

// TestWatcherStartsOnlyWithReconciler pins Q6 placement: the converge watcher
// exists only where the reconciler runs, and needs a GitHub client.
func TestWatcherStartsOnlyWithReconciler(t *testing.T) {
	cfg := &config.Config{FleetGitHubRepository: "acme/fleet", AuditSigningKey: strings.Repeat("k", 32)}
	reconciler := newFleetReconciler(nil, func(error) bool { return false })
	github := &laneFakeCommitSource{tip: strings.Repeat("a", 40), verified: true}
	if w := newFleetConvergeWatcher(nil, cfg, github, laneNilLister{}, laneNilRecorder{}, laneNilDesired{}); w != nil {
		t.Fatal("a watcher was built without a reconciler")
	}
	if w := newFleetConvergeWatcher(reconciler, cfg, nil, laneNilLister{}, laneNilRecorder{}, laneNilDesired{}); w != nil {
		t.Fatal("a watcher was built without a GitHub client")
	}
	w := newFleetConvergeWatcher(reconciler, cfg, github, laneNilLister{}, laneNilRecorder{}, laneNilDesired{})
	if w == nil {
		t.Fatal("no watcher with a reconciler and a GitHub client")
	}
	if w.SigningKey != cfg.AuditSigningKey || w.Desired == nil || w.Recorder == nil || w.Source == nil {
		t.Fatalf("watcher is not fully wired: %+v", w)
	}

	// The PG process wiring follows the same rule.
	if pgWatcher := wireFleetAuthorityExecutorLane(cfg, &handler.Handler{}, nil, nil, github); pgWatcher != nil {
		t.Fatal("the PG wiring built a watcher without a reconciler")
	}
	if pgWatcher := wireFleetAuthorityExecutorLane(cfg, &handler.Handler{}, nil, reconciler, github); pgWatcher == nil {
		t.Fatal("the PG wiring built no watcher with a reconciler")
	}
	stop := startFleetConvergeWatcher(context.Background(), w)
	stop() // returns promptly: Run honours cancellation
}

// --- watcher adapters ---

func TestFleetWatchedSourceKeepsOnlyTheConfiguredRepository(t *testing.T) {
	src := fleetWatchedSource{repository: "acme/fleet", lister: laneListerOf([]controller.WatchedResource{
		{Name: "a", Repository: "acme/fleet"}, {Name: "b"}, {Name: "c", Repository: "ACME/Fleet"}, {Name: "d", Repository: "acme/other"},
	})}
	got, err := src.ListWatchedResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, resource := range got {
		names = append(names, resource.Name)
		if resource.Repository == "" {
			t.Errorf("%s has no repository", resource.Name)
		}
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("watched = %v", names)
	}
}

type laneListerOf []controller.WatchedResource

func (l laneListerOf) ListWatchedResources(context.Context) ([]controller.WatchedResource, error) {
	return l, nil
}

type laneDesiredStore struct {
	resource *controller.Resource
	sets     []controller.DesiredRevision
	setErr   error
}

func (s *laneDesiredStore) GetFleetResource(context.Context, string) (*controller.Resource, error) {
	copy := *s.resource
	return &copy, nil
}

func (s *laneDesiredStore) SetDesiredFleetResourceIf(_ context.Context, _ string, expected int64, next controller.DesiredRevision) (*controller.Resource, error) {
	if s.setErr != nil {
		return nil, s.setErr
	}
	if expected != s.resource.Revision {
		return nil, errors.New("revision conflict")
	}
	next.Generation = s.resource.Desired.Generation + 1
	s.resource.Desired, s.resource.Revision = next, s.resource.Revision+1
	s.sets = append(s.sets, next)
	return s.resource, nil
}

func TestFleetConvergeDesiredSetterIsIdempotent(t *testing.T) {
	commit := strings.Repeat("a", 40)
	plan := controller.RecordedConverge{PlanID: uuid.NewString(), Converge: executor.ConvergePlan{Resource: "prod", Repository: "acme/fleet", CommitSHA: commit, Generation: 3}}
	store := &laneDesiredStore{resource: &controller.Resource{Name: "prod", Revision: 7, Desired: controller.DesiredRevision{Generation: 2, PlanID: "older"}}}
	setter := fleetConvergeDesiredSetter{store: store, now: func() time.Time { return time.Unix(1700000000, 0) }}
	watched := controller.WatchedResource{Name: "prod"}
	for i := 0; i < 3; i++ {
		if err := setter.SetConvergeDesired(context.Background(), watched, plan); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(store.sets) != 1 {
		t.Fatalf("the desired revision was set %d times", len(store.sets))
	}
	got := store.sets[0]
	if got.PlanID != plan.PlanID || got.CommitSHA != commit || got.Repository != "acme/fleet" || got.Verification != controller.VerificationGitHubProtectedMain || got.AcceptedBy != "fleet-converge-watcher" {
		t.Fatalf("desired = %+v", got)
	}
	if controller.ValidateDesired(got) != nil {
		t.Fatalf("desired revision is invalid: %v", controller.ValidateDesired(got))
	}
	// An operator moved desired meanwhile: the stale plan is refused loudly.
	stale := &laneDesiredStore{resource: &controller.Resource{Name: "prod", Revision: 1, Desired: controller.DesiredRevision{Generation: 5, PlanID: "operator"}}}
	if err := (fleetConvergeDesiredSetter{store: stale}).SetConvergeDesired(context.Background(), watched, plan); err == nil || len(stale.sets) != 0 {
		t.Fatalf("a stale generation was accepted: %v", err)
	}
}

// --- start-time verification ---

func TestFleetStartVerifierCachesVerifiedFor60Seconds(t *testing.T) {
	github := &laneFakeCommitSource{tip: strings.Repeat("a", 40), verified: true}
	clock := time.Unix(1700000000, 0)
	verifier := newFleetStartVerifier("acme/fleet", github)
	verifier.now = func() time.Time { return clock }
	sha, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	if verifier.ttl > 60*time.Second {
		t.Fatalf("ttl = %s, want at most 60s", verifier.ttl)
	}
	for i := 0; i < 3; i++ {
		v, err := verifier.VerifyStartCommit(context.Background(), "Acme/Fleet", sha, base)
		if err != nil || v.Verdict != githubapp.CommitVerified {
			t.Fatalf("verify: %v %+v", err, v)
		}
	}
	if github.verifies != 1 {
		t.Fatalf("GitHub was asked %d times within the cache window", github.verifies)
	}
	clock = clock.Add(61 * time.Second)
	if _, err := verifier.VerifyStartCommit(context.Background(), "acme/fleet", sha, base); err != nil || github.verifies != 2 {
		t.Fatalf("after expiry: %v, verifies=%d", err, github.verifies)
	}
	// A different base is a different verdict.
	if _, err := verifier.VerifyStartCommit(context.Background(), "acme/fleet", sha, ""); err != nil || github.verifies != 3 {
		t.Fatalf("different base: %v, verifies=%d", err, github.verifies)
	}
	// Only a verified verdict is cached.
	github.verified = false
	for i := 0; i < 2; i++ {
		v, err := verifier.VerifyStartCommit(context.Background(), "acme/fleet", strings.Repeat("c", 40), base)
		if err != nil || v.Verdict != githubapp.CommitUnverified {
			t.Fatalf("unverified: %v %+v", err, v)
		}
	}
	if github.verifies != 5 {
		t.Fatalf("an unverified verdict was cached: verifies=%d", github.verifies)
	}
	// A repository with no client fails closed.
	v, err := verifier.VerifyStartCommit(context.Background(), "acme/other", sha, base)
	if err != nil || v.Verdict != githubapp.CommitUnverified || v.Code != executor.CodeFleetExecutorTriggerUnverified {
		t.Fatalf("unknown repository: %v %+v", err, v)
	}
}
