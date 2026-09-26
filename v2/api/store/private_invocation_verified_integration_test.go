package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestVerifyClaimedPrivateInvocationRequiresSignedExactPayload(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	keys, err := NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := newAcceptance(t, stores[0], "invoke-verified", "operator", "demo", false)
	request.Identity.Kind, request.Operation.Kind = PrivateInvocationOperationKind, PrivateInvocationOperationKind
	request.Operation.Payload = map[string]interface{}{"process": "resize", "specDigest": "sha256:public"}
	accepted, err := stores[0].AcceptPrivateInvocation(ctx, request, PrivateInvocationInput{Body: "private-canary"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := dbs[0].ClaimNextOperation(ctx, "function-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	verified, err := stores[0].VerifyClaimedPrivateInvocation(ctx, *claimed)
	if err != nil || verified.Authority != request.Identity.Authority || verified.Operation.ID != accepted.Operation.ID {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	forged := *claimed
	forged.Payload = map[string]interface{}{"process": "other"}
	if _, err := stores[0].VerifyClaimedPrivateInvocation(ctx, forged); err == nil {
		t.Fatal("forged claimed payload passed signature verifier")
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET payload=jsonb_set(payload,'{process}','"other"'::jsonb) WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].VerifyClaimedPrivateInvocation(ctx, model.Operation{ID: accepted.Operation.ID, Kind: PrivateInvocationOperationKind, App: accepted.Operation.App, Payload: forged.Payload}); err == nil {
		t.Fatal("tampered stored payload passed signature verifier")
	}
}
