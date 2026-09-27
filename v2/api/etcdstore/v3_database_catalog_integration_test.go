package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func postgresCatalogFixture() database.Catalog {
	return database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "pg", Generation: 1, Purpose: database.PurposeApplication,
			Engine: database.EnginePostgreSQL, EngineVersion: "16", ProviderRef: "local:pg", Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 5432},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "primary", ServiceID: "pg", Database: "app", Role: "app", Generation: 1,
			CredentialRef: "secret:app/db", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "local", Topology: database.DeploymentTopologyLocal,
			AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"primary": "primary"}}}}
}

func TestV3PostgresCatalogClaimedActivationEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	catalog := postgresCatalogFixture()
	encodedCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encodedCatalog)
	accept := func(key, catalogValue string) store.OperationClaim {
		t.Helper()
		request := store.OperationAcceptance{
			Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"},
				Kind: "database.catalog-activate", Resource: "database-catalog", Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "database.catalog-activate", Ref: "database-catalog", Source: "test",
				Risk: "write", MaxAttempts: 1, Payload: map[string]interface{}{"expectedRevision": "0", "catalog": catalogValue,
					"catalogDigest": "sha256:" + hex.EncodeToString(digest[:]), "requestedBy": "operator"}},
			Audit: store.AcceptanceAuditContext{Source: "test", Scopes: []string{"database:write"}},
		}
		var err error
		request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, request); err != nil {
			t.Fatal(err)
		}
		operation, claim, err := adapter.ClaimNextOperation(ctx, "catalog-worker", time.Minute, []string{"database.catalog-activate"})
		if err != nil || operation == nil || operation.ID != request.Operation.ID {
			t.Fatalf("claim %+v: %v", operation, err)
		}
		return claim
	}
	stale := accept("stale", string(encodedCatalog))
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "database.catalog-activate:database-catalog")
	if err != nil || !acquired {
		t.Fatalf("catalog app lock: %v", err)
	}
	defer func() { lock.Release() }()
	owner, err := client.Get(ctx, adapter.ownerKey(stale.OperationID()))
	if err != nil || len(owner.Kvs) != 1 {
		t.Fatalf("owner: %v", err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, stale, lock, 0, postgresCatalogFixture(), "operator", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale claim changed routing: %v", err)
	}
	if _, err := adapter.ActiveDatabaseCatalog(ctx); !errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) {
		t.Fatalf("catalog appeared: %v", err)
	}
	if err := adapter.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, recoveredClaim, err := adapter.ClaimNextOperation(ctx, "catalog-recovery-worker", time.Minute, []string{"database.catalog-activate"})
	if err != nil || recovered == nil || recovered.ID != stale.OperationID() || recovered.Metadata["recoveredAfterLeaseExpiry"] != true {
		t.Fatalf("expired catalog claim was not safely requeued: %+v %v", recovered, err)
	}
	if err := adapter.FinishClaimedOperation(ctx, recoveredClaim, model.OperationFailed, "test cleanup", nil); err != nil {
		t.Fatal(err)
	}
	wrong := accept("wrong", `{"apiVersion":"wrong"}`)
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, wrong, lock, 0, catalog, "operator", nil); err == nil {
		t.Fatal("different signed catalog activated")
	}
	if _, err := adapter.ActiveDatabaseCatalog(ctx); !errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) {
		t.Fatalf("wrong catalog changed routing: %v", err)
	}
	if err := adapter.FinishClaimedOperation(ctx, wrong, model.OperationFailed, "refused", nil); err != nil {
		t.Fatal(err)
	}
	live := accept("live", string(encodedCatalog))
	lock.Release()
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, live, lock, 0, catalog, "operator", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("released app lock changed routing: %v", err)
	}
	lock, acquired, err = adapter.AcquireAppOperationLock(ctx, "database.catalog-activate:database-catalog")
	if err != nil || !acquired {
		t.Fatalf("reacquire catalog lock: %v", err)
	}
	activated, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, live, lock, 0, postgresCatalogFixture(), "operator", map[string]interface{}{"expectedRevision": 0})
	if err != nil || activated.Revision != 1 {
		t.Fatalf("activate %+v: %v", activated, err)
	}
	finished, err := adapter.GetOperation(ctx, live.OperationID())
	if err != nil || finished.Status != model.OperationSucceeded || fmt.Sprint(finished.Metadata["revision"]) != "1" || finished.Metadata["storedDigest"] != activated.Digest {
		t.Fatalf("terminal receipt %+v: %v", finished, err)
	}
	public, err := json.Marshal(finished)
	if err != nil || strings.Contains(string(public), "secret:app/db") || strings.Contains(string(public), `"catalog":`) {
		t.Fatalf("catalog leaked through operation read projection: %v", err)
	}
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, live, lock, 1, postgresCatalogFixture(), "operator", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("completed claim reused: %v", err)
	}
}

func TestV3PostgresCatalogActivationEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	catalog := postgresCatalogFixture()
	first, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil || first.Revision != 1 {
		t.Fatalf("first revision: %+v %v", first, err)
	}
	second, err := adapter.ActiveDatabaseCatalog(ctx)
	if err != nil || second.Digest != first.Digest || second.Revision != 1 {
		t.Fatalf("active: %+v %v", second, err)
	}
	if _, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 0, catalog, "stale"); !errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) {
		t.Fatalf("stale activation: %v", err)
	}
	removed := postgresCatalogFixture()
	removed.Profiles[0].DatabaseBindings = nil
	if _, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 1, removed, "operator"); err != nil {
		t.Fatal(err)
	}
	removed.Bindings = nil
	removed.Retired = []database.RetiredResource{{Kind: database.RetiredBinding, ID: "primary"}}
	if _, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 2, removed, "operator"); err != nil {
		t.Fatal(err)
	}
	reused := postgresCatalogFixture()
	if _, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 3, reused, "operator"); err == nil {
		t.Fatal("retired binding reused")
	}
	if _, err := adapter.DatabaseCatalogRevision(ctx, 1); err != nil {
		t.Fatalf("immutable revision unavailable: %v", err)
	}
	key := adapter.databaseCatalogRevisionKey(1)
	if _, err := client.Put(ctx, key, `{"revision":1,"digest":"bad","catalog":{},"actor":"operator"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.DatabaseCatalogRevision(ctx, 1); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered revision accepted: %v", err)
	}
}
