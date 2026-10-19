package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestEtcdScaleHTTPAcceptsPinnedManagedJobAndRejectsStaleWorker(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/scale-http/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-scale-http-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(client, prefix)
	rootID := uuid.NewString()
	now := time.Now().UTC()
	if err := identities.RecordAccessToken(ctx, &store.AccessToken{JTI: rootID, Subject: "operator", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	apps := t.TempDir()
	if err := os.Mkdir(filepath.Join(apps, "widgets"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "name: widgets\ndeploy: true\nregions:\n  us-central:\n    nomadRegion: global\n    datacenters: [dc1]\nprocesses:\n  web:\n    command: sleep 60\n    scaling:\n      min: 2\n      max: 3\n    health:\n      path: /ready\n"
	if err := os.WriteFile(filepath.Join(apps, "widgets", "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "widgets", ControlEnvironment: "staging", Cluster: "fleet", FleetEnvironment: "staging/us-central", Region: "us-central", NomadRegion: "global", Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	const managedJob = "norn-job-pinned-deployment"
	currentJob := managedJob
	nomadWrites := 0
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			nomadWrites++
			http.Error(w, "unexpected write", http.StatusConflict)
			return
		}
		if strings.Contains(r.URL.Path, managedJob) && strings.HasSuffix(r.URL.Path, "/scale") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"JobID": managedJob, "TaskGroups": map[string]interface{}{"web": map[string]int{"Desired": 2, "Placed": 2, "Running": 2, "Healthy": 2}}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(nomadServer.Close)
	cfg := &config.Config{Environment: "staging", AppsDir: apps, NomadAddr: nomadServer.URL}
	runtime, err := newEtcdScaleRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.resolveTarget = func(_ context.Context, app, environment, region string) (string, string, error) {
		if app != "widgets" || environment != "staging" || region != "us-central" {
			return "", "", fmt.Errorf("wrong target")
		}
		return currentJob, "deployment-pinned", nil
	}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/scale", etcdScaleApp(cfg, operations, identities, runtime))
	router.Get("/api/v1/apps/{id}/scale-status", etcdScaleStatus(cfg, operations, runtime))
	router.Get("/api/v1/operations/{id}", etcdFleetOperation(operations, false))
	principal := &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, TokenID: rootID, Scopes: []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}}
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := handler.WithAccessPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)), principal)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	const path = "/api/v1/apps/widgets/scale"
	for _, count := range []int{0, 1, 4} {
		body := fmt.Sprintf(`{"group":"web","region":"us-central","count":%d}`, count)
		if got := request(http.MethodPost, path, body, fmt.Sprintf("outside-%d", count)); got.Code != http.StatusBadRequest || nomadWrites != 0 {
			t.Fatalf("out-of-bounds %d admitted: %d %s", count, got.Code, got.Body.String())
		}
	}
	first := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":3}`, "same-intent")
	if first.Code != http.StatusAccepted {
		t.Fatalf("scale admission=%d %s", first.Code, first.Body.String())
	}
	var accepted model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil || accepted.ID == "" || accepted.Payload["nomadJobId"] != managedJob {
		t.Fatalf("accepted target=%+v err=%v", accepted, err)
	}
	if got := request(http.MethodGet, "/api/v1/operations/"+accepted.ID, "", ""); got.Code != http.StatusOK {
		t.Fatalf("operation status=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v1/apps/widgets/scale-status", "", ""); got.Code != http.StatusOK {
		t.Fatalf("scale status=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":3}`, "same-intent"); got.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":2}`, "same-intent"); got.Code != http.StatusConflict {
		t.Fatalf("conflicting replay=%d %s", got.Code, got.Body.String())
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "scale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || claim.OperationID() != accepted.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, claim, nil); err == nil {
		t.Fatal("accepted mutation without app-lock fence")
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, "widgets")
	if err != nil || !acquired {
		t.Fatalf("app lock=%t err=%v", acquired, err)
	}
	defer lock.Release()
	currentJob = "norn-job-other-deployment"
	if _, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, claim, lock); err == nil || nomadWrites != 0 {
		t.Fatalf("stale route reached Nomad: err=%v writes=%d", err, nomadWrites)
	}
}

