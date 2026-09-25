package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"norn/v2/api/store"
)

func TestV3VerifyClaimedPrivateInvocationRequiresSignedIndexEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	keys, err := store.NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := privateInvocationAcceptance(t, adapter.authority, "invoke-verified")
	request.Identity.Resource = request.Operation.App
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.AcceptPrivateInvocation(ctx, request, store.PrivateInvocationInput{Body: "private-canary"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := adapter.ClaimNextOperation(ctx, "function-worker", time.Minute, []string{store.PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	verified, err := adapter.VerifyClaimedPrivateInvocation(ctx, *claimed)
	if err != nil || verified.Authority != request.Identity.Authority || verified.Operation.ID != accepted.Operation.ID {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	forged := *claimed
	forged.Payload = map[string]interface{}{"process": "other"}
	if _, err := adapter.VerifyClaimedPrivateInvocation(ctx, forged); err == nil {
		t.Fatal("forged claimed payload passed signature verifier")
	}
	record, _, err := adapter.load(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Operation.Payload["process"] = "other"
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.opKey(accepted.Operation.ID), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.VerifyClaimedPrivateInvocation(ctx, forged); err == nil {
		t.Fatal("tampered stored payload passed signature verifier")
	}
}
