package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
)

// pgFleetResourceRoutes and etcdFleetResourceRoutes are the routes main.go and
// etcd_fleet_resources.go register, verbatim ({method, path, handler} plus the
// etcd auth middleware variable). requireRoutesRegistered fails the real-
// backend route tests if production stops registering any of them.
var pgFleetResourceRoutes = [][3]string{
	{"Get", "/fleet/resources", "List"},
	{"Post", "/fleet/resources/{name}", "Create"},
	{"Get", "/fleet/resources/{name}", "Get"},
	{"Post", "/fleet/resources/{name}/desired", "SetDesired"},
	{"Get", "/fleet/resources/{name}/observations", "ListObservations"},
	{"Post", "/fleet/resources/{name}/observations", "AppendObservation"},
}

var etcdFleetResourceRoutes = [][4]string{
	{"Get", "/api/v1/fleet/resources", "List", "resourceRead"},
	{"Post", "/api/v1/fleet/resources/{name}", "Create", "resourceCreate"},
	{"Get", "/api/v1/fleet/resources/{name}", "Get", "resourceRead"},
	{"Post", "/api/v1/fleet/resources/{name}/desired", "SetDesired", "resourceWrite"},
	{"Get", "/api/v1/fleet/resources/{name}/observations", "ListObservations", "resourceRead"},
	{"Post", "/api/v1/fleet/resources/{name}/observations", "AppendObservation", "resourceObserve"},
}

func mountFleetResourceRoutes(router chi.Router, routes *FleetResourceRoutes) {
	router.Get("/api/v1/fleet/resources", routes.List)
	router.Post("/api/v1/fleet/resources/{name}", routes.Create)
	router.Get("/api/v1/fleet/resources/{name}", routes.Get)
	router.Post("/api/v1/fleet/resources/{name}/desired", routes.SetDesired)
	router.Get("/api/v1/fleet/resources/{name}/observations", routes.ListObservations)
	router.Post("/api/v1/fleet/resources/{name}/observations", routes.AppendObservation)
}

const (
	testFleetRepoTuple = "acme/fleet@101@202"
	testFleetRepoName  = "acme/fleet"
)

func fleetResourceConfig() *config.Config {
	return &config.Config{GitHubActionsFleetAllowedRepository: testFleetRepoTuple, FleetGitHubRepository: testFleetRepoName, FleetGitHubEnvironment: "staging"}
}

func observerPrincipal() *AccessPrincipal {
	return &AccessPrincipal{Subject: "ci", TokenID: "token-ci", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetOperate}, CI: &CIIdentity{
		Provider: "github-actions", Repository: testFleetRepoName, RepositoryID: "101", RepositoryOwnerID: "202", RunID: "9001", RunAttempt: "1",
		Ref: "refs/heads/main", RefType: "branch", EventName: "push", Environment: "staging", RefProtected: true, Intent: FleetObserveIntent,
	}}
}

// fakeResourceBackend is an in-memory FleetResourceBackend with the real
// stores' CAS, monotonic-watermark and bounds behavior.
type fakeResourceBackend struct {
	mu        sync.Mutex
	targets   map[string]*FleetTargetRecord
	resources map[string]*controller.Resource
	obs       map[string][]controller.Observation
	plans     map[string]string // plan ID -> the target its cluster resolves to
	appends   int
}

const resTargetID = "tgt_0000000000000000000000000000000000000000000000000000000000000001"

func newFakeResourceBackend() *fakeResourceBackend {
	return &fakeResourceBackend{
		targets:   map[string]*FleetTargetRecord{resTargetID: {TargetID: resTargetID, Aliases: []string{"cluster:fleet", "environment:staging/nyc3"}}},
		resources: map[string]*controller.Resource{},
		obs:       map[string][]controller.Observation{},
		plans:     map[string]string{},
	}
}

