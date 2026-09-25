package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestEtcdCanaryHTTPAdmissionReplaysAcrossTokenRotationAndRunsWorker(t *testing.T) {
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	etcd, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = etcd.Close() })
	prefix := "/norn-tests/canary-http/" + uuid.NewString()
	t.Cleanup(func() { _, _ = etcd.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-canary-http-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStoreWithPolicy(etcd, prefix, authority, signer, store.AcceptancePolicy{ReplayTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(etcd, prefix)
	apps := t.TempDir()
	if err := os.MkdirAll(filepath.Join(apps, "widgets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, "widgets", "infraspec.yaml"), []byte("name: widgets\ndeploy: true\nprocesses:\n  web:\n    command: sleep 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var latestCalls, posts atomic.Int32
	var promoted atomic.Bool
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widgets/deployments":
			latestCalls.Add(1)
			_ = json.NewEncoder(w).Encode([]*nomadapi.Deployment{{ID: "deployment-accepted", JobID: "widgets", Status: "running", CreateIndex: 10,
				TaskGroups: map[string]*nomadapi.DeploymentState{"web": {PlacedCanaries: []string{"alloc-1"}, Promoted: false}}}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/deployment/promote/deployment-accepted":
			posts.Add(1)
			promoted.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]string{"EvalID": "eval-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/deployment/deployment-accepted":
			if !promoted.Load() {
				http.Error(w, "not promoted", http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(&nomadapi.Deployment{ID: "deployment-accepted", JobID: "widgets", Status: "successful",
				TaskGroups: map[string]*nomadapi.DeploymentState{"web": {PlacedCanaries: []string{"alloc-1"}, Promoted: true}}})
		default:
			http.Error(w, "unexpected Nomad request", http.StatusNotFound)
		}
	}))
	t.Cleanup(nomadServer.Close)
	secret := "norn-etcd-canary-http-test-api-secret-000000"
	cfg := &config.Config{AppsDir: apps, NomadAddr: nomadServer.URL, APIToken: secret}
	runtime, err := newEtcdCanaryRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	rootID := uuid.NewString()
	issued := time.Now().UTC()
	root := &store.AccessToken{JTI: rootID, Subject: "operator", Scopes: []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}, IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)}
	if err := identities.RecordAccessToken(ctx, root); err != nil {
		t.Fatal(err)
	}
	rootJWT := signedEtcdCanaryTestToken(t, secret, rootID)
	router := chi.NewRouter()
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)).Post("/api/v1/apps/{id}/promote", etcdCanaryPromote(cfg, operations, identities, runtime))
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)).Get("/api/v1/operations/{id}", etcdFleetOperation(operations, true))
	api := httptest.NewServer(router)
	t.Cleanup(api.Close)
	request := func(token, region string) (int, model.Operation) {
		t.Helper()
		url := api.URL + "/api/v1/apps/widgets/promote"
		if region != "" {
			url += "?region=" + region
		}
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "same-intent")
		response, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var got model.Operation
		if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, got
	}
	status, first := request(rootJWT, "")
	if status != http.StatusAccepted || first.ID == "" || latestCalls.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("first acceptance status=%d operation=%+v latest=%d posts=%d", status, first, latestCalls.Load(), posts.Load())
	}
	acceptedIdentity := store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/token-lineage", Subject: rootID}, Kind: "app.canary-promote", Resource: "widgets", Key: "same-intent"}
	signed, err := operations.ResolveIdentity(ctx, acceptedIdentity)
	if err != nil || signed.Operation.ID != first.ID || signed.Intent.Signature.Value == "" {
		t.Fatalf("signed etcd canary acceptance = %+v err=%v", signed, err)
	}
	// A replay uses the verified signed receipt even after declarative app
	// discovery changes; it never samples a new Nomad deployment.
	if err := os.Remove(filepath.Join(apps, "widgets", "infraspec.yaml")); err != nil {
		t.Fatal(err)
	}
	rotatedID := uuid.NewString()
	rotated := &store.AccessToken{JTI: rotatedID, Subject: "operator", Scopes: root.Scopes, IssuedAt: issued, ExpiresAt: issued.Add(time.Hour), RotatedFrom: rootID}
	if _, err := identities.RotateAccessToken(ctx, rootID, rotated); err != nil {
		t.Fatal(err)
	}
	rotatedJWT := signedEtcdCanaryTestToken(t, secret, rotatedID)
	status, duplicate := request(rotatedJWT, "")
	if status != http.StatusOK || duplicate.ID != first.ID || latestCalls.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("rotated duplicate status=%d operation=%+v latest=%d posts=%d", status, duplicate, latestCalls.Load(), posts.Load())
	}
	status, _ = request(rotatedJWT, "other-region")
	if status != http.StatusConflict || latestCalls.Load() != 1 {
		t.Fatalf("conflicting replay status=%d latest=%d", status, latestCalls.Load())
	}
	workerCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go runtime.worker.Run(workerCtx)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, err := operations.GetOperation(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Terminal() {
			if current.Status != model.OperationSucceeded || posts.Load() != 1 {
				t.Fatalf("worker result=%+v posts=%d", current, posts.Load())
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	current, err := operations.GetOperation(ctx, first.ID)
	if err != nil || current.Status != model.OperationSucceeded {
		t.Fatalf("worker did not terminalize accepted promotion: %+v err=%v", current, err)
	}
	status, duplicate = request(rotatedJWT, "")
	if status != http.StatusOK || duplicate.ID != first.ID || latestCalls.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("terminal duplicate status=%d operation=%+v latest=%d posts=%d", status, duplicate, latestCalls.Load(), posts.Load())
	}
}

func signedEtcdCanaryTestToken(t *testing.T, secret, id string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]interface{}{"sub": "operator", "iss": "norn", "aud": "norn-control", "use": "access", "managed": true,
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(), "jti": id, "scp": []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	key := hmac.New(sha256.New, []byte(secret))
	_, _ = key.Write([]byte("norn.jwt-signing/v1"))
	mac := hmac.New(sha256.New, key.Sum(nil))
	_, _ = mac.Write([]byte(unsigned))
	return fmt.Sprintf("%s.%s", unsigned, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func TestEtcdCanaryHTTPPreviewRequiresWorkerAndGatesCapabilities(t *testing.T) {
	getenv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	workerEnabled, httpEnabled, err := etcdCanaryPreviewFlags(getenv(nil))
	if err != nil || workerEnabled || httpEnabled {
		t.Fatalf("default preview flags = %t %t err=%v", workerEnabled, httpEnabled, err)
	}
	base := etcdFleetCapabilities(false)
	if _, advertised := base["endpoints"].(map[string]string)["appCanaryPromote"]; advertised {
		t.Fatal("disabled HTTP preview advertised canary promotion")
	}
	if _, _, err := etcdCanaryPreviewFlags(getenv(map[string]string{etcdCanaryHTTPPreviewEnv: "true"})); err == nil {
		t.Fatal("HTTP preview enabled without effect worker")
	}
	workerEnabled, httpEnabled, err = etcdCanaryPreviewFlags(getenv(map[string]string{etcdCanaryHTTPPreviewEnv: "true", etcdCanaryWorkerEnv: "true"}))
	if err != nil || !workerEnabled || !httpEnabled {
		t.Fatalf("qualified preview flags = %t %t err=%v", workerEnabled, httpEnabled, err)
	}
	preview := etcdFleetCapabilities(httpEnabled)
	if preview["endpoints"].(map[string]string)["appCanaryPromote"] != "/api/v1/apps/{id}/promote" {
		t.Fatal("enabled HTTP preview did not advertise its exact route")
	}
}
