package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type migrationSessionRecorder struct {
	statements []string
}

func (r *migrationSessionRecorder) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.statements = append(r.statements, sql)
	return pgconn.CommandTag{}, nil
}

func TestOperationPoolConfigRejectsDeadlockingPool(t *testing.T) {
	if _, err := operationPoolConfig("postgres://norn:norn@localhost/norn?pool_max_conns=1"); err == nil {
		t.Fatal("expected undersized pool to be rejected")
	}
	config, err := operationPoolConfig("postgres://norn:norn@localhost/norn?pool_max_conns=4")
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns != 4 {
		t.Fatalf("max connections = %d", config.MaxConns)
	}
}

func TestConfigureMigrationSessionUsesRoleConfigurableSettings(t *testing.T) {
	recorder := &migrationSessionRecorder{}
	if err := configureMigrationSession(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.statements) != 1 || recorder.statements[0] != `SET LOCAL lock_timeout = '30s'` {
		t.Fatalf("migration session statements = %#v", recorder.statements)
	}
	for _, statement := range recorder.statements {
		if strings.Contains(statement, "deadlock_timeout") {
			t.Fatalf("migration configured privileged deadlock_timeout: %q", statement)
		}
	}
}
