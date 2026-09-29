package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestEtcdCatalogAcceptedOperationWorker(t *testing.T) {
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/catalog-worker/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("catalog-worker-integration-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	catalog := database.Catalog{APIVersion: database.APIVersion}
	encodedCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encodedCatalog)
	request := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "integration", Subject: "operator"},
			Kind: pipeline.CatalogActivationKind, Resource: "database-catalog", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: pipeline.CatalogActivationKind, Ref: "database-catalog", Source: "test", Risk: "write", MaxAttempts: 3,
			Payload: map[string]interface{}{"expectedRevision": "0", "catalog": string(encodedCatalog), "catalogDigest": "sha256:" + hex.EncodeToString(digest[:]), "requestedBy": "integration/operator"}},
		Audit: store.AcceptanceAuditContext{Source: "catalog-worker-integration", Scopes: []string{"platform:operate"}},
	}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := operations.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	w := NewOperationWorkerForKinds(operations, &pipeline.EtcdCatalogExecutor{Catalog: operations}, []string{pipeline.CatalogActivationKind})
	if err := w.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished, err := operations.GetOperation(context.Background(), accepted.Operation.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.FinishedAt == nil {
		t.Fatalf("terminal operation: %+v %v", finished, err)
	}
	active, err := operations.ActiveDatabaseCatalog(context.Background())
	if err != nil || active.Revision != 1 || active.Digest != hex.EncodeToString(digest[:]) {
		t.Fatalf("active catalog: %+v %v", active, err)
	}
	if time.Since(*finished.FinishedAt) > time.Minute {
		t.Fatal("terminal receipt timestamp is stale")
	}
}
