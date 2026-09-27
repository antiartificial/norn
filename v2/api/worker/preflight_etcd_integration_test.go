package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// TestBackendNeutralPreflightEtcd executes the entire supported aggregate on a
// real etcd endpoint: signed acceptance, claimed execution, leased app lock,
// immutable source receipt, and fenced terminalization. The fixture has no
// build configuration, so this test cannot accidentally exercise legacy
// Docker/test effects outside the aggregate's declared capability.
func TestBackendNeutralPreflightEtcd(t *testing.T) {
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
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
	prefix := "/norn-conf/m3-preflight/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-preflight-integration-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}

	apps := t.TempDir()
	appDir := filepath.Join(apps, "receipt-demo")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: receipt-demo\ndeploy: false\nprocesses:\n  worker:\n    command: sleep 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "README.md"), []byte("deterministic source fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := &model.InfraSpec{App: "receipt-demo", Deploy: false, Processes: map[string]model.Process{"worker": {Command: "sleep 1"}}}
	pipe := &pipeline.Pipeline{OperationStore: operations, CheckpointStore: operations, AppsDir: apps, NetworkMode: "local"}
	request := pipeline.EnqueueRequest{
		Authority: authority,
		Actor:     store.OperationActor{Issuer: "integration", Subject: "operator"},
		Key:       uuid.NewString(),
		Audit:     store.AcceptanceAuditContext{Source: "preflight-etcd-integration"},
	}
	accepted, err := pipe.Preflight(ctx, spec, "HEAD", request)
	if err != nil || len(accepted.Intent.CanonicalBytes) == 0 || accepted.Intent.Signature.Value == "" {
		t.Fatalf("signed acceptance=%+v err=%v", accepted, err)
	}

	worker := NewOperationWorkerForKinds(operations, pipe, []string{"app.preflight"})
	if err := worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	identity := store.OperationRequestIdentity{Authority: authority, Actor: request.Actor, Kind: "app.preflight", Resource: "receipt-demo", Key: request.Key}
	resolved, err := operations.Resolve(ctx, identity, accepted.Intent.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Operation.Status != model.OperationSucceeded || resolved.Operation.FinishedAt == nil || resolved.Operation.Metadata["sourceIdentity"] == "" {
		t.Fatalf("terminal operation=%+v", resolved.Operation)
	}
	checkpoint, err := operations.LoadOperationCheckpoint(ctx, accepted.Operation.ID, store.CheckpointSource)
	if err != nil || checkpoint == nil || checkpoint.ClaimGeneration != 1 || checkpoint.OutputsDigest == "" {
		t.Fatalf("source checkpoint=%+v err=%v", checkpoint, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, "receipt-demo")
	if err != nil || !acquired {
		if lock != nil {
			lock.Release()
		}
		t.Fatalf("terminal worker left app lock active lock=%v acquired=%v err=%v", lock, acquired, err)
	}
	lock.Release()
}
