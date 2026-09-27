package etcdstore

import (
	"context"
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
	accept := func(key string) store.OperationClaim {
		t.Helper()
		request := store.OperationAcceptance{
			Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"},
				Kind: "database.catalog-activate", Resource: "database-catalog", Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "database.catalog-activate", Ref: "database-catalog", Source: "test",
				Risk: "write", MaxAttempts: 1},
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
	stale := accept("stale")
	owner, err := client.Get(ctx, adapter.ownerKey(stale.OperationID()))
	if err != nil || len(owner.Kvs) != 1 {
		t.Fatalf("owner: %v", err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, stale, 0, postgresCatalogFixture(), "operator", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale claim changed routing: %v", err)
	}
	if _, err := adapter.ActiveDatabaseCatalog(ctx); !errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) {
		t.Fatalf("catalog appeared: %v", err)
	}
	live := accept("live")
	activated, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, live, 0, postgresCatalogFixture(), "operator", map[string]interface{}{"expectedRevision": 0})
	if err != nil || activated.Revision != 1 {
		t.Fatalf("activate %+v: %v", activated, err)
	}
	finished, err := adapter.GetOperation(ctx, live.OperationID())
	if err != nil || finished.Status != model.OperationSucceeded || fmt.Sprint(finished.Metadata["revision"]) != "1" || finished.Metadata["storedDigest"] != activated.Digest {
		t.Fatalf("terminal receipt %+v: %v", finished, err)
	}
	if _, err := adapter.ActivatePostgresDatabaseCatalogClaimed(ctx, live, 1, postgresCatalogFixture(), "operator", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
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
