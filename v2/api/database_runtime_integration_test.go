package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/store"
)

func TestDatabaseTargetsConfigurationFailsClosed(t *testing.T) {
	// Unset keeps v2 routing without touching the store.
	if targets, err := configureDatabaseTargets(context.Background(), &config.Config{}, nil); targets != nil || err != nil {
		t.Fatalf("unset profile = %+v, %v", targets, err)
	}
	if _, err := configureDatabaseTargets(context.Background(), &config.Config{DatabaseProfile: "Production"}, nil); err == nil {
		t.Fatal("security profile name accepted as a database profile")
	}
	if _, err := configureDatabaseTargets(context.Background(), &config.Config{DatabaseProfile: "mini", DatabaseSecretDir: "relative/secrets"}, nil); err == nil {
		t.Fatal("relative secret directory accepted")
	}

	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "norn_database_runtime_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop database runtime test schema: %v", err)
		}
	}()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DatabaseProfile: "mini", DatabaseSecretDir: secretDir}

	// A profile without an active catalog refuses startup.
	if _, err := configureDatabaseTargets(ctx, cfg, db); err == nil {
		t.Fatal("database profile accepted without an active catalog")
	}
	catalog := database.Catalog{
		APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "mini-app-pg", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL,
			EngineVersion: "16", ProviderRef: "local:mini-postgres", Endpoint: database.DatabaseEndpoint{Host: "/var/run/postgresql", Port: 5432},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot}}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "other", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			LegacyPostgres: &database.LegacyPostgresDefault{MappingID: "mini-legacy-pg", ServiceID: "mini-app-pg", Role: "app", Generation: 1, CredentialRef: "secret:legacy", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}}},
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	// The active catalog must define the configured profile.
	if _, err := configureDatabaseTargets(ctx, cfg, db); err == nil || !strings.Contains(err.Error(), "not defined in catalog revision 1") {
		t.Fatalf("undefined profile = %v", err)
	}
	catalog.Profiles[0].ID = "mini"
	if _, err := db.ActivateDatabaseCatalog(ctx, 1, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	targets, err := configureDatabaseTargets(ctx, cfg, db)
	if err != nil || targets == nil || targets.ProfileID != "mini" {
		t.Fatalf("configured targets = %+v, %v", targets, err)
	}
	if closer, ok := targets.Secrets.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
