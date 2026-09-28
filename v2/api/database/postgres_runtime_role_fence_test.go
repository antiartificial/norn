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
		`CREATE ROLE unrelated_app LOGIN`,
		`CREATE ROLE underprivileged_fence LOGIN`,
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
	unrelatedConfig, err := pgx.ParseConfig(server.URL("cutover_source"))
	if err != nil {
		t.Fatal(err)
	}
	unrelatedConfig.User = "unrelated_app"
	unrelated, err := pgx.ConnectConfig(ctx, unrelatedConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer unrelated.Close(context.Background())
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
	underprivileged := resolved
	underprivileged.PostgresFence = &PostgresFenceCredentials{Generation: 1, Role: "underprivileged_fence", CredentialRef: "secret:fence"}
	if _, err := PreflightPostgresRuntimeRoleFenceForCutover(ctx, underprivileged, underprivileged.Target, secrets); err == nil {
		t.Fatal("underprivileged principal passed read-only fence preflight")
	}
	preflight, err := PreflightPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets)
	if err != nil || preflight.Target != resolved.Target || preflight.FenceGeneration != 1 || preflight.ServerVersion < 160000 || !preflight.CanLogin || preflight.Sessions < 1 {
		t.Fatalf("read-only source fence preflight=%+v err=%v", preflight, err)
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, underprivileged, underprivileged.Target, secrets); err == nil {
		t.Fatal("fence role without delegated authority was accepted")
	}
	var one int
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("rejected fence changed the runtime session: %v", err)
	}
	if err := InspectPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets); err == nil {
		t.Fatal("unfenced runtime role passed inspection")
	}
	stale := resolved.Target
	stale.BindingGeneration++
	if _, err := PreflightPostgresRuntimeRoleFenceForCutover(ctx, resolved, stale, secrets); err == nil {
		t.Fatal("stale target passed read-only fence preflight")
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, stale, secrets); err == nil {
		t.Fatal("stale target identity was accepted")
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	if err := InspectPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	preflight, err = PreflightPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets)
	if err != nil || preflight.CanLogin || preflight.Sessions != 0 {
		t.Fatalf("fenced source role preflight=%+v err=%v", preflight, err)
	}
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&one); err == nil {
		t.Fatal("preexisting runtime session survived fence")
	}
	if err := unrelated.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("fence terminated an unrelated role's session: %v", err)
	}
	if _, err := pgx.ConnectConfig(ctx, config); err == nil {
		t.Fatal("runtime role reconnected after fence")
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatalf("retry after lost fence response was not idempotent: %v", err)
	}
}

func TestPostgresRuntimeFencePreservesVerifyFullTLS(t *testing.T) {
	server := pgtest.StartTLS(t)
	server.CreateDatabase(t, "cutover_tls")
	server.CreatePasswordRole(t, "runtime_tls", "runtime-tls-password", "cutover_tls")
	server.CreatePasswordRole(t, "fence_tls", "fence-tls-password", "postgres")
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
		`ALTER ROLE fence_tls CREATEROLE`,
		`GRANT runtime_tls TO fence_tls WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`,
		`GRANT pg_signal_backend TO fence_tls`,
		`GRANT pg_read_all_stats TO fence_tls`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	resolved := ResolvedBinding{
		Target: TargetIdentity{ServiceID: "source-tls", ServiceGeneration: 1, BindingID: "source-tls-app", BindingGeneration: 1,
			Engine: EnginePostgreSQL, Database: "cutover_tls", Role: "runtime_tls"},
		Purpose: PurposeApplication, Endpoint: DatabaseEndpoint{Host: "localhost", Port: server.Port},
		CredentialRef: "secret:runtime", TLS: DatabaseTLS{Mode: TLSVerifyFull, ServerName: "localhost", CARef: "secret:ca"},
		PostgresFence: &PostgresFenceCredentials{Generation: 1, Role: "fence_tls", CredentialRef: "secret:fence"},
	}
	secrets := literalSecrets{
		"secret:runtime":  `{"password":"runtime-tls-password"}`,
		"secret:fence":    `{"password":"fence-tls-password"}`,
		"secret:ca":       string(server.CAPEM),
		"secret:wrong-ca": string(pgtest.OtherCAPEM(t)),
	}
	runtimeSession, err := OpenSession(ctx, resolved, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeSession.Close()
	runtime, err := pgx.ConnectConfig(ctx, runtimeSession.config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	wrong := resolved
	wrong.TLS.CARef = "secret:wrong-ca"
	if _, err := PreflightPostgresRuntimeRoleFenceForCutover(ctx, wrong, wrong.Target, secrets); err == nil {
		t.Fatal("wrong CA passed read-only fence preflight")
	}
	preflight, err := PreflightPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets)
	if err != nil || !preflight.CanLogin || preflight.Sessions < 1 {
		t.Fatalf("verify-full fence preflight=%+v err=%v", preflight, err)
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, wrong, wrong.Target, secrets); err == nil {
		t.Fatal("wrong CA was accepted for the fence connection")
	}
	var one int
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("failed TLS admission changed the runtime session: %v", err)
	}
	if err := FencePostgresRuntimeRoleForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	if err := InspectPostgresRuntimeRoleFenceForCutover(ctx, resolved, resolved.Target, secrets); err != nil {
		t.Fatal(err)
	}
	if err := runtime.QueryRow(ctx, "SELECT 1").Scan(&one); err == nil {
		t.Fatal("runtime TLS session survived the fence")
	}
}
