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
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// TestFleetTargetControlScopeAttemptsMatcher pins M17 at the router: only plan-scoped paths defer
// their scope decision to the handler through the /attempts and
// /reconciliations matcher. A resource or target named attempts-* (or ending
// in /reconciliations) must still face the generic scope check.
func TestFleetTargetControlScopeAttemptsMatcher(t *testing.T) {
	scope := func(method, path string) string {
		return controlScopeForRequest(httptest.NewRequest(method, path, nil))
	}
	for _, path := range []string{
		"/api/v1/fleet/plans/p/attempts", "/api/v1/fleet/plans/p/attempts/a/heartbeat", "/api/v1/fleet/plans/p/reconciliations",
	} {
		if got := scope(http.MethodPost, path); got != "" {
			t.Fatalf("POST %s must still defer to the handler, got %q", path, got)
		}
	}
	for _, path := range []string{
		"/api/v1/fleet/resources/attempts-prod/desired", "/api/v1/fleet/resources/attempts/observations",
		"/api/v1/fleet/resources/x/reconciliations", "/api/v1/fleet/targets/attempts-x/fence/release-x",
		"/api/v1/fleet/plans/x/attempts-foo", "/api/v1/fleet/plans/x/attempts/", "/api/v1/fleet/plans/x/../attempts",
		"/api/v1/fleet/plans/x%2Fy/attempts", "/api/v1/fleet/plans/attempts-x/github/dispatch", "/api/v1/fleet/node-pools/attempts/plan",
	} {
		if got := scope(http.MethodPost, path); got != handler.ScopeAPIWrite {
			t.Fatalf("POST %s skipped the scope check: %q", path, got)
		}
	}
	if got := scope(http.MethodPost, "/api/v1/fleet/targets"); got != handler.ScopePlatformOperate {
		t.Fatalf("register scope = %q", got)
	}
	for _, path := range []string{"/api/v1/fleet/targets/abandon-plan", "/api/v1/fleet/targets/tgt_x/fence/release"} {
		if got := scope(http.MethodPost, path); got != handler.ScopeAdmin {
			t.Fatalf("POST %s scope = %q", path, got)
		}
	}
	if got := scope(http.MethodGet, "/api/v1/fleet/targets/tgt_x"); got != handler.ScopeAPIRead {
		t.Fatalf("GET target scope = %q", got)
	}
}

