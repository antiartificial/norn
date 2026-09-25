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

type lostResponseFleetGitHub struct {
	approved githubapp.Dispatch
	nonces   []string
}

func (f *lostResponseFleetGitHub) ResolveApprovedPlan(context.Context, string, string) (*githubapp.Dispatch, error) {
	return &f.approved, nil
}
func (f *lostResponseFleetGitHub) DispatchBoundPlan(_ context.Context, _ string, _ string, _ bool, approved *githubapp.Dispatch, nonce string) (*githubapp.Dispatch, error) {
	if approved == nil || *approved != f.approved {
		return nil, errors.New("unapproved dispatch")
	}
	f.nonces = append(f.nonces, nonce)
	result := githubapp.Dispatch{RunID: 93, URL: "https://github.com/acme/norn-fleet/actions/runs/93", PlanRunID: f.approved.PlanRunID, PlanSHA: f.approved.PlanSHA, ApprovedHeadSHA: f.approved.ApprovedHeadSHA}
	if len(f.nonces) == 1 {
		return nil, errors.New("committed GitHub dispatch response lost")
	}
	return &result, nil
}

func TestEtcdFleetGitHubDispatchLostResponseReusesPrivateNonceEtcd(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoint == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "/norn-etcd-fleet-github-dispatch/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	cfg := &config.Config{AuditSigningKey: "norn-etcd-fleet-github-dispatch-signing-key", FleetGitHubRepository: "acme/norn-fleet", FleetGitHubConfigPath: "environments/staging/nyc3/cluster.yaml"}
	signer, err := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	plan := &fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: uuid.NewString(), Pool: "app", Cluster: "staging-nyc3", Action: "scale", SourceDigest: "sha256:" + strings.Repeat("d", 64)}
	if err := refreshEtcdCapacityPlanSignature(plan, cfg.AuditSigningKey); err != nil {
		t.Fatal(err)
	}
	payloadBytes, _ := json.Marshal(plan)
	payload := map[string]interface{}{}
	_ = json.Unmarshal(payloadBytes, &payload)
	now := time.Now().UTC()
	finished := now
	op := model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: plan.Pool, Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}
	authority, err := operations.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: op.Kind, Resource: op.Ref, Key: "plan"}, Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(context.Background(), acceptance); err != nil {
		t.Fatal(err)
	}
	fake := &lostResponseFleetGitHub{approved: githubapp.Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("a", 64), ApprovedHeadSHA: strings.Repeat("b", 40)}}
	route := etcdFleetGitHubDispatch(cfg, operations, fake)
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/github/dispatch", bytes.NewBufferString(`{"allowDestructive":true}`))
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("planID", plan.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
		req = handler.WithAccessPrincipal(req, &handler.AccessPrincipal{TokenID: "operator", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}})
		recorder := httptest.NewRecorder()
		route.ServeHTTP(recorder, req)
		return recorder
	}
	first := serve()
	if first.Code != http.StatusBadGateway || len(fake.nonces) != 1 || strings.Contains(first.Body.String(), fake.nonces[0]) {
		t.Fatalf("lost response status=%d body=%s nonces=%v", first.Code, first.Body.String(), fake.nonces)
	}
	second := serve()
	if second.Code != http.StatusCreated || len(fake.nonces) != 2 || fake.nonces[0] != fake.nonces[1] || strings.Contains(second.Body.String(), fake.nonces[0]) {
		t.Fatalf("recovery status=%d body=%s nonces=%v", second.Code, second.Body.String(), fake.nonces)
	}
	bound, err := operations.GetFleetRunnerDispatchBinding(context.Background(), plan.ID)
	if err != nil || bound.RunID != 93 || bound.PlanSHA256 != fake.approved.PlanSHA || bound.ApprovedHeadSHA != fake.approved.ApprovedHeadSHA {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	third := serve()
	if third.Code != http.StatusOK || len(fake.nonces) != 2 || strings.Contains(third.Body.String(), fake.nonces[0]) {
		t.Fatalf("replay status=%d body=%s nonces=%v", third.Code, third.Body.String(), fake.nonces)
	}
}
