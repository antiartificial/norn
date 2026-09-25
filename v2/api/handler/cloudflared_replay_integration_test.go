package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
}
