package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type lostResponsePullRequester struct {
	calls                int
	ambiguousAfterCreate bool
	result               githubapp.PullRequest
}

func (f *lostResponsePullRequester) ReconcilePullRequest(context.Context, string, string, string, string, fleet.NodePool, string) (*githubapp.Reconciliation, error) {
	if f.calls == 0 {
		return &githubapp.Reconciliation{Outcome: "verified-no-write"}, nil
	}
	if f.ambiguousAfterCreate {
		return &githubapp.Reconciliation{Outcome: "ambiguous"}, nil
	}
	return &githubapp.Reconciliation{Outcome: "remote-success", PullRequest: &f.result}, nil
}

func (f *lostResponsePullRequester) CreatePullRequest(context.Context, string, string, string, string, fleet.NodePool, string) (*githubapp.PullRequest, error) {
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("GitHub created PR but response was lost")
	}
	return &f.result, nil
}

func TestEtcdFleetGitHubPullRequestLostResponseReplaysIndefinitely(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoint == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-etcd-fleet-github-pr/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })
	cfg := &config.Config{AuditSigningKey: "norn-etcd-fleet-github-pr-signing-key", FleetGitHubRepository: "acme/norn-fleet", FleetGitHubConfigPath: "environments/staging/nyc3/cluster.yaml"}
	signer, _ := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey)
	operations, err := etcdstore.NewV3OperationStoreWithPolicy(client, prefix, uuid.NewString(), signer, store.AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	plan := &fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: uuid.NewString(), Pool: "app", Cluster: "staging-nyc3", Action: "scale", Current: fleet.NodePool{Desired: 2}, Proposed: fleet.NodePool{Desired: 3}, SourceDigest: "sha256:" + strings.Repeat("d", 64)}
	if err := refreshEtcdCapacityPlanSignature(plan, cfg.AuditSigningKey); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	payload := map[string]interface{}{}
	_ = json.Unmarshal(raw, &payload)
	now := time.Now().UTC()
	finished := now
	authority, _ := operations.Authority(context.Background())
	planOp := model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: plan.Pool, Status: model.OperationSucceeded, StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}
	accepted := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: planOp.Kind, Resource: planOp.Ref, Key: "plan"}, Operation: planOp, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	accepted.Fingerprint, _ = store.CanonicalOperationRequestFingerprint(accepted)
	if _, err := operations.Accept(context.Background(), accepted); err != nil {
		t.Fatal(err)
	}
	fake := &lostResponsePullRequester{result: githubapp.PullRequest{Number: 42, URL: "https://github.com/acme/norn-fleet/pull/42", Branch: "norn/plan-" + plan.ID, State: "open"}}
	route := etcdFleetGitHubPullRequest(cfg, operations, fake)
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/github/pull-request", bytes.NewReader(nil))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("planID", plan.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		req = handler.WithAccessPrincipal(req, &handler.AccessPrincipal{TokenID: "operator", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		return rec
	}
	if first := serve(); first.Code != http.StatusBadGateway || fake.calls != 1 {
		t.Fatalf("lost response status=%d body=%s calls=%d", first.Code, first.Body.String(), fake.calls)
	}
	fake.ambiguousAfterCreate = true
	if ambiguous := serve(); ambiguous.Code != http.StatusBadGateway || fake.calls != 1 {
		t.Fatalf("ambiguous reconciliation retried create: status=%d body=%s calls=%d", ambiguous.Code, ambiguous.Body.String(), fake.calls)
	}
	fake.ambiguousAfterCreate = false
	if second := serve(); second.Code != http.StatusOK || fake.calls != 1 || strings.Contains(second.Body.String(), "credential") {
		t.Fatalf("recovery status=%d body=%s calls=%d", second.Code, second.Body.String(), fake.calls)
	}
	if replay := serve(); replay.Code != http.StatusOK || fake.calls != 1 {
		t.Fatalf("replay status=%d calls=%d", replay.Code, fake.calls)
	}
	reservation, err := operations.GetFleetGitHubPullRequestReservation(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.VerifyFleetGitHubPullRequestReservation(context.Background(), reservation); err != nil {
		t.Fatalf("indefinite replay under default TTL: %v", err)
	}
}
