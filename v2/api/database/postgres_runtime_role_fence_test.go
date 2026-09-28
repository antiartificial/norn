package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/internal/pgtest"
)

func TestCatalogBoundPostgresRuntimeRoleFence(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "cutover_source")
	server.CreatePasswordRole(t, "cutover_runtime", "disposable-runtime-password", "cutover_source")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var version int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 160000 {
		t.Skip("delegated fence rehearsal requires PostgreSQL 16 or later")
	}
	for _, statement := range []string{
		`CREATE ROLE cutover_fence LOGIN CREATEROLE`,
		`GRANT cutover_runtime TO cutover_fence WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`,
		`GRANT pg_signal_backend TO cutover_fence`,
		`GRANT pg_read_all_stats TO cutover_fence`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	config, err := pgx.ParseConfig(server.URL("cutover_source"))
	if err != nil {
		t.Fatal(err)
	}
	config.User, config.Password = "cutover_runtime", "disposable-runtime-password"
	runtime, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	resolved := ResolvedBinding{
		Target: TargetIdentity{ServiceID: "source", ServiceGeneration: 1, BindingID: "source-app", BindingGeneration: 1,
			Engine: EnginePostgreSQL, Database: "cutover_source", Role: "cutover_runtime"},
		Purpose: PurposeApplication, Endpoint: DatabaseEndpoint{Host: server.SocketDir, Port: server.Port},
		CredentialRef: "secret:runtime", TLS: DatabaseTLS{Mode: TLSDisabled},
		PostgresFence: &PostgresFenceCredentials{Generation: 1, Role: "cutover_fence", CredentialRef: "secret:fence"},
	}
	secrets := literalSecrets{
		"secret:runtime": `{"password":"disposable-runtime-password"}`,
		"secret:fence":   `{"password":""}`,
	}
	if err := InspectPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets); err == nil {
		t.Fatal("unfenced runtime role passed inspection")
	}
	stale := resolved.Target
	stale.BindingGeneration++
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, stale, secrets); err == nil {
		t.Fatal("stale target identity was accepted")
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	if err := InspectPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&one); err == nil {
		t.Fatal("preexisting runtime session survived fence")
	}
	if _, err := pgx.ConnectConfig(ctx, config); err == nil {
		t.Fatal("runtime role reconnected after fence")
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatalf("retry after lost fence response was not idempotent: %v", err)
	}
}
