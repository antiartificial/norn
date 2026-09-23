package main

import (
	"context"
	"fmt"
	"strings"

	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// configureDatabaseTargets returns nil when no database profile is set,
// preserving v2 routing. Otherwise the active catalog must exist, contain the
// profile, and the secret directory must be private; any gap fails startup.
func configureDatabaseTargets(ctx context.Context, cfg *config.Config, db *store.DB) (*pipeline.DatabaseTargets, error) {
	if cfg.DatabaseProfile == "" {
		return nil, nil
	}
	switch strings.ToLower(cfg.DatabaseProfile) {
	case "development", "production":
		return nil, fmt.Errorf("NORN_DATABASE_PROFILE names a deployment profile and must not reuse NORN_PROFILE security names")
	}
	secrets, err := database.NewDirectorySecretSource(cfg.DatabaseSecretDir)
	if err != nil {
		return nil, fmt.Errorf("NORN_DATABASE_SECRET_DIR: %w", err)
	}
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil {
		secrets.Close()
		return nil, fmt.Errorf("NORN_DATABASE_PROFILE is set but no valid database catalog is active")
	}
	found := false
	for _, profile := range active.Catalog.Profiles {
		found = found || profile.ID == cfg.DatabaseProfile
	}
	if !found {
		secrets.Close()
		return nil, fmt.Errorf("database profile %q is not defined in catalog revision %d", cfg.DatabaseProfile, active.Revision)
	}
	return &pipeline.DatabaseTargets{ProfileID: cfg.DatabaseProfile, Catalog: db.ActiveDatabaseCatalog, Secrets: secrets}, nil
}