func (b *fakeResourceBackend) GetTarget(_ context.Context, id string) (*FleetTargetRecord, error) {
	return b.targets[id], nil
}
func (b *fakeResourceBackend) EnsureResource(_ context.Context, name, targetID string) (*controller.Resource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.resources[name]; ok {
		if existing.TargetID != targetID {
			return nil, errFleetResourceTargetMismatch
		}
		return existing, nil
	}
	now := time.Now().UTC()
	b.resources[name] = &controller.Resource{SchemaVersion: controller.SchemaVersion, Name: name, TargetID: targetID, CreatedAt: now, UpdatedAt: now}
	return b.resources[name], nil
}
func (b *fakeResourceBackend) GetResource(_ context.Context, name string) (*controller.Resource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	resource, ok := b.resources[name]
	if !ok {
		return nil, errFleetResourceNotFound
	}
	copied := *resource
	return &copied, nil
}
func (b *fakeResourceBackend) ListResourceNames(_ context.Context, after string, limit int) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for name := range b.resources {
		if name > after {
			names = append(names, name)
		}
	}
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	if len(names) > limit {
		names = names[:limit]
	}
	return names, nil
}
func (b *fakeResourceBackend) SetDesired(_ context.Context, name string, expected int64, next controller.DesiredRevision) (*controller.Resource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	resource, ok := b.resources[name]
	if !ok {
		return nil, errFleetResourceNotFound
	}
	if resource.Revision != expected {
		return nil, errFleetResourceRevisionConflict
	}
	if err := controller.ValidateDesired(next); err != nil {
		return nil, errFleetResourceInvalid
	}
	if resource.Desired.Generation > 0 {
		resource.DesiredHistory = append([]controller.DesiredRevision{resource.Desired}, resource.DesiredHistory...)
	}
	next.Generation = resource.Desired.Generation + 1
	resource.Desired = next
	resource.Revision++
	copied := *resource
	return &copied, nil
}
func (b *fakeResourceBackend) AppendObservation(_ context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, refs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	resource, ok := b.resources[name]
	if !ok {
		return nil, controller.Observation{}, errFleetResourceNotFound
	}
	b.appends++
	resource.ObservationSequence++
	wm, has := resource.Watermarks[source]
	applied, next := controller.ApplyObservation(wm, has, observedAt, resource.ObservationSequence)
	if applied {
		if resource.Watermarks == nil {
			resource.Watermarks = map[string]controller.Watermark{}
		}
		resource.Watermarks[source] = next
	}
	obs := controller.Observation{Sequence: resource.ObservationSequence, Source: source, ObservedAt: observedAt, ReceivedAt: time.Now().UTC(), Facts: facts, EvidenceRefs: refs, Reporter: reporter, Applied: applied}
	b.obs[name] = append([]controller.Observation{obs}, b.obs[name]...)
	copied := *resource
	return &copied, obs, nil
}
func (b *fakeResourceBackend) ListObservations(_ context.Context, name string, limit int) ([]controller.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.obs[name]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (b *fakeResourceBackend) PlanTarget(_ context.Context, id, environment string) (string, bool, error) {
	if environment != "staging/nyc3" {
		return "", false, errors.New("plan resolved under an unexpected lane")
	}
	if id == unregisteredPlanID {
		return "", true, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered}
	}
	target, ok := b.plans[id]
	return target, ok, nil
}

// unregisteredPlanID is a plan whose cluster has no registered target.
const unregisteredPlanID = "00000000-0000-4000-8000-0000000000ff"

func (b *fakeResourceBackend) FleetEnvironment() (string, *routeProblem) {
	return "staging/nyc3", nil
}

type fakeResolver struct {
	mu          sync.Mutex
	approved    *githubapp.Dispatch
	err         error
	environment string
	calls       int
}

func (f *fakeResolver) ResolveApprovedPlan(_ context.Context, _, environment string) (*githubapp.Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.environment = environment
	return f.approved, f.err
}

type fakeLiveness struct {
	at       time.Time
	interval time.Duration
}

func (f fakeLiveness) LastRescan() (time.Time, time.Duration) { return f.at, f.interval }

func newResourceRouter(routes *FleetResourceRoutes) http.Handler {
	router := chi.NewRouter()
	mountFleetResourceRoutes(router, routes)
	return router
}

func resourcePath(name string) string { return "/api/v1/fleet/resources/" + name }

func readerPrincipal() *AccessPrincipal   { return fleetTargetPrincipal(ScopeAPIRead) }
func writerPrincipal() *AccessPrincipal   { return fleetTargetPrincipal(ScopeAPIWrite) }
func platformPrincipal() *AccessPrincipal { return fleetTargetPrincipal(ScopePlatformOperate) }
func decodeResource(t *testing.T, rec *httptest.ResponseRecorder) FleetResourceView {
	t.Helper()
	var view FleetResourceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode resource: %v %s", err, rec.Body)
	}
	return view
}

const approvedSHA = "1111111111111111111111111111111111111111"

