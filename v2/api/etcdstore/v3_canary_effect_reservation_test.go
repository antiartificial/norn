package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3CanaryEffectReservationAtomicallyFencesClaimAndAppEtcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/canary-effects/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-canary-effects-test-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewV3CanaryEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		id := uuid.NewString()
		op := model.Operation{ID: id, Kind: "app.canary-promote", App: "widgets", Status: model.OperationQueued, MaxAttempts: 1,
			Payload: map[string]interface{}{"app": "widgets", "region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-" + id}}
		a := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: op.Kind, Resource: "app/widgets", Key: id},
			Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"app": "widgets", "deploymentId": op.Payload["deploymentId"]}}
		a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := operations.Accept(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	claims := make([]store.OperationClaim, 2)
	ops := make([]*model.Operation, 2)
	for i := range claims {
		ops[i], claims[i], err = operations.ClaimNextOperation(ctx, "worker-"+uuid.NewString(), time.Minute, []string{"app.canary-promote"})
		if err != nil || ops[i] == nil {
			t.Fatalf("claim %d: %v, %+v", i, err, ops[i])
		}
	}
	reservation := func(i int) effect.Reservation {
		t.Helper()
		payload, err := json.Marshal(canaryEffectInput{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: effectPayloadString(ops[i].Payload, "deploymentId")})
		if err != nil {
			t.Fatal(err)
		}
		r := effect.Reservation{Authority: authority, Resource: "app/widgets/canary-promote/us-central", Stage: "app.canary-promote.nomad", Supervisor: "nomad-canary-promotion", SupervisorExecutionID: "nomad-test-" + ops[i].ID,
			OperationClaim: effect.OperationClaim{OperationID: claims[i].OperationID(), OwnerID: claims[i].OwnerID(), Generation: claims[i].Generation()}, LaunchPayload: payload}
		r.InputDigest, err = effect.ComputeInputDigest(r)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	results := make([]effect.ReservationResult, 2)
	errs := make([]error, 2)
	reservations := []effect.Reservation{reservation(0), reservation(1)}
	changed := reservations[0]
	changed.LaunchPayload = []byte(`{"app":"widgets","region":"us-central","nomadRegion":"global","deploymentId":"different-deployment"}`)
	changed.InputDigest, err = effect.ComputeInputDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effects.Reserve(ctx, changed); err == nil {
		t.Fatal("reserved a canary effect for a deployment outside signed operation payload")
	}
	var wg sync.WaitGroup
	for i := range claims {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = effects.Reserve(ctx, reservations[i]) }(i)
	}
	wg.Wait()
	winners, blocked := 0, 0
	for i := range results {
		if results[i].Created {
			winners++
		} else if errors.Is(errs[i], effect.ErrResourceBlocked) {
			blocked++
		} else {
			t.Fatalf("reservation %d: %+v err=%v", i, results[i], errs[i])
		}
	}
	if winners != 1 || blocked != 1 {
		t.Fatalf("concurrent reservations: winners=%d blocked=%d", winners, blocked)
	}
	for i := range claims {
		if results[i].Created {
			if err := operations.FinishClaimedOperation(ctx, claims[i], model.OperationSucceeded, "done", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := effects.Reserve(ctx, reservation(i)); !errors.Is(err, store.ErrOperationOwnershipLost) {
				t.Fatalf("finished claim reserved again: %v", err)
			}
		}
	}
}
