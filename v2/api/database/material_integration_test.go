package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/internal/pgtest"
)

// Two real PostgreSQL servers: the inherited disposable server (A) and a
// scoped server started by this test (B). Both contain a database with the
// SAME name, reached with the SAME role name; each carries a different
// marker in an identically named schema. Selection is proven by the marker.

const materialCanary = "NORN_DB_MATERIAL_CANARY_4e8f"

type liveServer struct {
	url, host, database, user string
	port                      int
}

func parseLiveServer(t *testing.T, raw string) liveServer {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := liveServer{url: raw, database: strings.TrimPrefix(parsed.Path, "/"), user: parsed.User.Username(), host: parsed.Hostname(), port: 5432}
	if host := parsed.Query().Get("host"); host != "" {
		server.host = host
	}
	if port := parsed.Query().Get("port"); port != "" {
		server.port, _ = strconv.Atoi(port)
	} else if parsed.Port() != "" {
		server.port, _ = strconv.Atoi(parsed.Port())
	}
	return server
}

type twoServers struct {
	a, b     liveServer
	schema   string
	resolver *Resolver
	catalog  Catalog
	secrets  *DirectorySecretSource
	dir      string
}

func writeSecret(t *testing.T, directory, name, password string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"password":%q}`, password)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func twoServerCatalog(a, b liveServer, roleA string) Catalog {
	service := func(id, provider string, server liveServer) DatabaseService {
		return DatabaseService{APIVersion: APIVersion, ID: id, Generation: 1, Purpose: PurposeApplication, Engine: EnginePostgreSQL, EngineVersion: "16",
			ProviderRef: provider, Endpoint: DatabaseEndpoint{Host: server.host, Port: server.port},
			Topology: DatabaseTopology{Mode: TopologyLocalShared, AvailabilityClass: AvailabilitySingleHost}, TLS: DatabaseTLSPolicy{MinimumMode: TLSDisabled},
			Recovery: RecoveryPolicy{Capabilities: []Capability{CapabilityRuntime, CapabilityMigration, CapabilitySnapshot, CapabilityRestore, CapabilityHealth}}}
	}
	return Catalog{
		APIVersion: APIVersion,
		Services:   []DatabaseService{service("server-a", "local:inherited-disposable-server", a), service("server-b", "local:scoped-test-server", b)},
		Bindings: []DatabaseBinding{
			{APIVersion: APIVersion, ID: "target-a", ServiceID: "server-a", Database: a.database, Role: roleA, Generation: 1, CredentialRef: "secret:targets/a", TLS: DatabaseTLS{Mode: TLSDisabled}},
			{APIVersion: APIVersion, ID: "target-b", ServiceID: "server-b", Database: b.database, Role: b.user, Generation: 1, CredentialRef: "secret:targets/b", TLS: DatabaseTLS{Mode: TLSDisabled}},
		},
		Profiles: []DeploymentProfile{{APIVersion: APIVersion, ID: "local-test", Topology: DeploymentTopologyLocal, AvailabilityClass: AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"db-a": "target-a", "db-b": "target-b"}}},
	}
}

func markServer(t *testing.T, databaseURL, schema, marker string) {
	t.Helper()
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	identifier := pgx.Identifier{schema}.Sanitize()
	t.Cleanup(func() {
		_, _ = connection.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE; DROP SCHEMA IF EXISTS `+pgx.Identifier{schema + "_copy"}.Sanitize()+` CASCADE`)
		connection.Close(context.Background())
	})
	if _, err := connection.Exec(ctx, `CREATE SCHEMA `+identifier+`; CREATE TABLE `+identifier+`.marker (value text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO `+identifier+`.marker VALUES ($1)`, marker); err != nil {
		t.Fatal(err)
	}
}

func newTwoServers(t *testing.T) *twoServers {
	t.Helper()
	raw := os.Getenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL")
	if raw == "" {
		t.Skip("NORN_TEST_RECOVERY_TARGET_DATABASE_URL is not set")
	}
	a := parseLiveServer(t, raw)
	scoped := pgtest.Start(t)
	scoped.CreateDatabase(t, a.database)
	b := parseLiveServer(t, scoped.URL(a.database))
	if a.database != b.database {
		t.Fatal("fixture requires identical database names")
	}
	schema := "norn_two_server_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	markServer(t, a.url, schema, "server-a")
	markServer(t, b.url, schema, "server-b")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecret(t, dir, "targets/a", materialCanary)
	writeSecret(t, dir, "targets/b", materialCanary)
	secrets, err := NewDirectorySecretSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	catalog := twoServerCatalog(a, b, a.user)
	resolver, err := NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return &twoServers{a: a, b: b, schema: schema, resolver: resolver, catalog: catalog, secrets: secrets, dir: dir}
}