func TestDesiredResolvesMergedPlanOrOperatorDeclared(t *testing.T) {
	backend := newFakeResourceBackend()
	planID := uuid.NewString()
	backend.plans[planID] = resTargetID
	resolver := &fakeResolver{approved: &githubapp.Dispatch{ApprovedHeadSHA: approvedSHA, PlanRunID: 7}}
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), resolver, nil, nil)
	router := newResourceRouter(routes)

	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": resTargetID}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": resTargetID}); rec.Code != http.StatusOK {
		t.Fatalf("idempotent create: %d %s", rec.Code, rec.Body)
	}
	desired := func(principal *AccessPrincipal, body map[string]interface{}) *httptest.ResponseRecorder {
		return fleetTargetDo(router, http.MethodPost, resourcePath("prod")+"/desired", principal, "", body)
	}

	// Scope: api:read and a workload identity never set desired.
	if rec := desired(readerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusForbidden {
		t.Fatalf("api:read set desired: %d", rec.Code)
	}
	ci := observerPrincipal()
	ci.Scopes = []string{ScopeAPIWrite}
	if rec := desired(ci, map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusForbidden {
		t.Fatalf("CI set desired: %d", rec.Code)
	}
	if resolver.calls != 0 {
		t.Fatal("GitHub was consulted before the scope check passed")
	}

	// A client-supplied SHA is never trusted while the App can verify.
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "commitSha": strings.Repeat("a", 40), "expectedRevision": 0}); rec.Code != http.StatusBadRequest {
		t.Fatalf("commitSha with App: %d %s", rec.Code, rec.Body)
	}
	if rec := desired(writerPrincipal(), map[string]interface{}{"expectedRevision": 0}); rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != "invalid_fleet_plan_id" {
		t.Fatalf("missing planId: %d %s", rec.Code, rec.Body)
	}
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing expectedRevision: %d", rec.Code)
	}
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": uuid.NewString(), "expectedRevision": 0}); rec.Code != http.StatusNotFound || decodeProblemCode(rec) != "fleet_plan_not_found" {
		t.Fatalf("unknown plan: %d %s", rec.Code, rec.Body)
	}
	// A merged plan for a different cluster's target never becomes this
	// resource's desired revision, and GitHub is not consulted for it.
	otherPlan := uuid.NewString()
	backend.plans[otherPlan] = "tgt_" + strings.Repeat("e", 64)
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": otherPlan, "expectedRevision": 0}); rec.Code != http.StatusConflict || decodeProblemCode(rec) != "fleet_resource_plan_target_mismatch" {
		t.Fatalf("plan for another target: %d %s", rec.Code, rec.Body)
	}
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": unregisteredPlanID, "expectedRevision": 0}); rec.Code != http.StatusConflict || decodeProblemCode(rec) != lifecycle.CodeFleetTargetUnregistered {
		t.Fatalf("plan on an unregistered cluster: %d %s", rec.Code, rec.Body)
	}
	if resolver.calls != 0 {
		t.Fatal("GitHub was consulted for a plan bound to another target")
	}
	// A pending or unmerged proposal is refused.
	resolver.approved, resolver.err = nil, githubapp.ErrNotReady
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusConflict || decodeProblemCode(rec) != "fleet_github_plan_not_ready" {
		t.Fatalf("unmerged plan: %d %s", rec.Code, rec.Body)
	}
	// A malformed approved commit is never recorded.
	resolver.approved, resolver.err = &githubapp.Dispatch{ApprovedHeadSHA: "main"}, nil
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusBadGateway {
		t.Fatalf("unproven commit: %d %s", rec.Code, rec.Body)
	}
	if got := decodeResource(t, fleetTargetDo(router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil)); got.Desired.Generation != 0 {
		t.Fatalf("a refused request changed desired: %+v", got.Desired)
	}

	// Merged: the server's commit is recorded, not the client's.
	resolver.approved, resolver.err = &githubapp.Dispatch{ApprovedHeadSHA: approvedSHA}, nil
	rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("merged plan: %d %s", rec.Code, rec.Body)
	}
	view := decodeResource(t, rec)
	if view.Desired.Generation != 1 || view.Desired.CommitSHA != approvedSHA || view.Desired.PlanID != planID ||
		view.Desired.Verification != controller.VerificationGitHubMergedPlan || view.Desired.Repository != testFleetRepoName || view.Desired.AcceptedBy != "operator" || view.Revision != 1 {
		t.Fatalf("desired = %+v revision=%d", view.Desired, view.Revision)
	}
	if resolver.environment != "staging/nyc3" {
		t.Fatalf("resolved under %q", resolver.environment)
	}
	// expectedRevision is a CAS: the old token now conflicts.
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusConflict || decodeProblemCode(rec) != "fleet_resource_revision_conflict" {
		t.Fatalf("stale revision: %d %s", rec.Code, rec.Body)
	}
	if rec := desired(writerPrincipal(), map[string]interface{}{"planId": planID, "expectedRevision": 1}); rec.Code != http.StatusOK {
		t.Fatalf("fresh revision: %d %s", rec.Code, rec.Body)
	}
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("absent")+"/desired", writerPrincipal(), "", map[string]interface{}{"planId": planID, "expectedRevision": 0}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: %d", rec.Code)
	}

	// GitHub configured but unusable fails closed instead of falling back.
	broken := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, errors.New("bad key"), nil))
	if rec := fleetTargetDo(broken, http.MethodPost, resourcePath("prod")+"/desired", writerPrincipal(), "", map[string]interface{}{"commitSha": strings.Repeat("a", 40), "expectedRevision": 2}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken App fell back: %d %s", rec.Code, rec.Body)
	}

	// No App at all: an explicit commit is accepted and labeled operator-declared.
	declared := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil))
	sha := strings.Repeat("c", 40)
	for label, body := range map[string]map[string]interface{}{
		"planId without App": {"planId": planID, "commitSha": sha, "expectedRevision": 2},
		"short sha":          {"commitSha": "abc", "expectedRevision": 2},
		"upper-case sha":     {"commitSha": strings.ToUpper(sha), "expectedRevision": 2},
		"no sha":             {"expectedRevision": 2},
	} {
		if rec := fleetTargetDo(declared, http.MethodPost, resourcePath("prod")+"/desired", writerPrincipal(), "", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", label, rec.Code, rec.Body)
		}
	}
	rec = fleetTargetDo(declared, http.MethodPost, resourcePath("prod")+"/desired", writerPrincipal(), "", map[string]interface{}{"commitSha": sha, "expectedRevision": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("operator-declared: %d %s", rec.Code, rec.Body)
	}
	view = decodeResource(t, rec)
	if view.Desired.Verification != controller.VerificationOperatorDeclared || view.Desired.CommitSHA != sha || view.Desired.PlanID != "" || view.Desired.Repository != "" || view.Desired.Generation != 3 {
		t.Fatalf("operator-declared desired = %+v", view.Desired)
	}
	if view.ApprovalPolicy.ProtectedBranch || view.ApprovalPolicy.MergedPullRequest || view.ApprovalPolicy.PlanWorkflowSucceeded || view.ApprovalPolicy.BoundPlanDigest || view.ApprovalPolicy.AuthorizedDispatch || view.ApprovalPolicy.Basis != controller.VerificationOperatorDeclared {
		t.Fatalf("operator-declared policy implies provenance: %+v", view.ApprovalPolicy)
	}
}

