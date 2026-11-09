package main

import (
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

func TestEtcdFleetWorkloadReleasePolicyOperatorRoute(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "/norn-test/workload-release-policy/" + uuid.NewString()
	defer client.Delete(context.Background(), prefix, clientv3.WithPrefix())
	signer, err := store.NewHMACAcceptanceSigner("workload-release-policy-test-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "pilot", ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global",
		Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Environment: "staging", EnvironmentExplicit: true}
	router := chi.NewRouter()
	router.Get("/api/v1/apps/{id}/workload-release-policy", etcdFleetWorkloadReleasePolicyRead(cfg, operations))
	router.Put("/api/v1/apps/{id}/workload-release-policy", etcdFleetWorkloadReleasePolicyConfigure(cfg, operations))
	url := "/api/v1/apps/pilot/workload-release-policy"
	operator := &handler.AccessPrincipal{TokenID: "operator-token", Subject: "platform-operator", Scopes: []string{handler.ScopePlatformOperate}}
	request := func(method, body string, principal *handler.AccessPrincipal) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if principal != nil {
			req = handler.WithAccessPrincipal(req, principal)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if got := request(http.MethodGet, "", nil); got.Code != http.StatusNotFound {
		t.Fatalf("missing policy read status=%d", got.Code)
	}
	body := `{"expectedRevision":0,"mode":"stateless-overlap","minReplicas":2,"maxReplicas":5,"surgeLimit":2,"routeSteps":[{"oldWeight":100,"targetWeight":0},{"oldWeight":90,"targetWeight":10},{"oldWeight":0,"targetWeight":100}]}`
	if got := request(http.MethodPut, body, nil); got.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated policy configure status=%d", got.Code)
	}
	ci := &handler.AccessPrincipal{TokenID: "ci-token", Subject: "ci", Scopes: []string{handler.ScopePlatformOperate}, CI: &handler.CIIdentity{}}
	if got := request(http.MethodPut, body, ci); got.Code != http.StatusForbidden {
		t.Fatalf("CI policy configure status=%d", got.Code)
	}
	created := request(http.MethodPut, body, operator)
	if created.Code != http.StatusCreated {
		t.Fatalf("policy configure status=%d body=%s", created.Code, created.Body.String())
	}
	var receipt struct {
		Policy   store.WorkloadReleasePolicy `json:"policy"`
		Revision int64                       `json:"revision"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil || receipt.Revision <= 0 || receipt.Policy.ApprovalID == "" ||
		receipt.Policy.ApprovedBy != operator.Subject || receipt.Policy.ApprovedAt.IsZero() || receipt.Policy.FleetTargetGeneration != target.Generation {
		t.Fatalf("policy receipt=%+v err=%v", receipt, err)
	}
	read := request(http.MethodGet, "", nil)
	var readback struct {
		Policy   store.WorkloadReleasePolicy `json:"policy"`
		Revision int64                       `json:"revision"`
	}
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &readback) != nil || readback.Revision != receipt.Revision ||
		readback.Policy.ApprovalID != receipt.Policy.ApprovalID {
		t.Fatalf("policy read status=%d readback=%+v body=%s", read.Code, readback, read.Body.String())
	}
	currentTarget, targetRevision, err := operations.CurrentFleetAppTarget(context.Background(), "pilot", "staging")
	if err != nil {
		t.Fatal(err)
	}
	currentTarget.Generation++
	currentTarget.Region = "east"
	if _, err := operations.ConfigureFleetAppTarget(context.Background(), currentTarget, targetRevision); err != nil {
		t.Fatal(err)
	}
	if stale := request(http.MethodGet, "", nil); stale.Code != http.StatusConflict {
		t.Fatalf("stale policy after placement change status=%d body=%s", stale.Code, stale.Body.String())
	}
	bad := `{"expectedRevision":0,"mode":"stateless-overlap","minReplicas":2,"maxReplicas":5,"surgeLimit":2,"routeSteps":[{"oldWeight":100,"targetWeight":0},{"oldWeight":0,"targetWeight":100},{"oldWeight":0,"targetWeight":100}]}`
	if got := request(http.MethodPut, bad, operator); got.Code != http.StatusConflict {
		t.Fatalf("non-overlap policy status=%d body=%s", got.Code, got.Body.String())
	}
}
