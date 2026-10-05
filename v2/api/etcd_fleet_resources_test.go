package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// TestEtcdFleetResourceRoutesEtcd drives the real route mount
// (registerEtcdFleetResourceRoutes) with managed-token auth against real
// etcd: a registered target, an environment-bound CI observation, an
// operator-declared desired revision with the expectedRevision CAS, a real
// reconcile and rescan, and the single read that explains the record.
func TestEtcdFleetResourceRoutesEtcd(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		if os.Getenv("NORN_TEST_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("NORN_TEST_ETCD_ENDPOINTS is required")
		}
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/fleet-resource-routes/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("fleet-resource-routes-test-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "0123456789abcdef0123456789abcdef"
	cfg := &config.Config{APIToken: secret, GitHubActionsFleetAllowedRepository: "acme/fleet@101@202", FleetGitHubRepository: "acme/fleet",
		FleetGitHubConfigPath: "environments/staging/nyc3/cluster.yaml", FleetGitHubEnvironment: "staging"}
	identities := etcdstore.NewAuthStore(client, prefix)
	reconciler := newFleetReconciler(operations, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") })
	router := chi.NewRouter()
	registerEtcdFleetTargetRoutes(router, cfg, identities, operations, nil)
	registerEtcdFleetResourceRoutes(router, cfg, identities, operations, nil, reconciler)

	tokens := map[string]string{}
	token := func(scopes ...string) string {
		if cached, ok := tokens[strings.Join(scopes, ",")]; ok {
			return cached
		}
		record, err := handler.NewManagedAccessTokenRecord("operator", scopes, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := identities.RecordAccessToken(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		signed, err := handler.SignManagedAccessToken(secret, record)
		if err != nil {
			t.Fatal(err)
		}
		tokens[strings.Join(scopes, ",")] = signed
		return signed
	}
	ciToken := func(intent string) string {
		ci := &handler.CIIdentity{Provider: "github-actions", Repository: "acme/fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "7", RunAttempt: "1",
			Ref: "refs/heads/main", RefType: "branch", EventName: "push", Environment: "staging", SHA: strings.Repeat("c", 40), RefProtected: true, Intent: intent}
		signed, err := signedProcessManagedCIToken(secret, identities, ci)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	call := func(method, path, key, bearer string, body interface{}) *httptest.ResponseRecorder {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+bearer)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodPost, "/api/v1/fleet/targets", "reg-1", token(handler.ScopePlatformOperate), map[string]interface{}{
		"provider": "digitalocean", "providerAccount": "acct", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:res-routes", "environment:staging/nyc3"}})
	var registered struct {
		Ref string `json:"ref"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &registered) != nil || !strings.HasPrefix(registered.Ref, "tgt_") {
		t.Fatalf("register target: %d %s", rec.Code, rec.Body)
	}
	targetID := registered.Ref
	path := "/api/v1/fleet/resources/prod"

	for _, scope := range []string{handler.ScopeAPIRead, handler.ScopeAPIWrite} {
		if rec := call(http.MethodPost, path, "", token(scope), map[string]string{"targetId": targetID}); rec.Code != http.StatusForbidden {
			t.Fatalf("create with %s: %d", scope, rec.Code)
		}
	}
	if rec := call(http.MethodPost, path, "", token(handler.ScopePlatformOperate), map[string]string{"targetId": "tgt_" + strings.Repeat("0", 64)}); rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered target: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, path, "", token(handler.ScopePlatformOperate), map[string]string{"targetId": targetID}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, path, "", token(handler.ScopePlatformOperate), map[string]string{"targetId": targetID}); rec.Code != http.StatusOK {
		t.Fatalf("idempotent create: %d %s", rec.Code, rec.Body)
	}
	read := func() handler.FleetResourceView {
		t.Helper()
		rec := call(http.MethodGet, path, "", token(handler.ScopeAPIRead), nil)
		var view handler.FleetResourceView
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
			t.Fatalf("read: %d %s", rec.Code, rec.Body)
		}
		return view
	}
	if rec := call(http.MethodGet, path, "", token(handler.ScopeFleetOperate), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("read without api:read: %d", rec.Code)
	}
	if fresh := read(); fresh.Controller.State != "unknown" || fresh.Desired.Generation != 0 || fresh.ApprovalPolicy.Basis != "none" {
		t.Fatalf("fresh resource = %+v", fresh)
	}

	// Observations: the observe intent only, from a workload identity.
	observed := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body := map[string]interface{}{"source": "runtime", "observedAt": observed, "facts": map[string]interface{}{"ready": true, "ingressReady": true}, "evidenceRefs": []string{"run/7"}}
	for label, bearer := range map[string]string{"no CI": token(handler.ScopeFleetOperate), "apply intent": ciToken("apply"), "api:write": token(handler.ScopeAPIWrite)} {
		if rec := call(http.MethodPost, path+"/observations", "", bearer, body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s observation: %d %s", label, rec.Code, rec.Body)
		}
	}
	rec = call(http.MethodPost, path+"/observations", "", ciToken(handler.FleetObserveIntent), body)
	var appended handler.FleetObservationAppended
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &appended) != nil || appended.Result != "applied" || appended.Reporter != "github-actions:acme/fleet:run/7/1" {
		t.Fatalf("observation: %d %s", rec.Code, rec.Body)
	}
	body["observedAt"] = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if rec := call(http.MethodPost, path+"/observations", "", ciToken(handler.FleetObserveIntent), body); !strings.Contains(rec.Body.String(), `"result":"superseded"`) {
		t.Fatalf("older observation: %d %s", rec.Code, rec.Body)
	}
	body["observedAt"] = time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if rec := call(http.MethodPost, path+"/observations", "", ciToken(handler.FleetObserveIntent), body); rec.Code != http.StatusBadRequest {
		t.Fatalf("future observation: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, path+"/observations", "", token(handler.ScopeAPIRead), nil); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"sequence"`) != 2 {
		t.Fatalf("list observations: %d %s", rec.Code, rec.Body)
	}

	// Desired (no GitHub App here: operator-declared) with the revision CAS.
	sha := strings.Repeat("e", 40)
	desired := func(expected int64) int {
		return call(http.MethodPost, path+"/desired", "", token(handler.ScopeAPIWrite), map[string]interface{}{"commitSha": sha, "expectedRevision": expected}).Code
	}
	if code := desired(0); code != http.StatusOK {
		t.Fatalf("desired: %d", code)
	}
	if code := desired(0); code != http.StatusConflict {
		t.Fatalf("stale desired: %d", code)
	}
	if code := desired(1); code != http.StatusOK {
		t.Fatalf("desired at revision 1: %d", code)
	}

	// Reconcile for real and run one rescan; then one read explains it all.
	if _, err := operations.ReconcileFleetResource(context.Background(), "prod", controller.DeriveStatus, nil); err != nil {
		t.Fatal(err)
	}
	reconciler.Step(context.Background())
	view := read()
	runtimeReady := ""
	for _, c := range view.Status.Conditions {
		if c.Type == controller.ConditionRuntimeReady {
			runtimeReady = c.Status
		}
	}
	if view.TargetID != targetID || view.Desired.Generation != 2 || view.Desired.CommitSHA != sha || view.Desired.Verification != controller.VerificationOperatorDeclared ||
		runtimeReady != controller.StatusTrue || view.Observed[controller.SourceRuntime].Sequence != 1 || view.Status.EvaluatedAt == nil ||
		view.Status.NextAction != controller.NextActionRefreshObservations || view.Blocker == nil || view.Blocker.Reason != controller.ReasonDesiredNotApplied ||
		view.Controller.State != "running" || view.Controller.LastRescanAt == nil || view.ApprovalPolicy.ProtectedBranch || view.ApprovalPolicy.AuthorizedDispatch || view.ApprovalPolicy.IndependentReview != "not_enforced" {
		t.Fatalf("record = %+v", view)
	}
	var list handler.FleetResourceList
	if rec := call(http.MethodGet, "/api/v1/fleet/resources", "", token(handler.ScopeAPIRead), nil); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].Name != "prod" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

// TestFleetResourceControlScope pins how the generic router check treats the
// resource paths: everything stays on the ordinary scope check, and only a
// fleet:operate workload identity on exactly POST .../observations is let
// through to the handler (which then enforces the observe intent).
func TestFleetResourceControlScope(t *testing.T) {
	scope := func(method, path string) string {
		return controlScopeForRequest(httptest.NewRequest(method, path, nil))
	}
	if got := scope(http.MethodGet, "/api/v1/fleet/resources"); got != handler.ScopeAPIRead {
		t.Fatalf("list scope = %q", got)
	}
	if got := scope(http.MethodPost, "/api/v1/fleet/resources/prod"); got != handler.ScopePlatformOperate {
		t.Fatalf("create scope = %q", got)
	}
	for _, path := range []string{"/api/v1/fleet/resources/prod/desired", "/api/v1/fleet/resources/prod/observations", "/api/v1/fleet/resources/prod%2Fdesired"} {
		if got := scope(http.MethodPost, path); got != handler.ScopeAPIWrite {
			t.Fatalf("POST %s scope = %q", path, got)
		}
	}
	ci := &handler.AccessPrincipal{Scopes: []string{handler.ScopeFleetOperate}, CI: &handler.CIIdentity{}}
	human := &handler.AccessPrincipal{Scopes: []string{handler.ScopeFleetOperate}}
	for _, tc := range []struct {
		method, path string
		principal    *handler.AccessPrincipal
		want         bool
	}{
		{http.MethodPost, "/api/v1/fleet/resources/prod/observations", ci, true},
		{http.MethodPost, "/api/v1/fleet/resources/attempts/observations", ci, true},
		{http.MethodPost, "/api/v1/fleet/resources/prod/desired", ci, false},
		{http.MethodPost, "/api/v1/fleet/resources/prod", ci, false},
		{http.MethodGet, "/api/v1/fleet/resources/prod/observations", ci, false},
		{http.MethodPost, "/api/v1/fleet/resources/prod/observations", human, false},
		{http.MethodPost, "/api/v1/fleet/resources/prod/observations/", ci, false},
		// %2F routes on the raw path in chi (to Create), so it never matches.
		{http.MethodPost, "/api/v1/fleet/resources/prod%2Fobservations", ci, false},
		{http.MethodPost, "/api/v1/fleet/resources/%70rod/observations", ci, false},
		{http.MethodPost, "/api/v1/fleet/targets", ci, false},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := allowsFleetObservationIngest(tc.principal, req); got != tc.want {
			t.Errorf("%s %s ci=%t: %t, want %t", tc.method, tc.path, tc.principal.CI != nil, got, tc.want)
		}
	}
}

// TestFleetResourceAuthorityOnlyRoutesAndCapabilities pins Q6 placement on the
// PG authority-only router (the routes are mounted and reach their handlers),
// the optional observe intent in startup validation, and the additive
// capability on both backends.
func TestFleetResourceAuthorityOnlyRoutesAndCapabilities(t *testing.T) {
	valid := fleetAuthorityOnlyTestConfig(t)
	router := fleetAuthorityOnlyRouter(valid, nil)
	for _, route := range [][2]string{
		{http.MethodGet, "/api/v1/fleet/resources"}, {http.MethodPost, "/api/v1/fleet/resources/prod"}, {http.MethodGet, "/api/v1/fleet/resources/prod"},
		{http.MethodPost, "/api/v1/fleet/resources/prod/desired"}, {http.MethodGet, "/api/v1/fleet/resources/prod/observations"}, {http.MethodPost, "/api/v1/fleet/resources/prod/observations"},
	} {
		req := httptest.NewRequest(route[0], route[1], strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+valid.APIToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s %s is not registered on the authority-only router: %d", route[0], route[1], rec.Code)
		}
	}
	candidate := *valid
	candidate.GitHubActionsFleetAllowedIntents = []string{"apply", "recover", "observe"}
	if err := validateControlSecurity(&candidate); err != nil {
		t.Fatalf("observe intent rejected: %v", err)
	}
	for _, intents := range [][]string{{"apply", "observe"}, {"apply", "recover", "observe", "stage"}, {"apply", "recover", "Observe"}} {
		candidate.GitHubActionsFleetAllowedIntents = intents
		if err := validateControlSecurity(&candidate); err == nil {
			t.Fatalf("intents %v were accepted", intents)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	var capabilities struct {
		Features  []string          `json:"features"`
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if !containsCapability(capabilities.Features, "fleet-resource-v1") || capabilities.Endpoints["fleetResources"] != "/api/v1/fleet/resources" {
		t.Fatalf("authority-only capabilities: %#v", capabilities)
	}
	etcd := etcdFleetCapabilities(&config.Config{}, false)
	features, _ := etcd["features"].([]string)
	endpoints, _ := etcd["endpoints"].(map[string]string)
	if !containsCapability(features, "fleet-resource-v1") || endpoints["fleetResources"] != "/api/v1/fleet/resources" {
		t.Fatalf("etcd capabilities: %#v", etcd)
	}
}
