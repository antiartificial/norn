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

func TestEtcdWorkloadReleaseAdmissionHTTPReplaysDurableAcceptance(t *testing.T) {
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
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/workload-release-http-replay/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
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
	router := chi.NewRouter()
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeReleaseStage)).Post(
		"/api/v1/apps/{id}/workload-releases",
		etcdWorkloadReleaseAdmission(cfg, operations, identities, keys, &workloadReleaseAdmissionLineageFake{}, nil))
	api := httptest.NewServer(router)
	t.Cleanup(api.Close)
	token, err := handler.SignManagedAccessToken(apiSecret, operator)
	if err != nil {
		t.Fatal(err)
	}
	newRequest := func(targetReplicas int) *http.Request {
		t.Helper()
		body, err := json.Marshal(map[string]interface{}{"qualificationId": qualificationID, "targetReplicas": targetReplicas})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, api.URL+"/api/v1/apps/"+app+"/workload-releases", strings.NewReader(string(body)))
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
