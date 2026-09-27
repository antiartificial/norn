package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/cloudflared"
	"norn/v2/api/model"
)

func TestCloudflaredSignedClaimedEffectReplaysWithoutAnotherRestart(t *testing.T) {
	p, db, request := acceptancePipelineFixture(t)
	fake := &fakeCloudflaredDriver{config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	effects, err := newCloudflaredEffects(db, fake)
	if err != nil {
		t.Fatal(err)
	}
	p.CloudflaredEffects = effects
	reservation := cloudflaredTestReservation(t, fake)
	var payload map[string]interface{}
	if err := json.Unmarshal(reservation.LaunchPayload, &payload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: cloudflaredMutationKind, App: "demo", SagaID: uuid.NewString(), Ref: "enable", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: payload}
	request.Key = "cloudflared-enable"
	request.Semantics = map[string]interface{}{"action": "enable", "app": "demo", "hostnames": []string{"https://demo.example.com"}, "service": "http://127.0.0.1:8080"}
	ctx := context.Background()
	accepted, err := p.QueueOperation(ctx, op, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := p.QueueOperation(ctx, op, request)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID {
		t.Fatalf("acceptance replay=%+v err=%v", replayed, err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "cloudflared-worker", time.Minute, []string{cloudflaredMutationKind})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	first, err := p.ExecuteOperation(ctx, claimed, claim)
	if err != nil || first == nil || first.Status != model.OperationSucceeded {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := p.ExecuteOperation(ctx, claimed, claim)
	if err != nil || second == nil || second.Status != model.OperationSucceeded {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	if fake.applyCalls != 1 || fake.restartCalls != 1 {
		t.Fatalf("writes=%d restarts=%d", fake.applyCalls, fake.restartCalls)
	}
	if second.Metadata["effectReused"] != true {
		t.Fatalf("effect was not reused: %+v", second.Metadata)
	}
}