// TestEtcdFleetTargetRoutesEtcd drives the real route mount
// (registerEtcdFleetTargetRoutes) with managed-token auth against real etcd.
func TestEtcdFleetTargetRoutesEtcd(t *testing.T) {
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
	prefix := "/norn-test/fleet-target-routes/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("fleet-target-routes-test-key-0000000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "0123456789abcdef0123456789abcdef"
	cfg := &config.Config{APIToken: secret}
	router := chi.NewRouter()
	registerEtcdFleetTargetRoutes(router, cfg, activeManagedIdentity{}, operations, nil)
	tokens := map[string]string{}
	token := func(scopes ...string) string {
		// One stable token per scope set: the actor is the token ID, so a
		// replay needs the same credential.
		if cached, ok := tokens[strings.Join(scopes, ",")]; ok {
			return cached
		}
		record, err := handler.NewManagedAccessTokenRecord("operator", scopes, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		signed, err := handler.SignManagedAccessToken(secret, record)
		if err != nil {
			t.Fatal(err)
		}
		tokens[strings.Join(scopes, ",")] = signed
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
	register := map[string]interface{}{"provider": "digitalocean", "providerAccount": "acct", "stateBackend": "s3://bucket/state", "aliases": []string{"cluster:fleet-routes"}}
	if rec := call(http.MethodPost, "/api/v1/fleet/targets", "reg-1", token(handler.ScopeAPIWrite), register); rec.Code != http.StatusForbidden {
		t.Fatalf("register without platform:operate: %d", rec.Code)
	}
	rec := call(http.MethodPost, "/api/v1/fleet/targets", "reg-1", token(handler.ScopePlatformOperate), register)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var operation struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &operation); err != nil || !strings.HasPrefix(operation.Ref, "tgt_") {
		t.Fatalf("register body: %s %v", rec.Body, err)
	}
	if replay := call(http.MethodPost, "/api/v1/fleet/targets", "reg-1", token(handler.ScopePlatformOperate), register); replay.Code != http.StatusOK {
		t.Fatalf("register replay: %d", replay.Code)
	}
	targetPath := "/api/v1/fleet/targets/" + operation.Ref
	if rec := call(http.MethodGet, targetPath, "", token(handler.ScopeFleetOperate), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("get without api:read: %d", rec.Code)
	}
	view := call(http.MethodGet, targetPath, "", token(handler.ScopeAPIRead), nil)
	var target handler.FleetTargetView
	if view.Code != http.StatusOK || json.Unmarshal(view.Body.Bytes(), &target) != nil || target.TargetID != operation.Ref || target.Fence.Held || target.Fence.Occupancy != "Free" {
		t.Fatalf("get target: %d %s", view.Code, view.Body)
	}
	if fence := call(http.MethodGet, targetPath+"/fence", "", token(handler.ScopeAPIRead), nil); fence.Code != http.StatusOK || !strings.Contains(fence.Body.String(), `"occupancy":"Free"`) {
		t.Fatalf("get fence: %d %s", fence.Code, fence.Body)
	}
	release := map[string]interface{}{"mode": "abandon", "expectedGeneration": target.Fence.Generation, "reason": "test"}
	if rec := call(http.MethodPost, targetPath+"/fence/release", "rel-1", token(handler.ScopePlatformOperate), release); rec.Code != http.StatusForbidden {
		t.Fatalf("release without admin: %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/v1/fleet/targets/abandon-plan", "ap-1", token(handler.ScopePlatformOperate), map[string]interface{}{"planId": uuid.NewString()}); rec.Code != http.StatusForbidden {
		t.Fatalf("abandon-plan without admin: %d", rec.Code)
	}
	free := call(http.MethodPost, targetPath+"/fence/release", "rel-1", token(handler.ScopeAdmin), release)
	if free.Code != http.StatusConflict || !strings.Contains(free.Body.String(), "fleet_target_fence_not_held") {
		t.Fatalf("release of a free fence: %d %s", free.Code, free.Body)
	}
	stale := map[string]interface{}{"mode": "abandon", "expectedGeneration": target.Fence.Generation + 5}
	if rec := call(http.MethodPost, targetPath+"/fence/release", "rel-2", token(handler.ScopeAdmin), stale); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "fleet_target_expected_generation_mismatch") {
		t.Fatalf("stale generation: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/fleet/targets/abandon-plan", "ap-2", token(handler.ScopeAdmin), map[string]interface{}{"planId": uuid.NewString()}); rec.Code != http.StatusNotFound {
		t.Fatalf("abandon-plan of an unknown plan: %d %s", rec.Code, rec.Body)
	}
}

// TestFleetTargetAuthorityOnlyRoutesAndCapabilities pins Q6 placement on the
// PG Fleet authority-only router (the routes are mounted and reach their
// handlers) and the additive capability on both backends.
func TestFleetTargetAuthorityOnlyRoutesAndCapabilities(t *testing.T) {
	valid := fleetAuthorityOnlyTestConfig(t)
	router := fleetAuthorityOnlyRouter(valid, nil)
	for _, route := range [][2]string{
		{http.MethodPost, "/api/v1/fleet/targets"}, {http.MethodPost, "/api/v1/fleet/targets/abandon-plan"},
		{http.MethodGet, "/api/v1/fleet/targets/tgt_bad"}, {http.MethodGet, "/api/v1/fleet/targets/tgt_bad/fence"},
		{http.MethodPost, "/api/v1/fleet/targets/tgt_bad/fence/release"},
	} {
		req := httptest.NewRequest(route[0], route[1], strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+valid.APIToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s %s is not registered on the authority-only router: %d", route[0], route[1], rec.Code)
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
	if !containsCapability(capabilities.Features, "fleet-target-fence-v1") || capabilities.Endpoints["fleetTargets"] != "/api/v1/fleet/targets" {
		t.Fatalf("authority-only capabilities: %#v", capabilities)
	}
	etcd := etcdFleetCapabilities(&config.Config{}, false)
	features, _ := etcd["features"].([]string)
	endpoints, _ := etcd["endpoints"].(map[string]string)
	if !containsCapability(features, "fleet-target-fence-v1") || endpoints["fleetTargets"] != "/api/v1/fleet/targets" {
		t.Fatalf("etcd capabilities: %#v", etcd)
	}
}
