package etcdstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/database"
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