func (s *twoServers) session(t *testing.T, logical string) *Session {
	t.Helper()
	resolved, err := s.resolver.Resolve(ResolveRequest{DeploymentProfileID: "local-test", Purpose: PurposeApplication, LogicalResourceID: logical})
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenSession(context.Background(), resolved, s.secrets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

// hostileEnvironment points every ambient libpq and pgx setting at server A
// (or at nothing) and plants credential canaries.
func (s *twoServers) hostileEnvironment(t *testing.T) {
	t.Setenv("PGHOST", s.a.host)
	t.Setenv("PGPORT", strconv.Itoa(s.a.port))
	t.Setenv("PGDATABASE", s.a.database)
	t.Setenv("PGUSER", s.a.user)
	t.Setenv("PGPASSWORD", materialCanary+"-ambient")
	t.Setenv("PGSERVICE", "ambient_service")
	t.Setenv("PGSERVICEFILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("PGOPTIONS", "-c search_path=ambient_evil")
	t.Setenv("PGSSLCERT", filepath.Join(t.TempDir(), "missing.crt"))
	t.Setenv("PGCONNECT_TIMEOUT", "1")
	t.Setenv("NORN_DATABASE_URL", "postgres://control:"+materialCanary+"@control/norn")
	t.Setenv("DATABASE_URL", "postgres://app:"+materialCanary+"@wrong/app")
}

func TestSameNamedDatabasesOnTwoServersAreSelectedByDeclaredTarget(t *testing.T) {
	servers := newTwoServers(t)
	requireTool(t, "psql")
	servers.hostileEnvironment(t)
	markerQuery := fmt.Sprintf("select value from %s.marker", pgx.Identifier{servers.schema}.Sanitize())
	for logical, want := range map[string]string{"db-a": "server-a", "db-b": "server-b"} {
		session := servers.session(t, logical)
		probe, err := session.Probe(context.Background())
		if err != nil || probe.Database != servers.a.database {
			t.Fatalf("%s probe = %+v, %v", logical, probe, err)
		}
		if session.config.Password != materialCanary || session.config.Database != servers.a.database {
			t.Fatalf("%s session did not use its own credential and declared database", logical)
		}
		command := session.Command(context.Background(), "psql", "-X", "-At", "-d", session.ServiceArgument(), "-c", markerQuery)
		output, err := command.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != want {
			t.Fatalf("%s psql marker = %q, %v", logical, output, err)
		}
		for _, value := range append(command.Args, command.Env...) {
			if strings.Contains(value, materialCanary) || strings.HasPrefix(value, "PGHOST=") || strings.HasPrefix(value, "PGDATABASE=") ||
				strings.HasPrefix(value, "PGPASSWORD=") || strings.HasPrefix(value, "NORN_") || strings.HasPrefix(value, "DATABASE_URL=") {
				t.Fatalf("%s command exposes ambient or secret value %q", logical, value)
			}
		}
		// Migrations get the connection as a value and as a private file;
		// both select the declared server. The rest of the environment holds
		// no credential and nothing ambient.
		// The URL carries the password, so it is read from the environment
		// and file by an ordinary Node process, never passed as an argument.
		node, client := pgtest.NodeClientScript(t)
		migration := exec.Command("sh", "-c", `"$0" "$1" value "$2" && "$0" "$1" file "$2" && env | grep -v '^DATABASE_URL='`, node, client, markerQuery)
		migration.Env = session.MigrationEnvironment("DATABASE_URL", "DATABASE_URL_FILE")
		output, err = migration.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(output), want+"\n"+want+"\n") || strings.Contains(string(output), materialCanary) || strings.Contains(string(output), "NORN_DATABASE_URL") || strings.Contains(string(output), "PGPASSWORD") {
			t.Fatalf("%s migration environment output = %q, %v", logical, session.Redact(output), err)
		}
		if info, err := os.Stat(filepath.Join(session.directory, "connection.url")); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s connection file mode = %v, %v", logical, info, err)
		}
		// In-process queries also reach the declared server.
		connection, err := pgx.ConnectConfig(context.Background(), session.config.Copy())
		if err != nil {
			t.Fatal(err)
		}
		var marker string
		err = connection.QueryRow(context.Background(), markerQuery).Scan(&marker)
		connection.Close(context.Background())
		if err != nil || marker != want {
			t.Fatalf("%s in-process marker = %q, %v", logical, marker, err)
		}
	}
}

func TestDumpFromOneServerRestoresIntoTheSameNamedDatabaseOnTheOther(t *testing.T) {
	servers := newTwoServers(t)
	requireTool(t, "pg_dump")
	requireTool(t, "pg_restore")
	ctx := context.Background()
	// Verification connections are opened before the ambient environment is
	// made hostile; only the adapter's commands run under it.
	verifyA, err := pgx.Connect(ctx, servers.a.url)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyA.Close(ctx)
	verifyB, err := pgx.Connect(ctx, servers.b.url)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyB.Close(ctx)
	servers.hostileEnvironment(t)
	source := servers.session(t, "db-a")
	target := servers.session(t, "db-b")
	dump := filepath.Join(t.TempDir(), "a.dump")
	if output, err := source.Command(ctx, "pg_dump", "-Fc", "--schema="+servers.schema, "-d", source.ServiceArgument(), "-f", dump).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %s", source.Redact(output))
	}
	// Rename on the way in so B's own marker survives.
	copyDump := filepath.Join(t.TempDir(), "a-copy.sql")
	if output, err := source.Command(ctx, "pg_restore", "-f", copyDump, dump).CombinedOutput(); err != nil {
		t.Fatalf("pg_restore to script: %s", source.Redact(output))
	}
	script, err := os.ReadFile(copyDump)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.ReplaceAll(string(script), servers.schema, servers.schema+"_copy")
	if err := os.WriteFile(copyDump, []byte(renamed), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := target.Command(ctx, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-d", target.ServiceArgument(), "-f", copyDump).CombinedOutput(); err != nil {
		t.Fatalf("restore into B: %s", target.Redact(output))
	}
	check := func(connection *pgx.Conn, query, want string) {
		t.Helper()
		var value string
		if err := connection.QueryRow(ctx, query).Scan(&value); err != nil || value != want {
			t.Fatalf("%s = %q, %v", query, value, err)
		}
	}
	check(verifyB, "select value from "+pgx.Identifier{servers.schema + "_copy"}.Sanitize()+".marker", "server-a")
	check(verifyB, "select value from "+pgx.Identifier{servers.schema}.Sanitize()+".marker", "server-b")
	check(verifyA, "select value from "+pgx.Identifier{servers.schema}.Sanitize()+".marker", "server-a")
	var copiedOnA bool
	if err := verifyA.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, servers.schema+"_copy").Scan(&copiedOnA); err != nil || copiedOnA {
		t.Fatalf("restore reached the wrong server: %v %v", copiedOnA, err)
	}
}