func appendObservation(router http.Handler, principal *AccessPrincipal, name string, body interface{}) *httptest.ResponseRecorder {
	return fleetTargetDo(router, http.MethodPost, resourcePath(name)+"/observations", principal, "", body)
}

func okObservation(observedAt time.Time) map[string]interface{} {
	return map[string]interface{}{"source": "runtime", "observedAt": observedAt.UTC().Format(time.RFC3339Nano), "facts": map[string]interface{}{"ready": true}, "evidenceRefs": []string{"run/9001"}}
}

func TestObservationIngestRequiresObserveIntentProtectedRefAndAlias(t *testing.T) {
	backend := newFakeResourceBackend()
	other := "tgt_0000000000000000000000000000000000000000000000000000000000000002"
	backend.targets[other] = &FleetTargetRecord{TargetID: other, Aliases: []string{"cluster:other", "environment:production/nyc3"}}
	// A target aliased by the raw GitHub environment name, not a lane.
	rawAlias := "tgt_0000000000000000000000000000000000000000000000000000000000000003"
	backend.targets[rawAlias] = &FleetTargetRecord{TargetID: rawAlias, Aliases: []string{"cluster:raw", "environment:staging"}}
	for name, target := range map[string]string{"prod": resTargetID, "elsewhere": other, "rawalias": rawAlias} {
		if _, err := backend.EnsureResource(context.Background(), name, target); err != nil {
			t.Fatal(err)
		}
	}
	router := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil))
	now := time.Now()

	mutate := func(edit func(*AccessPrincipal)) *AccessPrincipal {
		principal := observerPrincipal()
		edit(principal)
		return principal
	}
	denied := map[string]*AccessPrincipal{
		"no principal":        nil,
		"api:write human":     writerPrincipal(),
		"admin human":         fleetTargetPrincipal(ScopeAdmin),
		"fleet:operate human": fleetTargetPrincipal(ScopeFleetOperate),
		"admin CI":            mutate(func(p *AccessPrincipal) { p.Scopes = []string{ScopeAdmin} }),
		"intent apply":        mutate(func(p *AccessPrincipal) { p.CI.Intent = "apply" }),
		"intent recover":      mutate(func(p *AccessPrincipal) { p.CI.Intent = "recover" }),
		"intent missing":      mutate(func(p *AccessPrincipal) { p.CI.Intent = "" }),
		"intent unknown":      mutate(func(p *AccessPrincipal) { p.CI.Intent = "observe-all" }),
		"intent case":         mutate(func(p *AccessPrincipal) { p.CI.Intent = "Observe" }),
		"unprotected ref":     mutate(func(p *AccessPrincipal) { p.CI.RefProtected = false }),
		"other repository":    mutate(func(p *AccessPrincipal) { p.CI.Repository = "acme/other" }),
		"other repository id": mutate(func(p *AccessPrincipal) { p.CI.RepositoryID = "999" }),
		"other owner id":      mutate(func(p *AccessPrincipal) { p.CI.RepositoryOwnerID = "999" }),
		"not github":          mutate(func(p *AccessPrincipal) { p.CI.Provider = "other" }),
		"no run identity":     mutate(func(p *AccessPrincipal) { p.CI.RunID = "" }),
		// production is the GitHub environment of another lane (production/nyc3).
		"environment of another lane": mutate(func(p *AccessPrincipal) { p.CI.Environment = "production" }),
		"environment not bound":       mutate(func(p *AccessPrincipal) { p.CI.Environment = "qa" }),
	}
	for label, principal := range denied {
		rec := appendObservation(router, principal, "prod", okObservation(now))
		wantStatus := http.StatusForbidden
		if principal == nil {
			wantStatus = http.StatusUnauthorized
		}
		if rec.Code != wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", label, rec.Code, wantStatus, rec.Body)
		}
	}
	// The lane must resolve to THIS resource's target, through an
	// environment:<lane> alias (never the raw GitHub environment name).
	for _, name := range []string{"elsewhere", "rawalias"} {
		if rec := appendObservation(router, observerPrincipal(), name, okObservation(now)); rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "fleet_observation_identity_denied" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A token for another lane's GitHub environment is refused even when
	// that lane's alias names the resource's target.
	if rec := appendObservation(router, mutate(func(p *AccessPrincipal) { p.CI.Environment = "production" }), "elsewhere", okObservation(now)); rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "fleet_observation_identity_denied" {
		t.Fatalf("other lane's environment: %d %s", rec.Code, rec.Body)
	}
	if backend.appends != 0 {
		t.Fatalf("%d observations were stored by refused requests", backend.appends)
	}
	// No configured repository tuple fails closed.
	noRepo := newResourceRouter(NewFleetResourceRoutes(backend, &config.Config{}, nil, nil, nil))
	if rec := appendObservation(noRepo, observerPrincipal(), "prod", okObservation(now)); rec.Code != http.StatusForbidden {
		t.Fatalf("unconfigured repository: %d", rec.Code)
	}

	rec := appendObservation(router, observerPrincipal(), "prod", okObservation(now))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid observation: %d %s", rec.Code, rec.Body)
	}
	var first FleetObservationAppended
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil || first.Result != "applied" || !first.Applied || first.Sequence != 1 || first.Source != "runtime" ||
		first.Reporter != "github-actions:acme/fleet:run/9001/1" {
		t.Fatalf("applied observation = %+v %v", first, err)
	}
	// An older observation is stored but superseded; it can never clear a newer one.
	rec = appendObservation(router, observerPrincipal(), "prod", okObservation(now.Add(-time.Hour)))
	var second FleetObservationAppended
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &second) != nil || second.Result != "superseded" || second.Applied || second.Sequence != 2 {
		t.Fatalf("superseded observation: %d %s", rec.Code, rec.Body)
	}
	if rec := appendObservation(router, observerPrincipal(), "absent", okObservation(now)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: %d", rec.Code)
	}
	// Observations are readable with api:read, newest first.
	list := fleetTargetDo(router, http.MethodGet, resourcePath("prod")+"/observations", readerPrincipal(), "", nil)
	var observations FleetObservationList
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &observations) != nil || len(observations.Items) != 2 || observations.Items[0].Sequence != 2 {
		t.Fatalf("list observations: %d %s", list.Code, list.Body)
	}
}

