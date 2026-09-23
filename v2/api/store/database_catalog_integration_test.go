package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func storeTestCatalog() database.Catalog {
	service := func(id, provider string) database.DatabaseService {
		return database.DatabaseService{APIVersion: database.APIVersion, ID: id, Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL,
			EngineVersion: "16", ProviderRef: provider, Endpoint: database.DatabaseEndpoint{Host: "/var/run/" + id, Port: 5432}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}}}
	}
	return database.Catalog{
		APIVersion: database.APIVersion,
		Services:   []database.DatabaseService{service("mini-app-pg", "local:mini-postgres"), service("spare-pg", "local:spare-postgres")},
		Bindings: []database.DatabaseBinding{
			{APIVersion: database.APIVersion, ID: "shop-primary", ServiceID: "mini-app-pg", Database: "shop", Role: "shop", Generation: 1, CredentialRef: "secret:shop", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
			{APIVersion: database.APIVersion, ID: "spare", ServiceID: "spare-pg", Database: "spare", Role: "spare", Generation: 1, CredentialRef: "secret:spare", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"shop-db": "shop-primary"},
			LegacyPostgres:   &database.LegacyPostgresDefault{MappingID: "mini-legacy", ServiceID: "mini-app-pg", Role: "legacy", Generation: 1, CredentialRef: "secret:legacy", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}}},
	}
}

func TestDatabaseCatalogRevisionsAreCompareAndSetAndPersistRetirements(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	if _, err := db.ActiveDatabaseCatalog(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("empty catalog = %v", err)
	}
	first, err := db.ActivateDatabaseCatalog(ctx, 0, storeTestCatalog(), "operator")
	if err != nil || first.Revision != 1 {
		t.Fatalf("activate 1 = %+v, %v", first, err)
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, 0, storeTestCatalog(), "operator"); !errors.Is(err, ErrDatabaseCatalogRevisionConflict) {
		t.Fatalf("stale expected revision = %v", err)
	}
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil || active.Revision != 1 || active.Digest != first.Digest || len(active.Catalog.Bindings) != 2 {
		t.Fatalf("active = %+v, %v", active, err)
	}

	// Retire the unreferenced spare binding, then (two-phase) its service.
	unbound := storeTestCatalog()
	unbound.Bindings = unbound.Bindings[:1]
	unbound.Retired = []database.RetiredResource{{Kind: database.RetiredBinding, ID: "spare"}}
	if second, err := db.ActivateDatabaseCatalog(ctx, 1, unbound, "operator"); err != nil || second.Revision != 2 {
		t.Fatalf("activate 2 = %+v, %v", second, err)
	}
	retired := cloneStoreCatalog(unbound)
	retired.Services = retired.Services[:1]
	retired.Retired = append(retired.Retired, database.RetiredResource{Kind: database.RetiredService, ID: "spare-pg"})
	if third, err := db.ActivateDatabaseCatalog(ctx, 2, retired, "operator"); err != nil || third.Revision != 3 {
		t.Fatalf("activate 3 = %+v, %v", third, err)
	}
	var retirements int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM database_catalog_retirements WHERE (id='spare' AND retired_revision=2) OR (id='spare-pg' AND retired_revision=3)`).Scan(&retirements); err != nil || retirements != 2 {
		t.Fatalf("durable retirements = %d, %v", retirements, err)
	}

	// Recreating the retired binding is refused, with or without the tombstone.
	recreated := storeTestCatalog()
	recreated.Retired = retired.Retired
	if _, err := db.ActivateDatabaseCatalog(ctx, 3, recreated, "operator"); err == nil {
		t.Fatal("retired binding recreated")
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, 3, storeTestCatalog(), "operator"); err == nil {
		t.Fatal("tombstones dropped and binding recreated")
	}
	// A target change without a generation bump is refused by the transition.
	moved := cloneStoreCatalog(retired)
	moved.Bindings[0].Database = "shop2"
	if _, err := db.ActivateDatabaseCatalog(ctx, 3, moved, "operator"); err == nil {
		t.Fatal("binding target changed without a generation bump")
	}
	// The durable history is enforced even when the adjacent catalog would
	// allow it: a tombstone recorded for a live ID blocks activation.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO database_catalog_retirements (kind,id,retired_revision) VALUES ('binding','shop-primary',3)`); err != nil {
		t.Fatal(err)
	}
	rotated := cloneStoreCatalog(retired)
	rotated.Bindings[0].CredentialRef = "secret:shop-rotated"
	if _, err := db.ActivateDatabaseCatalog(ctx, 3, rotated, "operator"); !errors.Is(err, ErrDatabaseCatalogRetiredIdentity) {
		t.Fatalf("durable retirement history bypassed: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM database_catalog_retirements WHERE id='shop-primary'`); err != nil {
		t.Fatal(err)
	}

	// Concurrent activations of the same expected revision: exactly one wins.
	var wins, conflicts int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := db.ActivateDatabaseCatalog(ctx, 3, rotated, "operator")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if errors.Is(err, ErrDatabaseCatalogRevisionConflict) {
				conflicts++
			} else {
				t.Errorf("concurrent activation: %v", err)
			}
		}()
	}
	wait.Wait()
	if wins != 1 || conflicts != 3 {
		t.Fatalf("concurrent activations wins=%d conflicts=%d", wins, conflicts)
	}

	// Stored bytes are verified on read.
	if _, err := db.Pool.Exec(ctx, `UPDATE database_catalog_revisions SET catalog = convert_to('{"apiVersion":"x"}','UTF8') WHERE revision=4`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActiveDatabaseCatalog(ctx); err == nil {
		t.Fatal("tampered catalog revision accepted")
	}
}