func TestEndpointIsCatalogIdentityAndRotationKeepsTarget(t *testing.T) {
	servers := newTwoServers(t)
	ctx := context.Background()
	first := servers.session(t, "db-a")
	firstTarget := first.Target()

	// Credential-only rotation keeps the target and still reaches server A.
	writeSecret(t, servers.dir, "targets/a.rotated", materialCanary+"-rotated")
	rotated := twoServerCatalog(servers.a, servers.b, servers.a.user)
	rotated.Bindings[0].CredentialRef = "secret:targets/a.rotated"
	if err := ValidateTransition(servers.catalog, rotated); err != nil {
		t.Fatalf("credential rotation transition: %v", err)
	}
	resolver, err := NewResolver(rotated)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "local-test", Purpose: PurposeApplication, LogicalResourceID: "db-a", Expected: &firstTarget})
	if err != nil {
		t.Fatalf("credential rotation repointed or staled the target: %v", err)
	}
	session, err := OpenSession(ctx, resolved, servers.secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		t.Fatal(err)
	}

	// Repointing server-a's endpoint at server B without a generation bump is
	// refused; with a bump, accepted work naming generation 1 is stale.
	repointed := twoServerCatalog(servers.a, servers.b, servers.a.user)
	repointed.Services[0].Endpoint = repointed.Services[1].Endpoint
	var resolverErr *ResolverError
	if err := ValidateTransition(servers.catalog, repointed); !errors.As(err, &resolverErr) || resolverErr.Code != CodeUnsafeTransition {
		t.Fatalf("endpoint repoint without bump = %v", err)
	}
	repointed.Services[0].Generation++
	if err := ValidateTransition(servers.catalog, repointed); err != nil {
		t.Fatalf("bumped endpoint change: %v", err)
	}
	bumped, err := NewResolver(repointed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bumped.Resolve(ResolveRequest{DeploymentProfileID: "local-test", Purpose: PurposeApplication, LogicalResourceID: "db-a", Expected: &firstTarget}); !errors.As(err, &resolverErr) || resolverErr.Code != CodeStaleTarget {
		t.Fatalf("accepted work after endpoint change = %v", err)
	}

	// A declared role the server lacks fails the probe without echoing secrets.
	missingRole := twoServerCatalog(servers.a, servers.b, "norn_missing_role_"+strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	missingResolver, err := NewResolver(missingRole)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = missingResolver.Resolve(ResolveRequest{DeploymentProfileID: "local-test", Purpose: PurposeApplication, LogicalResourceID: "db-a"})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := OpenSession(ctx, resolved, servers.secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err := wrong.Probe(ctx); err == nil || strings.Contains(err.Error(), materialCanary) {
		t.Fatalf("missing role probe = %v", err)
	}
	if redacted := wrong.Redact([]byte("password " + materialCanary + " leaked")); strings.Contains(redacted, materialCanary) {
		t.Fatalf("redaction kept password: %q", redacted)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if text := fmt.Sprintf(verb, wrong); strings.Contains(text, materialCanary) {
			t.Fatalf("%s formatting exposed the credential", verb)
		}
		if text := fmt.Sprintf(verb, *wrong); strings.Contains(text, materialCanary) {
			t.Fatalf("%s value formatting exposed the credential", verb)
		}
	}

	directory := first.directory
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode = %v, %v", info, err)
	}
	service, err := os.Stat(filepath.Join(directory, "pg_service.conf"))
	if err != nil || service.Mode().Perm() != 0o600 {
		t.Fatalf("service file mode = %v, %v", service, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed session left private material on disk")
	}
	if _, err := first.Probe(ctx); err == nil {
		t.Fatal("closed session probed")
	}
}

type literalSecrets map[string]string

func (l literalSecrets) Resolve(_ context.Context, reference string) ([]byte, error) {
	value, ok := l[reference]
	if !ok {
		return nil, errors.New("missing")
	}
	return []byte(value), nil
}

func TestSessionRejectsUnsupportedTargetsAndMalformedSecrets(t *testing.T) {
	secrets := literalSecrets{
		"secret:ok":        `{"password":"` + materialCanary + `"}`,
		"secret:endpoint":  `{"host":"attacker.example","port":5432,"password":"x"}`,
		"secret:user":      `{"user":"postgres","password":"x"}`,
		"secret:duplicate": `{"password":"a","password":"b"}`,
		"secret:trailing":  `{"password":"a"} {"password":"b"}`,
		"secret:array":     `[{"password":"a"}]`,
	}
	base := ResolvedBinding{Target: TargetIdentity{ServiceID: "s", ServiceGeneration: 1, BindingID: "b", BindingGeneration: 1, Engine: EnginePostgreSQL, Database: "app", Role: "app"},
		Purpose: PurposeApplication, CredentialRef: "secret:ok", TLS: DatabaseTLS{Mode: TLSDisabled}, Endpoint: DatabaseEndpoint{Host: "db.internal.example", Port: 5432}}
	for name, test := range map[string]struct {
		mutate func(*ResolvedBinding)
		code   ErrorCode
	}{
		"unknown engine":    {func(r *ResolvedBinding) { r.Target.Engine = "cockroachdb" }, CodeUnsupportedEngine},
		"control purpose":   {func(r *ResolvedBinding) { r.Purpose = PurposeControl }, CodePurposeMismatch},
		"missing secret":    {func(r *ResolvedBinding) { r.CredentialRef = "secret:absent" }, CodeInvalidRequest},
		"secret endpoint":   {func(r *ResolvedBinding) { r.CredentialRef = "secret:endpoint" }, CodeInvalidRequest},
		"secret user":       {func(r *ResolvedBinding) { r.CredentialRef = "secret:user" }, CodeInvalidRequest},
		"duplicate key":     {func(r *ResolvedBinding) { r.CredentialRef = "secret:duplicate" }, CodeInvalidRequest},
		"trailing document": {func(r *ResolvedBinding) { r.CredentialRef = "secret:trailing" }, CodeInvalidRequest},
		"array":             {func(r *ResolvedBinding) { r.CredentialRef = "secret:array" }, CodeInvalidRequest},
		"no endpoint":       {func(r *ResolvedBinding) { r.Endpoint = DatabaseEndpoint{} }, CodeInvalidRequest},
		"server name": {func(r *ResolvedBinding) {
			r.TLS = DatabaseTLS{Mode: TLSVerifyFull, ServerName: "other.example", CARef: "secret:ok"}
		}, CodeInvalidRequest},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := base
			test.mutate(&resolved)
			session, err := OpenSession(context.Background(), resolved, secrets)
			if session != nil {
				session.Close()
			}
			var resolverErr *ResolverError
			if !errors.As(err, &resolverErr) || resolverErr.Code != test.code || strings.Contains(err.Error(), materialCanary) {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestDirectorySecretSourceIsPrivateAndConfined(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shared"), []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	source, err := NewDirectorySecretSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Resolve(context.Background(), "secret:ok"); err != nil {
		t.Fatalf("private secret rejected: %v", err)
	}
	for _, reference := range []string{"secret:shared", "secret:escape", "secret:../outside", "secret:missing", "file:ok", "secret:/etc/passwd"} {
		if _, err := source.Resolve(context.Background(), reference); err == nil {
			t.Fatalf("reference %q was resolved", reference)
		}
	}
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectorySecretSource(open); err == nil {
		t.Fatal("group-readable secret directory accepted")
	}
	if _, err := NewDirectorySecretSource("relative"); err == nil {
		t.Fatal("relative secret directory accepted")
	}
}
