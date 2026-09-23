package pipeline

import (
	"context"
	"testing"

	"norn/v2/api/model"
)

func TestReviewLegacyMigrationDoesNotInheritControlEnvironment(t *testing.T) {
	t.Setenv("NORN_API_TOKEN", "review-only-control-token")
	t.Setenv("DATABASE_URL", "postgresql://review-control.invalid/control")
	t.Setenv("PGSERVICE", "review-control-service")
	t.Setenv("PGPASSWORD", "review-only-control-password")
	p := &Pipeline{}
	st := &state{spec: &model.InfraSpec{
		App:        "legacy-migration-review",
		Migrations: `test -z "${NORN_API_TOKEN+x}" && test -z "${DATABASE_URL+x}" && test -z "${PGSERVICE+x}" && test -z "${PGPASSWORD+x}"`,
	}, workDir: t.TempDir()}
	if err := p.migrate(context.Background(), st, nil); err != nil {
		t.Fatalf("migration inherited control-plane environment: %v", err)
	}
}
