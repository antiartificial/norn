package etcdstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3OperationCheckpointFencesAndVerifiesEtcd(t *testing.T) {
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
	prefix := "/norn-conf/v3-checkpoints/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-checkpoint-conformance-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	adapter, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "etcd-checkpoint", Subject: "worker"}, Kind: "app.preflight", Resource: "app/checkpoint", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "checkpoint", Ref: "HEAD", Risk: "read-only", Source: "etcd-checkpoint", MaxAttempts: 1},
		Audit:     store.AcceptanceAuditContext{Source: "etcd-checkpoint"},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "checkpoint-worker", time.Minute, []string{"app.preflight"})
	if err != nil {
		t.Fatal(err)
	}
	outputs := json.RawMessage(`{"treeDigest":"sha256:source"}`)
	stored, err := adapter.RecordOperationCheckpoint(ctx, claim, store.CheckpointSource, outputs)
	if err != nil || stored.ClaimGeneration != claim.Generation() {
		t.Fatalf("record checkpoint=%+v err=%v", stored, err)
	}
	repeated, err := adapter.RecordOperationCheckpoint(ctx, claim, store.CheckpointSource, outputs)
	if err != nil || repeated.OutputsDigest != stored.OutputsDigest {
		t.Fatalf("repeat checkpoint=%+v err=%v", repeated, err)
	}
	if _, err := adapter.RecordOperationCheckpoint(ctx, claim, store.CheckpointSource, json.RawMessage(`{"treeDigest":"sha256:other"}`)); !errors.Is(err, store.ErrCheckpointConflict) {
		t.Fatalf("different checkpoint error=%v", err)
	}

	checkpointKey := prefix + "/v3/checkpoints/" + acceptance.Operation.ID + "/" + store.CheckpointSource
	t.Run("corrupt checkpoint fails closed", func(t *testing.T) {
		if _, err := client.Put(ctx, checkpointKey, `{"not":"a checkpoint"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.LoadOperationCheckpoint(ctx, acceptance.Operation.ID, store.CheckpointSource); err == nil {
			t.Fatal("corrupt checkpoint was returned")
		}
	})

	t.Run("foreign checkpoint fails closed", func(t *testing.T) {
		foreign := json.RawMessage(`{"treeDigest":"sha256:foreign"}`)
		digest := sha256.Sum256(foreign)
		value, err := json.Marshal(map[string]interface{}{
			"operationId":     uuid.NewString(),
			"stage":           store.CheckpointSource,
			"claimGeneration": claim.Generation(),
			"outputs":         json.RawMessage(foreign),
			"outputsDigest":   "sha256:" + hex.EncodeToString(digest[:]),
			"createdAt":       time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, checkpointKey, string(value)); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.LoadOperationCheckpoint(ctx, acceptance.Operation.ID, store.CheckpointSource); err == nil {
			t.Fatal("foreign checkpoint was returned")
		}
	})

	owner, err := client.Get(ctx, prefix+"/v3/owners/"+acceptance.Operation.ID)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RecordOperationCheckpoint(ctx, claim, store.CheckpointBuild, json.RawMessage(`{"imageTag":"ignored"}`)); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale claim recorded checkpoint: %v", err)
	}
}