func TestClaimedCatalogActivationRequiresTheLiveClaim(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	now := time.Now().UTC()
	op := &model.Operation{ID: uuid.NewString(), Kind: "database.catalog-activate", Ref: "database-catalog", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1, StartedAt: now, NextAttemptAt: now.Add(-time.Second)}
	if err := db.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "catalog-owner", 50*time.Millisecond, []string{"database.catalog-activate"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	time.Sleep(150 * time.Millisecond) // the lease lapses
	if _, err := db.ActivateDatabaseCatalogClaimed(ctx, claim, 0, storeTestCatalog(), "operator", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("activation with a lapsed claim = %v", err)
	}
	if _, err := db.ActiveDatabaseCatalog(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("lapsed claim changed routing: %v", err)
	}
	second := &model.Operation{ID: uuid.NewString(), Kind: "database.catalog-activate", Ref: "database-catalog", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1, StartedAt: now, NextAttemptAt: now.Add(-time.Second)}
	if err := db.InsertOperation(ctx, second); err != nil {
		t.Fatal(err)
	}
	reclaimed, live, err := db.ClaimNextOperation(ctx, "catalog-owner-2", time.Minute, []string{"database.catalog-activate"})
	if err != nil || reclaimed == nil || reclaimed.ID != second.ID {
		t.Fatalf("live claim = %+v, %v", reclaimed, err)
	}
	if revision, err := db.ActivateDatabaseCatalogClaimed(ctx, live, 0, storeTestCatalog(), "operator", map[string]interface{}{"catalogDigest": "d"}); err != nil || revision.Revision != 1 {
		t.Fatalf("activation with the live claim = %+v, %v", revision, err)
	}
	// Activation and the operation's terminal record committed together.
	finished, err := db.GetOperation(ctx, second.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Metadata["catalogDigest"] != "d" || finished.Metadata["revision"] != float64(1) {
		t.Fatalf("operation after activation = %+v, %v", finished, err)
	}
	// Neither the finished claim nor the superseded one can activate again.
	for _, stale := range []OperationClaim{live, claim} {
		if _, err := db.ActivateDatabaseCatalogClaimed(ctx, stale, 1, storeTestCatalog(), "operator", nil); !errors.Is(err, ErrOperationOwnershipLost) {
			t.Fatalf("stale claim = %v", err)
		}
	}
	if active, err := db.ActiveDatabaseCatalog(ctx); err != nil || active.Revision != 1 {
		t.Fatalf("active = %+v, %v", active, err)
	}
}

// CheckOperationClaim is the pre-external-write check: live claims pass,
// lapsed or superseded ones fail by the database wall clock, and the check
// never extends a lease.
func TestCheckOperationClaimUsesTheWallClock(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	now := time.Now().UTC()
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "claim-check", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1, StartedAt: now, NextAttemptAt: now.Add(-time.Second)}
	if err := db.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "claim-owner", 300*time.Millisecond, []string{"app.deploy"})
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := db.CheckOperationClaim(ctx, claim); err != nil {
		t.Fatalf("live claim = %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := db.CheckOperationClaim(ctx, claim); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("lapsed claim = %v", err)
	}
	if err := db.CheckOperationClaim(ctx, OperationClaim{}); err == nil {
		t.Fatal("empty claim passed")
	}
}

func cloneStoreCatalog(catalog database.Catalog) database.Catalog {
	out := catalog
	out.Services = append([]database.DatabaseService(nil), catalog.Services...)
	out.Bindings = append([]database.DatabaseBinding(nil), catalog.Bindings...)
	out.Retired = append([]database.RetiredResource(nil), catalog.Retired...)
	out.Profiles = nil
	for _, profile := range catalog.Profiles {
		mapping := map[string]string{}
		for key, value := range profile.DatabaseBindings {
			mapping[key] = value
		}
		profile.DatabaseBindings = mapping
		if profile.LegacyPostgres != nil {
			legacy := *profile.LegacyPostgres
			profile.LegacyPostgres = &legacy
		}
		out.Profiles = append(out.Profiles, profile)
	}
	return out
}
