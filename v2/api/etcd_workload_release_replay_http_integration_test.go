package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// responseDropTransport waits for the server to finish and then discards its
// response. This models a client losing an HTTP response after the server has
// durably handled the request; it never retries the request itself.
type responseDropTransport struct{ base http.RoundTripper }

func (t responseDropTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return nil, errors.New("simulated response loss after server completion")
}

func TestEtcdWorkloadReleaseAdmissionHTTPReplaysDurableAcceptanceAfterRestart(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var client *clientv3.Client
	newClient := func() *clientv3.Client {
		t.Helper()
		current, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	client = newClient()
	prefix := "/norn-tests/workload-release-http-replay/" + uuid.NewString()
	t.Cleanup(func() {
		if client != nil {
			_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
			_ = client.Close()
		}
	})
	firstClient := client
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-http-replay-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(client, prefix)
	now := time.Now().UTC().Truncate(time.Second)
	operator, err := handler.NewManagedAccessTokenRecord("workload-release-operator", []string{handler.ScopeReleaseStage}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := identities.RecordAccessToken(ctx, operator); err != nil {
		t.Fatal(err)
	}
	const app, qualificationID, idempotencyKey = "demo", "qualification-1", "same-release-request"
	requestDigest, err := pipeline.WorkloadReleaseRequestDigest(qualificationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Ref: qualificationID,
		Status: model.OperationQueued, Source: "release-control-api", Message: "queued workload release",
		StartedAt: now, MaxAttempts: 3, Metadata: map[string]interface{}{"workloadReleaseRequestDigest": requestDigest},
		Payload: map[string]interface{}{"workloadReleasePlan": "durably-accepted-before-response-loss"}}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority,
			Actor: store.OperationActor{Issuer: "norn://managed-token", Subject: operator.JTI},
			Kind:  operation.Kind, Resource: "app/" + app, Key: idempotencyKey},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit: store.AcceptanceAuditContext{CredentialID: operator.JTI, Source: "http-replay-integration",
			Scopes: []string{handler.ScopeReleaseStage}},
		Semantics: map[string]interface{}{"workloadReleaseRequestDigest": requestDigest},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the durable acceptance through the real etcd adapter so this test
	// isolates the HTTP replay path from the mutable-input setup required by a
	// fresh admission.
	committed, err := operations.Accept(ctx, acceptance)
	if err != nil || committed.Operation.ID != operation.ID || committed.Intent.Signature.Value == "" {
		t.Fatalf("durable acceptance=%+v err=%v", committed, err)
	}

	const apiSecret = "norn-workload-release-http-replay-api-secret-000"
	cfg := &config.Config{Environment: "staging", APIToken: apiSecret}
	keys, err := store.NewWorkloadReleaseInputKeyRing("input-v1", map[string][]byte{"input-v1": []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	newAPI := func(operations *etcdstore.V3OperationStore, identities *etcdstore.AuthStore) *httptest.Server {
		router := chi.NewRouter()
		router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeReleaseStage)).Post(
			"/api/v1/apps/{id}/workload-releases",
			etcdWorkloadReleaseAdmission(cfg, operations, identities, keys, &workloadReleaseAdmissionLineageFake{}, nil))
		return httptest.NewServer(router)
	}
	api := newAPI(operations, identities)
	t.Cleanup(api.Close)
	token, err := handler.SignManagedAccessToken(apiSecret, operator)
	if err != nil {
		t.Fatal(err)
	}
	apiURL := api.URL
	newRequest := func(targetReplicas int) *http.Request {
		t.Helper()
		body, err := json.Marshal(map[string]interface{}{"qualificationId": qualificationID, "targetReplicas": targetReplicas})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, apiURL+"/api/v1/apps/"+app+"/workload-releases", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Idempotency-Key", idempotencyKey)
		return request
	}

	baseTransport := api.Client().Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	dropClient := &http.Client{Transport: responseDropTransport{base: baseTransport}}
	if _, err := dropClient.Do(newRequest(2)); err == nil || !strings.Contains(err.Error(), "simulated response loss") {
		t.Fatalf("initial retry response error=%v, want deterministic response loss", err)
	}
	stored, err := operations.GetOperation(ctx, operation.ID)
	if err != nil || stored.Status != model.OperationQueued {
		t.Fatalf("operation after dropped response=%+v err=%v", stored, err)
	}
	api.Close()
	if err := firstClient.Close(); err != nil {
		t.Fatalf("close first etcd client: %v", err)
	}
	// Rebuild both the HTTP handler and etcd adapters. The retry must recover
	// the acceptance from durable etcd state without process-local replay state.
	client = newClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	identities = etcdstore.NewAuthStore(client, prefix)
	api = newAPI(operations, identities)
	t.Cleanup(api.Close)
	apiURL = api.URL
	stored, err = operations.GetOperation(ctx, operation.ID)
	if err != nil || stored.Status != model.OperationQueued {
		t.Fatalf("operation after handler and store restart=%+v err=%v", stored, err)
	}

	response, err := api.Client().Do(newRequest(2))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var replay struct {
		OperationID      string `json:"operationId"`
		Replayed         bool   `json:"replayed"`
		AcceptanceDigest string `json:"acceptanceDigest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || replay.OperationID != operation.ID || !replay.Replayed || replay.AcceptanceDigest != committed.Intent.CanonicalDigest {
		t.Fatalf("same-request HTTP replay status=%d receipt=%+v want operation=%s digest=%s", response.StatusCode, replay, operation.ID, committed.Intent.CanonicalDigest)
	}

	conflict, err := api.Client().Do(newRequest(3))
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("changed-request replay status=%d, want %d", conflict.StatusCode, http.StatusConflict)
	}
}

// This test composes the public HTTP admission route with real etcd placement,
// policy, identity, and serving-route reads. Qualification lookup is a narrow
// fixture because this scenario stops at the absent-route gate; no route
// record is seeded and no acceptance can be committed.
func TestEtcdWorkloadReleaseAdmissionHTTPRequiresOwnedServingRoute(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-tests/workload-release-http-route-gate/" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
		_ = client.Close()
	})
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-http-route-gate-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	const app, qualificationID, idempotencyKey = "demo", "qualification-route-gate", "missing-owned-route"
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: app, ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/local", Region: "local", NomadRegion: "global",
		NomadNamespace: "default", Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	policy := store.WorkloadReleasePolicy{SchemaVersion: store.WorkloadReleasePolicySchema, App: app, ControlEnvironment: "staging",
		FleetTargetGeneration: target.Generation, ApprovalID: "11111111-1111-4111-8111-111111111111", ApprovedBy: "integration-owner",
		ApprovedAt: now, Mode: store.WorkloadReleasePolicyStatelessOverlap, MinReplicas: 1, MaxReplicas: 4, SurgeLimit: 1,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
	if _, err := operations.ConfigureWorkloadReleasePolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(client, prefix)
	operator, err := handler.NewManagedAccessTokenRecord("route-gate-operator", []string{handler.ScopeReleaseStage}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := identities.RecordAccessToken(ctx, operator); err != nil {
		t.Fatal(err)
	}
	const apiSecret = "norn-workload-release-route-gate-api-secret-000"
	cfg := &config.Config{Environment: "staging", APIToken: apiSecret}
	keys, err := store.NewWorkloadReleaseInputKeyRing("input-v1", map[string][]byte{"input-v1": []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	apiStore := &workloadReleaseAdmissionRouteGateStore{V3OperationStore: operations,
		resolveQualification: func(_ context.Context, requestedApp, requestedID string) (model.ReleaseQualification, error) {
			if requestedApp != app || requestedID != qualificationID {
				return model.ReleaseQualification{}, errors.New("unexpected qualification selection")
			}
			return model.ReleaseQualification{ID: qualificationID, App: app, Environment: "staging"}, nil
		}}
	router := chi.NewRouter()
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeReleaseStage)).Post(
		"/api/v1/apps/{id}/workload-releases",
		etcdWorkloadReleaseAdmission(cfg, apiStore, identities, keys, &workloadReleaseAdmissionLineageFake{}, nil))
	api := httptest.NewServer(router)
	t.Cleanup(api.Close)
	token, err := handler.SignManagedAccessToken(apiSecret, operator)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(map[string]interface{}{"qualificationId": qualificationID, "targetReplicas": 2})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, api.URL+"/api/v1/apps/"+app+"/workload-releases", strings.NewReader(string(requestBody)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := api.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(responseBody), "workload_release_route_unavailable") {
		t.Fatalf("missing owned route response status=%d body=%s", response.StatusCode, responseBody)
	}
	digest, err := pipeline.WorkloadReleaseRequestDigest(qualificationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.OperationRequestIdentity{Authority: authority,
		Actor: store.OperationActor{Issuer: "norn://managed-token", Subject: operator.JTI},
		Kind:  "app.workload-release", Resource: "app/" + app, Key: idempotencyKey}
	probe := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		Metadata: map[string]interface{}{"workloadReleaseRequestDigest": digest}}
	if _, err := operations.ResolvePrivateWorkloadReleaseReplay(ctx, identity, probe); !errors.Is(err, store.ErrAcceptanceNotFound) {
		t.Fatalf("route-gated request left durable acceptance: %v", err)
	}
}

type workloadReleaseAdmissionRouteGateStore struct {
	*etcdstore.V3OperationStore
	resolveQualification func(context.Context, string, string) (model.ReleaseQualification, error)
}

func (s *workloadReleaseAdmissionRouteGateStore) ResolveWorkloadReleaseQualification(ctx context.Context, app, id string, _ []string, _ time.Time) (model.ReleaseQualification, error) {
	return s.resolveQualification(ctx, app, id)
}