func TestObservationIngestBounds(t *testing.T) {
	backend := newFakeResourceBackend()
	if _, err := backend.EnsureResource(context.Background(), "prod", resTargetID); err != nil {
		t.Fatal(err)
	}
	router := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil))
	now := time.Now()
	for label, observedAt := range map[string]time.Time{
		"older than 24h":  now.Add(-24*time.Hour - time.Minute),
		"more than 1m on": now.Add(2 * time.Minute),
	} {
		if rec := appendObservation(router, observerPrincipal(), "prod", okObservation(observedAt)); rec.Code != http.StatusBadRequest || decodeProblemCode(rec) != "fleet_observation_out_of_bounds" {
			t.Errorf("%s: %d %s", label, rec.Code, rec.Body)
		}
	}
	for label, observedAt := range map[string]time.Time{
		"23h ago":          now.Add(-23 * time.Hour),
		"30s of skew":      now.Add(30 * time.Second),
		"exactly in range": now,
	} {
		if rec := appendObservation(router, observerPrincipal(), "prod", okObservation(observedAt)); rec.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", label, rec.Code, rec.Body)
		}
	}
	refs := make([]string, controller.MaxObservationEvidenceRefs+1)
	for i := range refs {
		refs[i] = fmt.Sprintf("ref-%d", i)
	}
	big := okObservation(now)
	big["facts"] = map[string]interface{}{"blob": strings.Repeat("x", controller.MaxObservationFactsBytes)}
	tooManyRefs := okObservation(now)
	tooManyRefs["evidenceRefs"] = refs
	longRef := okObservation(now)
	longRef["evidenceRefs"] = []string{strings.Repeat("r", controller.MaxObservationStringBytes+1)}
	for label, body := range map[string]interface{}{
		"facts over 16KiB": big, "21 evidence refs": tooManyRefs, "513-byte ref": longRef,
		"unknown source":     map[string]interface{}{"source": "billing", "observedAt": now.UTC().Format(time.RFC3339Nano)},
		"missing observedAt": map[string]interface{}{"source": "runtime"},
		"unknown field":      map[string]interface{}{"source": "runtime", "observedAt": now.UTC().Format(time.RFC3339Nano), "reporter": "someone-else"},
		"not json":           []byte("{"),
	} {
		rec := appendObservation(router, observerPrincipal(), "prod", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", label, rec.Code, rec.Body)
		}
	}
	if backend.appends != 3 {
		t.Fatalf("stored %d observations, want only the 3 in-range ones", backend.appends)
	}
}

