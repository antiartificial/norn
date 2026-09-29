package etcdstore_test

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
	"norn/v2/api/store"
)

func TestV3ClaimedCatalogResolvesLostCommitResponseEtcd(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/catalog-lost-commit/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("catalog-lost-commit-test-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &lostCommitClient{Client: client}
	authority := uuid.NewString()
	adapter, err := etcdstore.NewV3OperationStore(wrapped, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	catalog := database.Catalog{APIVersion: database.APIVersion}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	request := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "database.catalog-activate", Resource: "database-catalog", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "database.catalog-activate", Ref: "database-catalog", Risk: "write", Source: "test", MaxAttempts: 2,
			Payload: map[string]interface{}{"expectedRevision": "0", "catalog": string(encoded), "catalogDigest": "sha256:" + hex.EncodeToString(digest[:]), "requestedBy": "test/operator"}},
		Audit: store.AcceptanceAuditContext{Source: "test"},
	}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(context.Background(), "catalog-worker", time.Minute, []string{"database.catalog-activate"})
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(context.Background(), "database.catalog-activate:database-catalog")
	if err != nil || !acquired {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Release()
	wrapped.loseNext = true
	activated, err := adapter.ActivatePostgresDatabaseCatalogClaimed(context.Background(), claim, lock, 0, catalog, "test/operator", nil)
	if err != nil || activated.Revision != 1 {
		t.Fatalf("lost response was not resolved: %+v %v", activated, err)
	}
	operation, err := adapter.GetOperation(context.Background(), claim.OperationID())
	if err != nil || operation.Status != model.OperationSucceeded {
		t.Fatalf("terminal receipt: %+v %v", operation, err)
	}
}
