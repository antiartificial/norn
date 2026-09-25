package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestPrivateInvocationPostgresAtomicAcceptanceAndReplay(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	keys, err := NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := newAcceptance(t, stores[0], "invoke-once", "operator", "demo", false)
	request.Identity.Kind, request.Operation.Kind = privateInvocationKind, privateInvocationKind
	request.Operation.Payload = map[string]interface{}{"process": "resize", "imageTag": "image@sha256:abcdef"}
	private := PrivateInvocationInput{Body: "secret-body-canary", Method: "POST", Path: "/private-path-canary"}
	first, err := stores[0].AcceptPrivateInvocation(ctx, request, private, keys)
	if err != nil || first.Replayed {
		t.Fatalf("first accepted=%+v err=%v", first, err)
	}
	if first.Operation.Payload["privateRecordId"] != first.Operation.ID || first.Operation.Payload["privateKeyId"] != "invocation-1" {
		t.Fatalf("private descriptor = %+v", first.Operation.Payload)
	}
	var controlEvidence string
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT o.payload::text || convert_from(i.canonical_bytes,'UTF8') || convert_from(i.request_canonical_bytes,'UTF8') FROM operations o JOIN operation_acceptance_intents i ON i.operation_id=o.id WHERE o.id=$1`, first.Operation.ID).Scan(&controlEvidence); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(controlEvidence, "secret-body-canary") || strings.Contains(controlEvidence, "private-path-canary") {
		t.Fatal("plaintext request appeared in signed control evidence")
	}
	var envelope []byte
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT envelope FROM private_invocation_material WHERE operation_id=$1`, first.Operation.ID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(envelope, []byte("secret-body-canary")) || bytes.Contains(envelope, []byte("private-path-canary")) {
		t.Fatal("plaintext request appeared in private material row")
	}
	needed, err := stores[0].RequiredPrivateInvocationKeys(ctx)
	if err != nil || len(needed) != 1 || needed[0] != "invocation-1" {
		t.Fatalf("required keys=%v err=%v", needed, err)
	}
	opened, err := stores[1].OpenPrivateInvocation(ctx, first.Operation, keys)
	if err != nil || opened != private {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	replay := request
	replay.Operation.ID = uuid.NewString()
	replay.Operation.SagaID = uuid.NewString()
	second, err := stores[1].AcceptPrivateInvocation(ctx, replay, private, keys)
	if err != nil || !second.Replayed || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	if _, err := stores[1].AcceptPrivateInvocation(ctx, replay, PrivateInvocationInput{Body: "different"}, keys); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("changed request err=%v, want conflict", err)
	}
	replay.Operation.Payload = map[string]interface{}{"process": "different", "imageTag": "image@sha256:abcdef"}
	if _, err := stores[1].AcceptPrivateInvocation(ctx, replay, private, keys); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("changed public request err=%v, want conflict", err)
	}
	missing, _ := NewPrivateInvocationKeyRing("invocation-2", map[string][]byte{"invocation-2": bytes.Repeat([]byte{0x88}, 32)})
	if err := missing.RequirePrivateInvocationKeys(needed); err == nil {
		t.Fatal("restore preflight accepted missing historical key")
	}
	if _, err := stores[1].OpenPrivateInvocation(ctx, first.Operation, missing); err == nil {
		t.Fatal("opened private request after restoring without historical key")
	}
	if _, err := stores[1].AcceptPrivateInvocation(ctx, replay, private, missing); err == nil {
		t.Fatal("replayed private request without historical key")
	}
}

func TestPrivateInvocationPostgresRollbackLeavesNoMaterial(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	keys, _ := NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	request := newAcceptance(t, stores[0], "invoke-rollback", "operator", "demo", false)
	request.Identity.Kind, request.Operation.Kind = privateInvocationKind, privateInvocationKind
	request.Operation.Payload = map[string]interface{}{"process": "resize"}
	stores[0].test.beforeIntent = func() error { return errors.New("injected acceptance failure") }
	if _, err := stores[0].AcceptPrivateInvocation(ctx, request, PrivateInvocationInput{Body: "private"}, keys); err == nil {
		t.Fatal("accepted operation after injected failure")
	}
	var operations, material int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id=$1`, request.Operation.ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM private_invocation_material WHERE operation_id=$1`, request.Operation.ID).Scan(&material); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || material != 0 {
		t.Fatalf("rollback left operation=%d private material=%d", operations, material)
	}
}

func TestPrivateInvocationPostgresConcurrentSameKey(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	keys, _ := NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	base := newAcceptance(t, stores[0], "invoke-concurrent", "operator", "demo", false)
	base.Identity.Kind, base.Operation.Kind = privateInvocationKind, privateInvocationKind
	base.Operation.Payload = map[string]interface{}{"process": "resize"}
	material := PrivateInvocationInput{Body: "private concurrent body"}
	start := make(chan struct{})
	results := make([]AcceptedOperation, 2)
	errorsOut := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := base
			request.Operation.ID = uuid.NewString()
			request.Operation.SagaID = uuid.NewString()
			<-start
			results[index], errorsOut[index] = stores[index].AcceptPrivateInvocation(ctx, request, material, keys)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errorsOut {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if results[0].Operation.ID != results[1].Operation.ID || results[0].Replayed == results[1].Replayed {
		t.Fatalf("two requests = %+v %+v", results[0], results[1])
	}
	var count int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM private_invocation_material`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("private records=%d, want one", count)
	}
}