func TestResourceReadRequiresAPIRead(t *testing.T) {
	backend := newFakeResourceBackend()
	if _, err := backend.EnsureResource(context.Background(), "prod", resTargetID); err != nil {
		t.Fatal(err)
	}
	router := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil))
	for _, path := range []string{"/api/v1/fleet/resources", resourcePath("prod"), resourcePath("prod") + "/observations"} {
		if rec := fleetTargetDo(router, http.MethodGet, path, nil, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a principal: %d", path, rec.Code)
		}
		for label, principal := range map[string]*AccessPrincipal{
			"fleet:operate": fleetTargetPrincipal(ScopeFleetOperate), "api:write": writerPrincipal(), "observer CI": observerPrincipal(),
		} {
			if rec := fleetTargetDo(router, http.MethodGet, path, principal, "", nil); rec.Code != http.StatusForbidden {
				t.Errorf("GET %s as %s: %d", path, label, rec.Code)
			}
		}
		if rec := fleetTargetDo(router, http.MethodGet, path, readerPrincipal(), "", nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s as api:read: %d %s", path, rec.Code, rec.Body)
		}
	}
	// Create needs platform:operate (Q7, as registration).
	for label, principal := range map[string]*AccessPrincipal{"api:read": readerPrincipal(), "api:write": writerPrincipal()} {
		if rec := fleetTargetDo(router, http.MethodPost, resourcePath("new"), principal, "", map[string]string{"targetId": resTargetID}); rec.Code != http.StatusForbidden {
			t.Errorf("create as %s: %d", label, rec.Code)
		}
	}
	ciWriter := observerPrincipal()
	ciWriter.Scopes = []string{ScopePlatformOperate}
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("new"), ciWriter, "", map[string]string{"targetId": resTargetID}); rec.Code != http.StatusForbidden {
		t.Errorf("create as CI: %d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodGet, resourcePath("absent"), readerPrincipal(), "", nil); rec.Code != http.StatusNotFound || decodeProblemCode(rec) != "fleet_resource_not_found" {
		t.Errorf("unknown resource: %d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodGet, resourcePath("Bad_Name"), readerPrincipal(), "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d", rec.Code)
	}
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("new"), platformPrincipal(), "", map[string]string{"targetId": "tgt_missing"}); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed target: %d", rec.Code)
	}
	unregistered := "tgt_" + strings.Repeat("f", 64)
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("new"), platformPrincipal(), "", map[string]string{"targetId": unregistered}); rec.Code != http.StatusNotFound || decodeProblemCode(rec) != "fleet_target_not_found" {
		t.Errorf("unregistered target: %d %s", rec.Code, rec.Body)
	}
	otherTarget := "tgt_" + strings.Repeat("e", 64)
	backend.targets[otherTarget] = &FleetTargetRecord{TargetID: otherTarget}
	if rec := fleetTargetDo(router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": otherTarget}); rec.Code != http.StatusConflict || decodeProblemCode(rec) != "fleet_resource_target_mismatch" {
		t.Errorf("rebinding a resource: %d %s", rec.Code, rec.Body)
	}
}

func TestResourceListPagesByName(t *testing.T) {
	backend := newFakeResourceBackend()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if _, err := backend.EnsureResource(context.Background(), name, resTargetID); err != nil {
			t.Fatal(err)
		}
	}
	router := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil))
	var seen []string
	after := ""
	for page := 0; page < 5; page++ {
		path := "/api/v1/fleet/resources?limit=2"
		if after != "" {
			path += "&after=" + after
		}
		rec := fleetTargetDo(router, http.MethodGet, path, readerPrincipal(), "", nil)
		var list FleetResourceList
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body)
		}
		for _, item := range list.Items {
			seen = append(seen, item.Name)
		}
		if list.Next == "" {
			break
		}
		after = list.Next
	}
	if strings.Join(seen, ",") != "a,b,c,d,e" {
		t.Fatalf("paged names = %v", seen)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=x", "after=Bad%20Name"} {
		if rec := fleetTargetDo(router, http.MethodGet, "/api/v1/fleet/resources?"+query, readerPrincipal(), "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", query, rec.Code)
		}
	}
}