func TestEtcdScaleCrashAfterNomadAcceptRecoversWithoutSecondLaunch(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/scale-crash/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-scale-crash-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	const app, region, jobID, deploymentID = "widgets", "us-central", "norn-job-managed", "deployment-managed"
	nomadWrites := 0
	ready := false
	var scaleMeta map[string]interface{}
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID+"/deployments":
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/job/"+jobID+"/scale":
			nomadWrites++
			var request struct {
				Meta map[string]interface{} `json:"Meta"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Nomad scale: %v", err)
			}
			scaleMeta = request.Meta
			_ = json.NewEncoder(w).Encode(map[string]string{"EvalID": "eval-managed"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID+"/scale":
			if !ready {
				http.Error(w, "temporary Nomad read outage", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"JobID": jobID, "TaskGroups": map[string]interface{}{"web": map[string]interface{}{"Desired": 3, "Placed": 3, "Running": 3, "Healthy": 3, "Events": []interface{}{map[string]interface{}{"Count": 3, "EvalID": "eval-managed", "Meta": scaleMeta}}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/evaluation/eval-managed":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ID": "eval-managed", "JobID": jobID, "Status": "complete"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(nomadServer.Close)
	cfg := &config.Config{Environment: "staging", AppsDir: t.TempDir(), NomadAddr: nomadServer.URL}
	runtime, err := newEtcdScaleRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.resolveTarget = func(_ context.Context, gotApp, environment, gotRegion string) (string, string, error) {
		if gotApp != app || environment != "staging" || gotRegion != region {
			return "", "", fmt.Errorf("wrong target")
		}
		return jobID, deploymentID, nil
	}
	id := uuid.NewString()
	op := model.Operation{ID: id, Kind: "app.scale", App: app, Ref: region + "/web", Status: model.OperationQueued, MaxAttempts: 1,
		Payload: map[string]interface{}{"app": app, "group": "web", "region": region, "nomadRegion": "global", "count": 3, "nomadJobId": jobID, "deploymentId": deploymentID}}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: op.Kind, Resource: "app/widgets", Key: id}, Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"app": app, "count": 3}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, first, err := operations.ClaimNextOperation(ctx, "first-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("first app lock=%t err=%v", acquired, err)
	}
	result, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, first, lock)
	if err == nil || result != nil || nomadWrites != 1 {
		t.Fatalf("post-launch uncertainty result=%+v err=%v writes=%d", result, err, nomadWrites)
	}
	lock.Release() // process dies before it can defer or commit desired intent
	owner, err := client.Get(ctx, prefix+"/v3/owners/"+id)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := operations.GetOperation(ctx, id)
	if err != nil || queued.Status != model.OperationQueued || queued.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("recovered operation=%+v err=%v", queued, err)
	}
	ready = true
	claimed, second, err := operations.ClaimNextOperation(ctx, "second-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || second.Generation() <= first.Generation() {
		t.Fatalf("successor claim=%+v %+v err=%v", claimed, second, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("successor app lock=%t err=%v", acquired, err)
	}
	defer lock.Release()
	result, err = runtime.ExecuteOperationWithAppLock(ctx, claimed, second, lock)
	if err != nil || result == nil || !result.Finished() || !result.AppLockFenced() || nomadWrites != 1 {
		t.Fatalf("recovery result=%+v err=%v writes=%d", result, err, nomadWrites)
	}
	final, err := operations.GetOperation(ctx, id)
	counts, countErr := operations.DesiredReplicaCounts(ctx, app, region)
	if err != nil || countErr != nil || final.Status != model.OperationSucceeded || counts["web"] != 3 {
		t.Fatalf("recovered receipt=%+v counts=%v err=%v countErr=%v", final, counts, err, countErr)
	}
}
