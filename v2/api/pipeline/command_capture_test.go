package pipeline

import (
	"context"
	"strings"
	"testing"

	"norn/v2/api/model"
)

// A failing migration that floods its output reports a bounded error that
// keeps the start and the final lines, with the truncation made explicit.
func TestFailingMigrationOutputIsBoundedWhileRunning(t *testing.T) {
	spec := &model.InfraSpec{App: "legacy", Migrations: "echo FIRST; head -c 33554432 /dev/zero | tr '\\000' 'x'; echo; echo LAST-ERROR >&2; exit 4",
		Infrastructure: &model.Infrastructure{Postgres: &model.PostgresInfra{Database: "legacy_db"}}}
	err := (&Pipeline{}).migrate(context.Background(), &state{spec: spec, databaseOpened: true}, nil)
	if err == nil {
		t.Fatal("failing migration succeeded")
	}
	message := err.Error()
	if len(message) > commandOutputHead+commandOutputTail+256 || !strings.Contains(message, "FIRST") ||
		!strings.HasSuffix(strings.TrimSpace(message), "LAST-ERROR") || !strings.Contains(message, "bytes of output truncated") {
		t.Fatalf("migration error length %d, head %q", len(message), message[:min(len(message), 64)])
	}
}