func TestApprovalPolicyReportsNonEnforcedReview(t *testing.T) {
	backend := newFakeResourceBackend()
	if _, err := backend.EnsureResource(context.Background(), "prod", resTargetID); err != nil {
		t.Fatal(err)
	}
	// The GitHub App dispatch path is available: authorizedDispatch holds.
	router := newResourceRouter(NewFleetResourceRoutes(backend, fleetResourceConfig(), &fakeResolver{}, nil, nil))
	read := func() map[string]interface{} {
		rec := fleetTargetDo(router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil)
		var body struct {
			ApprovalPolicy map[string]interface{} `json:"approvalPolicy"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("read: %d %s", rec.Code, rec.Body)
		}
		return body.ApprovalPolicy
	}
	want := func(label string, got map[string]interface{}, expected map[string]interface{}) {
		t.Helper()
		for key, value := range expected {
			if got[key] != value {
				t.Errorf("%s: approvalPolicy.%s = %v, want %v (%v)", label, key, got[key], value, got)
			}
		}
	}
	// Whatever the desired revision, review controls are never claimed.
	notEnforced := map[string]interface{}{"environmentReviewers": "not_enforced", "independentReview": "not_enforced", "ownerApprovalEnvelope": false, "authorizedDispatch": true}
	none := read()
	want("no desired", none, notEnforced)
	want("no desired", none, map[string]interface{}{"basis": "none", "protectedBranch": false, "mergedPullRequest": false, "planWorkflowSucceeded": false, "boundPlanDigest": false})

	backend.resources["prod"].Desired = controller.DesiredRevision{Generation: 1, CommitSHA: approvedSHA, PlanID: uuid.NewString(), Repository: testFleetRepoName, Verification: controller.VerificationGitHubMergedPlan}
	merged := read()
	want("merged plan", merged, notEnforced)
	want("merged plan", merged, map[string]interface{}{"basis": "github-merged-plan", "protectedBranch": true, "mergedPullRequest": true, "planWorkflowSucceeded": true, "boundPlanDigest": true})

	backend.resources["prod"].Desired = controller.DesiredRevision{Generation: 2, CommitSHA: approvedSHA, Verification: controller.VerificationOperatorDeclared}
	declared := read()
	want("operator-declared", declared, notEnforced)
	want("operator-declared", declared, map[string]interface{}{"basis": "operator-declared", "protectedBranch": false, "mergedPullRequest": false, "planWorkflowSucceeded": false, "boundPlanDigest": false})

	// The stored Policy field is ignored: the response is always derived.
	backend.resources["prod"].Policy = controller.ApprovalPolicy{EnvironmentReviewers: "required", IndependentReview: "required", ProtectedBranch: true}
	want("stored policy ignored", read(), notEnforced)

	// No App, or a configured but unusable one: no authorized-dispatch
	// control exists, so none is claimed.
	for label, routes := range map[string]*FleetResourceRoutes{
		"no App":     NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil),
		"broken App": NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, errors.New("bad key"), nil),
	} {
		router = newResourceRouter(routes)
		want(label, read(), map[string]interface{}{"authorizedDispatch": false, "environmentReviewers": "not_enforced", "independentReview": "not_enforced"})
	}
}

// TestResourceReadExplainsRecord is the proposal's success measure: one GET
// says what was requested, what exists, what is running, what blocks progress
// and which supported action comes next, with freshness re-applied at read
// time and the controller's liveness reported.
func TestResourceReadExplainsRecord(t *testing.T) {
	now := time.Now().UTC()
	derivedAt := now.Add(-time.Minute)
	planID := uuid.NewString()
	backend := newFakeResourceBackend()
	resource, _ := backend.EnsureResource(context.Background(), "prod", resTargetID)
	resource.Desired = controller.DesiredRevision{Generation: 2, PlanID: planID, CommitSHA: approvedSHA, Repository: testFleetRepoName,
		Verification: controller.VerificationGitHubMergedPlan, AcceptedAt: derivedAt, AcceptedBy: "operator"}
	resource.Watermarks = map[string]controller.Watermark{
		controller.SourceProvider: {ObservedAt: derivedAt, Sequence: 1}, controller.SourceRuntime: {ObservedAt: derivedAt, Sequence: 2},
	}
	resource.Revision = 4
	// The reconciler derived this a minute ago: provider fresh with 2 of 3
	// nodes enrolled, runtime ready, and a held fence whose dispatch was
	// submitted but has no live attempt (Uncertain).
	resource.Status = controller.DeriveStatus(controller.Input{
		Resource:       *resource,
		Fence:          lifecycle.FenceFacts{TargetID: resTargetID, Generation: 3, Held: true, HolderPlanID: planID, AuthorityEpoch: 1},
		Holder:         lifecycle.HolderFacts{DispatchState: "dispatched", DispatchCreatedAt: derivedAt},
		AuthorityEpoch: 1, HolderDispatchRunID: 4242,
		LatestObservations: map[string]controller.Observation{
			controller.SourceProvider: {Sequence: 1, Source: controller.SourceProvider, ObservedAt: derivedAt, Facts: controller.ObservationFacts{"targetId": resTargetID, "enrolledNodes": 2.0, "expectedNodes": 3.0}, EvidenceRefs: []string{"run/1"}},
			controller.SourceRuntime:  {Sequence: 2, Source: controller.SourceRuntime, ObservedAt: derivedAt, Facts: controller.ObservationFacts{"ready": true, "ingressReady": true}},
		},
		TiedObservations: map[string][]controller.Observation{}, Now: derivedAt,
	})
	liveness := fakeLiveness{at: now.Add(-30 * time.Second), interval: time.Minute}
	routes := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, liveness)
	routes.now = func() time.Time { return now }
	router := newResourceRouter(routes)

	rec := fleetTargetDo(router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	view := decodeResource(t, rec)
	byType := map[string]FleetConditionView{}
	for _, c := range view.Status.Conditions {
		byType[c.Type] = c
	}
	// What was requested.
	if view.Desired.Generation != 2 || view.Desired.CommitSHA != approvedSHA || view.Desired.PlanID != planID || view.Desired.Verification != controller.VerificationGitHubMergedPlan {
		t.Fatalf("requested = %+v", view.Desired)
	}
	// What exists.
	if byType[controller.ConditionNodesEnrolled].Status != controller.StatusFalse || byType[controller.ConditionNodesEnrolled].Reason != controller.ReasonEnrolledBelowExpected ||
		byType[controller.ConditionRuntimeReady].Status != controller.StatusTrue || view.Observed[controller.SourceProvider].Sequence != 1 || view.Observed[controller.SourceRuntime].Sequence != 2 {
		t.Fatalf("exists = %+v observed=%+v", view.Status.Conditions, view.Observed)
	}
	// What is running.
	if view.Status.Active == nil || view.Status.Active.PlanID != planID || view.Status.Active.DispatchRunID != "4242" || view.Status.Active.Occupancy != string(lifecycle.OccupancyUncertain) ||
		view.Status.Active.OccupancyReason != lifecycle.ReasonNoLiveAttempt || view.Status.Active.FenceGeneration != 3 {
		t.Fatalf("running = %+v", view.Status.Active)
	}
	// What blocks progress.
	if view.Blocker == nil || view.Blocker.Kind != "execution" || view.Blocker.Reason != lifecycle.ReasonNoLiveAttempt || view.Blocker.PlanID != planID {
		t.Fatalf("blocker = %+v", view.Blocker)
	}
	// Which supported action comes next.
	if view.Status.NextAction != controller.NextActionResolveUncertainOutcome {
		t.Fatalf("nextAction = %q", view.Status.NextAction)
	}
	// The approval policy and controller liveness travel with the record.
	if view.ApprovalPolicy.Basis != controller.VerificationGitHubMergedPlan || view.ApprovalPolicy.EnvironmentReviewers != "not_enforced" || view.ApprovalPolicy.IndependentReview != "not_enforced" {
		t.Fatalf("policy = %+v", view.ApprovalPolicy)
	}
	if view.Controller.State != "running" || view.Controller.LastRescanAt == nil || view.Controller.RescanIntervalSeconds != 60 {
		t.Fatalf("controller = %+v", view.Controller)
	}
	// Freshness is re-applied at read time: 20 minutes later the same stored
	// status reads Unknown/ObservationStale, and a controller that has not
	// rescanned for more than three intervals reads stale.
	routes.now = func() time.Time { return now.Add(20 * time.Minute) }
	later := decodeResource(t, fleetTargetDo(router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil))
	for _, c := range later.Status.Conditions {
		if c.Type == controller.ConditionReconciliationRequired {
			continue
		}
		if c.Status != controller.StatusUnknown || (c.Reason != controller.ReasonObservationStale && c.Reason != controller.ReasonNoObservation) {
			t.Fatalf("%s after 20m = %s/%s, want Unknown/ObservationStale", c.Type, c.Status, c.Reason)
		}
	}
	if later.Controller.State != "stale" {
		t.Fatalf("controller after 20m = %+v", later.Controller)
	}
	// With the fence free, a stale-only record blocks on observations and the
	// next action is to refresh them.
	resource.Status.Active = nil
	resource.Status.NextAction = controller.NextActionNone
	for i := range resource.Status.Conditions {
		if resource.Status.Conditions[i].Type == controller.ConditionReconciliationRequired {
			resource.Status.Conditions[i].Status, resource.Status.Conditions[i].Reason = controller.StatusFalse, controller.ReasonUpToDate
		}
	}
	free := decodeResource(t, fleetTargetDo(router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil))
	if free.Status.NextAction != controller.NextActionRefreshObservations || free.Blocker == nil || free.Blocker.Kind != "observation" {
		t.Fatalf("stale record: next=%q blocker=%+v", free.Status.NextAction, free.Blocker)
	}
	// No reconciler in this process: liveness is unknown.
	unknown := NewFleetResourceRoutes(backend, fleetResourceConfig(), nil, nil, nil)
	if got := decodeResource(t, fleetTargetDo(newResourceRouter(unknown), http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil)); got.Controller.State != "unknown" || got.Controller.LastRescanAt != nil {
		t.Fatalf("controller without a reconciler = %+v", got.Controller)
	}
}

func TestFleetObservationPathMatcher(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/fleet/resources/prod/observations":      true,
		"/api/v1/fleet/resources/attempts/observations":  true,
		"/api/v1/fleet/resources/prod/desired":           false,
		"/api/v1/fleet/resources/prod/observations/":     false,
		"/api/v1/fleet/resources//observations":          false,
		"/api/v1/fleet/resources/../observations":        false,
		"/api/v1/fleet/resources/a/b/observations":       false,
		"/api/v1/fleet/resources/prod/observations-more": false,
		"/api/v1/fleet/plans/prod/observations":          false,
	} {
		if got := FleetObservationPath(path); got != want {
			t.Errorf("FleetObservationPath(%q) = %t, want %t", path, got, want)
		}
	}
}

// TestResourceRoutesAbsentFromGeneralRouter pins Q6: the resource routes (and
// the reconciler's liveness wiring) are mounted only in the PG Fleet
// authority-only router function and the etcd Fleet runtime, never on the
// general router.
func TestResourceRoutesAbsentFromGeneralRouter(t *testing.T) {
	source, err := os.ReadFile("../main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func fleetAuthorityOnlyRouterWithHandler(")
	end := strings.Index(text, "func newFleetAuthorityOnlyHandler(")
	if start < 0 || end < start {
		t.Fatal("cannot locate the Fleet authority-only router in main.go")
	}
	for _, needle := range []string{"FleetResourceRoutes()", `"/fleet/resources`, `"/v1/fleet/resources`, "/api/v1/fleet/resources/{name}"} {
		for from := 0; ; {
			i := strings.Index(text[from:], needle)
			if i < 0 {
				break
			}
			i += from
			if i < start || i >= end {
				// Only comments, the capability endpoint map and the bearer
				// deferral helper may mention the path outside the router.
				lineStart := strings.LastIndex(text[:i], "\n") + 1
				lineEnd := i + strings.Index(text[i:], "\n")
				line := strings.TrimSpace(text[lineStart:lineEnd])
				if !strings.HasPrefix(line, "//") && !strings.Contains(line, `"fleetResources"`) {
					t.Errorf("main.go registers %q outside the Fleet authority-only router: %s", needle, line)
				}
			}
			from = i + len(needle)
		}
	}
	if strings.Count(text, "h.FleetResourceRoutes()") != 1 {
		t.Fatal("expected exactly one resource route mount in main.go")
	}
	if !strings.Contains(text[start:end], "fleetResources := h.FleetResourceRoutes()") {
		t.Fatal("resource routes are not mounted in the Fleet authority-only router")
	}
}
