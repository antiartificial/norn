package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/internal/pgtest"
)

// This disposable rehearsal establishes the minimum PostgreSQL session
// behavior the M6 source-writer fence must handle. It is not a cutover lane:
// the coordinator still needs a durable intent and an inventory of every
// writer before it may invoke a provider-specific fence.
func TestPostgresWriterFenceRequiresSessionTermination(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "cutover_source")
	server.CreatePasswordRole(t, "cutover_runtime", "disposable-fence-password", "cutover_source")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	config, err := pgx.ParseConfig(server.URL("cutover_source"))
	if err != nil {
		t.Fatal(err)
	}
	config.User, config.Password = "cutover_runtime", "disposable-fence-password"
	runtime, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	var runtimePID int32
	if err := runtime.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&runtimePID); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.Exec(ctx, `ALTER ROLE cutover_runtime NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := pgx.ConnectConfig(ctx, config); err == nil {
		t.Fatal("NOLOGIN admitted a fresh runtime connection")
	}
	var stillConnected int
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&stillConnected); err != nil || stillConnected != 1 {
		t.Fatalf("NOLOGIN unexpectedly closed the existing writer: %v", err)
	}

	var terminated bool
	if err := admin.QueryRow(ctx, "SELECT pg_terminate_backend($1)", runtimePID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("existing runtime backend was not terminated: %v", err)
	}
	var canLogin bool
	if err := admin.QueryRow(ctx, "SELECT rolcanlogin FROM pg_roles WHERE rolname = $1", config.User).Scan(&canLogin); err != nil || canLogin {
		t.Fatalf("runtime role did not remain NOLOGIN: %v", err)
	}
	var sessions int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE usename = $1", config.User).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("runtime sessions remain after termination: %d, %v", sessions, err)
	}
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&stillConnected); err == nil {
		t.Fatal("preexisting runtime session still queried after termination")
	}
	if _, err := pgx.ConnectConfig(ctx, config); err == nil {
		t.Fatal("fenced runtime role reconnected")
	}
}
