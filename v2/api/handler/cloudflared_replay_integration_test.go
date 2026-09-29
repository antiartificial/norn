package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestCloudflaredReplayDoesNotResolveUnavailableLiveService(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	p := &pipeline.Pipeline{DB: db}
	effects, err := pipeline.NewCloudflaredEffects(db)
	if err != nil {
		t.Fatal(err)
	}
	p.CloudflaredEffects = effects
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: "cloudflared-replay-test-signing-key-0001"}, p, nil, nil, nil, nil, nil)
	p.SetOperationStore(h.OperationStore())
	ctx := context.Background()
	receiptID := uuid.NewString()
	if err := db.ReserveMutationAudit(ctx, &store.MutationAuditEvent{ID: receiptID, RequestID: "cloudflared-replay", PrincipalSubject: "operator", Method: http.MethodPost, Path: "/api/v1/apps/demo/forge", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/forge", nil)
	req.Header.Set("Idempotency-Key", "forge-key")
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{ReceiptID: receiptID, RequestID: "cloudflared-replay", Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator", CredentialID: "token-1", DeviceID: "device-1", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}}})
	hostnames := []string{"https://demo.example.com"}
	enqueue, ok := h.pipelineEnqueueRequest(httptest.NewRecorder(), req, "forge-key", map[string]interface{}{"app": "demo", "action": "forge", "hostnames": hostnames, "service": "http://127.0.0.1:8080"})
	if !ok {
		t.Fatal("acceptance identity unavailable")
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cloudflared-mutate", App: "demo", SagaID: uuid.NewString(), Ref: "forge", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: map[string]interface{}{"action": "forge", "app": "demo", "hostnames": hostnames, "service": "http://127.0.0.1:8080", "host": "mini-a", "configPath": "/private/config.yml", "beforeDigest": "before", "afterDigest": "after"}}
	accepted, err := p.QueueOperation(ctx, op, enqueue)
	if err != nil {
		t.Fatal(err)
	}
	serviceCalls := 0
	response := httptest.NewRecorder()
	h.queueCloudflaredMutation(response, req, &model.InfraSpec{App: "demo"}, "forge", hostnames, func() (string, error) { serviceCalls++; return "", errors.New("Nomad unavailable") })
	if response.Code != http.StatusOK || serviceCalls != 0 || response.Header().Get("Location") != "/api/v1/operations/"+accepted.Operation.ID {
		t.Fatalf("replay status=%d serviceCalls=%d body=%s", response.Code, serviceCalls, response.Body.String())
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "demo")
	routeRequest := req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	missingSpecReplay := httptest.NewRecorder()
	h.Forge(missingSpecReplay, routeRequest)
	if missingSpecReplay.Code != http.StatusOK || missingSpecReplay.Header().Get("Location") != "/api/v1/operations/"+accepted.Operation.ID {
		t.Fatalf("missing-spec replay status=%d body=%s", missingSpecReplay.Code, missingSpecReplay.Body.String())
	}
	wrongAction := httptest.NewRecorder()
	h.Teardown(wrongAction, routeRequest)
	if wrongAction.Code != http.StatusConflict {
		t.Fatalf("same-key wrong-action status=%d body=%s", wrongAction.Code, wrongAction.Body.String())
	}
	toggleReceiptID := uuid.NewString()
	if err := db.ReserveMutationAudit(ctx, &store.MutationAuditEvent{ID: toggleReceiptID, RequestID: "cloudflared-toggle-replay", PrincipalSubject: "operator", Method: http.MethodPost, Path: "/api/v1/apps/toggle-demo/endpoints/toggle", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	toggleRequest := httptest.NewRequest(http.MethodPost, "/api/v1/apps/toggle-demo/endpoints/toggle", strings.NewReader(`{"hostname":"toggle.example.com","enabled":true}`))
	toggleRequest.Header.Set("Idempotency-Key", "toggle-key")
	toggleRequest = withOperationAcceptanceRequestContext(toggleRequest, operationAcceptanceRequestContext{ReceiptID: toggleReceiptID, RequestID: "cloudflared-toggle-replay", Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator", CredentialID: "token-1", DeviceID: "device-1", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}}})
	toggleRoute := chi.NewRouteContext()
	toggleRoute.URLParams.Add("id", "toggle-demo")
	toggleRequest = toggleRequest.WithContext(context.WithValue(toggleRequest.Context(), chi.RouteCtxKey, toggleRoute))
	toggleHostnames := []string{"https://toggle.example.com"}
	toggleEnqueue, ok := h.pipelineEnqueueRequest(httptest.NewRecorder(), toggleRequest, "toggle-key", map[string]interface{}{"app": "toggle-demo", "action": "enable", "hostnames": toggleHostnames, "service": "http://127.0.0.1:8080"})
	if !ok {
		t.Fatal("toggle acceptance identity unavailable")
	}
	toggleOp := model.Operation{ID: uuid.NewString(), Kind: "app.cloudflared-mutate", App: "toggle-demo", SagaID: uuid.NewString(), Ref: "enable", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: map[string]interface{}{"action": "enable", "app": "toggle-demo", "hostnames": toggleHostnames, "service": "http://127.0.0.1:8080", "host": "mini-a", "configPath": "/private/config.yml", "beforeDigest": "before", "afterDigest": "after"}}
	toggleAccepted, err := p.QueueOperation(ctx, toggleOp, toggleEnqueue)
	if err != nil {
		t.Fatal(err)
	}
	toggleReplay := httptest.NewRecorder()
	h.ToggleEndpoint(toggleReplay, toggleRequest)
	if toggleReplay.Code != http.StatusOK || toggleReplay.Header().Get("Location") != "/api/v1/operations/"+toggleAccepted.Operation.ID {
		t.Fatalf("missing-spec toggle replay status=%d body=%s", toggleReplay.Code, toggleReplay.Body.String())
	}
	wrongHostname := toggleRequest.Clone(toggleRequest.Context())
	wrongHostname.Body = io.NopCloser(strings.NewReader(`{"hostname":"other.example.com","enabled":true}`))
	wrongHostnameReplay := httptest.NewRecorder()
	h.ToggleEndpoint(wrongHostnameReplay, wrongHostname)
	if wrongHostnameReplay.Code != http.StatusConflict {
		t.Fatalf("same-key wrong-hostname status=%d body=%s", wrongHostnameReplay.Code, wrongHostnameReplay.Body.String())
	}
}
