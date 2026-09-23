package startup

import (
	"context"
	"fmt"

	"norn/v2/api/store"
)

// ApplySchemaMode is the shared API/host-agent schema startup behavior.
func ApplySchemaMode(ctx context.Context, migrator *store.SchemaMigrator, cfg Config) (store.SchemaStatus, error) {
	if cfg.SchemaTimeout <= 0 {
		return store.SchemaStatus{}, fmt.Errorf("%s must be positive", SchemaTimeoutEnv)
	}
	schemaCtx, cancel := context.WithTimeout(ctx, cfg.SchemaTimeout)
	defer cancel()
	switch cfg.SchemaMode {
	case SchemaModeAuto, SchemaModeMigrateOnly:
		return migrator.Migrate(schemaCtx)
	case SchemaModeCheck:
		// Check itself is a read-only repeatable-read transaction. Even a passive
		// qualification candidate must prove the intended serving writer is
		// compatible before the release can be promoted.
		return migrator.Check(schemaCtx, store.SchemaAccessReadWrite)
	default:
		return store.SchemaStatus{}, fmt.Errorf("unsupported schema mode %q", cfg.SchemaMode)
	}
}
