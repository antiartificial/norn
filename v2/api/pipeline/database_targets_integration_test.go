package pipeline

import (
	"context"
	"encoding/json"
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

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// The control store lives in an isolated schema of NORN_TEST_DATABASE_URL.
// The application target is a different disposable database on the SAME
// server (NORN_TEST_RECOVERY_TARGET_DATABASE_URL), exercised only inside an
// isolated schema. This proves identity-based selection and control/app
// separation; it does not claim two servers. The two-server pipeline proof
// is TestDatabaseTargetsRouteToDeclaredServerNotAmbientOne, which points the
// catalog at a scoped second server holding a same-named database.

const targetCanary = "NORN_DB_TARGET_CANARY_19ac"

type targetFixture struct {
	p          *Pipeline
	db         *store.DB
	request    EnqueueRequest
	app        string
	appSchema  string
	appDB      *pgx.Conn
	appDBName  string
	appRole    string
	appHost    string
	appPort    int
	catalog    database.Catalog
	snapshots  string
	controlDB  string
	secretDir  string
	requestSeq int
}

func newTargetFixture(t *testing.T) *targetFixture {
	t.Helper()
	targetURL := os.Getenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL")
	if targetURL == "" {
		t.Skip("NORN_TEST_RECOVERY_TARGET_DATABASE_URL is not set")
	}
	for _, tool := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	p, db, request := acceptancePipelineFixture(t)
	ctx := context.Background()
	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port := parsed.Query().Get("host"), 5432
	if host == "" {
		host = parsed.Hostname()
	}
	if value := parsed.Query().Get("port"); value != "" {
		port, _ = strconv.Atoi(value)
	} else if parsed.Port() != "" {
		port, _ = strconv.Atoi(parsed.Port())
	}
	appDBName := strings.TrimPrefix(parsed.Path, "/")
	var controlDB string
	if err := db.Pool.QueryRow(ctx, `SELECT current_database()`).Scan(&controlDB); err != nil {
		t.Fatal(err)
	}
	if controlDB == appDBName {
		t.Skip("control and application disposable URLs name the same database")
	}
	appDB, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		t.Fatal(err)
	}
	appSchema := "norn_app_target_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	identifier := pgx.Identifier{appSchema}.Sanitize()
	t.Cleanup(func() {
		_, _ = appDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`)
		appDB.Close(context.Background())
	})
	if _, err := appDB.Exec(ctx, `CREATE SCHEMA `+identifier+`; CREATE TABLE `+identifier+`.orders (id int, state text); INSERT INTO `+identifier+`.orders VALUES (1, 'original')`); err != nil {
		t.Fatal(err)
	}

	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := fmt.Sprintf(`{"password":%q}`, targetCanary)
	if err := os.WriteFile(filepath.Join(secretDir, "legacy"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := database.NewDirectorySecretSource(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })

	app := "orders-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	appsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appsDir, app), 0o755); err != nil {
		t.Fatal(err)
	}
	// The migration writes where DATABASE_URL points and fails if any control
	// secret or ambient routing leaked into its environment.
	migration := fmt.Sprintf(`test -z "$NORN_DATABASE_URL" && test -z "$PGDATABASE" && psql -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -c "CREATE TABLE %s.migrated AS SELECT current_database() AS db"`, identifier)
	spec := fmt.Sprintf("name: %s\ndeploy: true\nprocesses:\n  web:\n    command: ./web\ninfrastructure:\n  postgres:\n    database: %s\nmigrations: %q\n", app, appDBName, migration)
	if err := os.WriteFile(filepath.Join(appsDir, app, "infraspec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	p.AppsDir = appsDir
	snapshots := t.TempDir()
	p.DatabaseTargets = &DatabaseTargets{
		ProfileID: "mini-local", Catalog: db.ActiveDatabaseCatalog, Secrets: secrets, SnapshotRoot: snapshots,
		dumpScope: []string{"--schema=" + appSchema},
	}
	// Ambient routing and control secrets that must never reach app tools.
	t.Setenv("NORN_DATABASE_URL", "postgres://norn:"+targetCanary+"@control/norn")
	t.Setenv("PGDATABASE", controlDB)
	catalog := database.Catalog{
		APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "mini-app-pg", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL,
			EngineVersion: "16", ProviderRef: "local:disposable-database-b", Endpoint: database.DatabaseEndpoint{Host: host, Port: port}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityMigration, database.CapabilitySnapshot, database.CapabilityRestore, database.CapabilityHealth}}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini-local", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			LegacyPostgres: &database.LegacyPostgresDefault{MappingID: "mini-legacy-pg", ServiceID: "mini-app-pg", Role: parsed.User.Username(), Generation: 1, CredentialRef: "secret:legacy", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}}},
	}
	return &targetFixture{p: p, db: db, request: request, app: app, appSchema: appSchema, appDB: appDB, appDBName: appDBName, appRole: parsed.User.Username(), appHost: host, appPort: port, catalog: catalog, snapshots: snapshots, controlDB: controlDB, secretDir: secretDir}
}

func (f *targetFixture) queue(t *testing.T, kind string, payload map[string]interface{}) (model.Operation, error) {
	t.Helper()
	f.requestSeq++
	request := f.request
	request.Key = fmt.Sprintf("target-%s-%d", kind, f.requestSeq)
	operation := model.Operation{ID: uuid.NewString(), Kind: kind, App: f.app, SagaID: uuid.NewString(), Status: model.OperationQueued, Source: "control-api", Payload: payload, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	accepted, err := f.p.QueueOperation(context.Background(), operation, request)
	return accepted.Operation, err
}

func (f *targetFixture) execute(t *testing.T, operationID string) (*OperationResult, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, operationID); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "target-worker", 60_000_000_000, []string{"app.snapshot", "app.snapshot-restore", "app.snapshot-prune", "app.migrate", DatabaseBaselineKind})
	if err != nil || claimed == nil || claimed.ID != operationID {
		t.Fatalf("claim %s = %+v, %v", operationID, claimed, err)
	}
	return f.p.ExecuteOperation(ctx, claimed, claim)
}

func (f *targetFixture) orderState(t *testing.T) string {
	t.Helper()
	var state string
	if err := f.appDB.QueryRow(context.Background(), `SELECT state FROM `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDatabaseTargetsBindAcceptanceAndDriveSnapshotRestoreMigration(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()

	// A profile without an active catalog refuses acceptance outright.
	if _, err := f.queue(t, "app.snapshot", map[string]interface{}{}); err == nil {
		t.Fatal("database work accepted without an active catalog")
	}
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}

	// Snapshot: the full target tuple is signed into the payload.
	snapshotOp, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := recordedTargetFromPayload(snapshotOp.Payload)
	if err != nil || recorded == nil || recorded.CatalogRevision != 1 || !recorded.Legacy ||
		recorded.Target != (database.TargetIdentity{ServiceID: "mini-app-pg", ServiceGeneration: 1, BindingID: "mini-legacy-pg", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: f.appDBName, Role: f.catalog.Profiles[0].LegacyPostgres.Role}) {
		t.Fatalf("recorded target = %+v, %v", recorded, err)
	}
	result, err := f.execute(t, snapshotOp.ID)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("snapshot = %+v, %v", result, err)
	}
	snapshotFile, _ := result.Metadata["snapshot"].(string)
	sidecar, err := readSidecar(snapshotLocation{dir: f.snapshots}, snapshotFile)
	if err != nil || sidecar == nil || sidecar.Target != recorded.Target || sidecar.CatalogRevision != 1 {
		t.Fatalf("snapshot sidecar = %+v, %v", sidecar, err)
	}

	// Restore: data changed after the snapshot returns to the snapshot state.
	if _, err := f.appDB.Exec(ctx, `UPDATE `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` SET state='mutated'`); err != nil {
		t.Fatal(err)
	}
	restoreOp, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"snapshot": snapshotFile})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, restoreOp.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("restore = %+v, %v", result, err)
	}
	if state := f.orderState(t); state != "original" {
		t.Fatalf("restored state = %q", state)
	}

	// A snapshot bound to another target identity is refused, even with a
	// matching database-name prefix in the same namespace.
	foreignFile := strings.Replace(snapshotFile, "_manual_", "_foreign_", 1)
	data, err := os.ReadFile(filepath.Join(f.snapshots, snapshotFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.snapshots, foreignFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := *sidecar
	foreign.Target.ServiceID = "other-server-pg"
	encoded, _ := json.Marshal(foreign)
	if err := os.WriteFile(filepath.Join(f.snapshots, foreignFile+sidecarSuffix), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.appDB.Exec(ctx, `UPDATE `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` SET state='kept'`); err != nil {
		t.Fatal(err)
	}
	foreignOp, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"snapshot": foreignFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, foreignOp.ID); !errors.Is(err, errSnapshotTargetMismatch) {
		t.Fatalf("foreign restore = %v", err)
	}
	// Tampered bytes under a matching sidecar are refused too.
	tampered := strings.Replace(snapshotFile, "_manual_", "_tampered_", 1)
	if err := os.WriteFile(filepath.Join(f.snapshots, tampered), append(append([]byte(nil), data...), 0), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(sidecar)
	if err := os.WriteFile(filepath.Join(f.snapshots, tampered+sidecarSuffix), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedOp, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"snapshot": tampered})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, tamperedOp.ID); err == nil || !strings.Contains(err.Error(), "differs from its target sidecar") {
		t.Fatalf("tampered restore = %v", err)
	}
	if state := f.orderState(t); state != "kept" {
		t.Fatalf("refused restores changed data: %q", state)
	}

	// Migration: runs against the recorded target with no control DSN or
	// ambient routing in its environment.
	migrateOp, err := f.queue(t, "app.migrate", map[string]interface{}{"ref": ""})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, migrateOp.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("migrate = %+v, %v", result, err)
	}
	var migratedDB string
	if err := f.appDB.QueryRow(ctx, `SELECT db FROM `+pgx.Identifier{f.appSchema, "migrated"}.Sanitize()).Scan(&migratedDB); err != nil || migratedDB != f.appDBName {
		t.Fatalf("migration landed in %q, %v", migratedDB, err)
	}
	var controlCopy bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, f.appSchema).Scan(&controlCopy); err != nil || controlCopy {
		t.Fatalf("migration touched the control database: %v %v", controlCopy, err)
	}

	// No persisted result, metadata or saga event carries the credential.
	var leaked int
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operations WHERE payload::text LIKE $1 OR metadata::text LIKE $1 OR message LIKE $1 OR last_error LIKE $1)
		+ (SELECT count(*) FROM saga_events WHERE message LIKE $1 OR metadata::text LIKE $1)`, "%"+targetCanary+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("credential canary persisted %d times (%v)", leaked, err)
	}
}

func TestDatabaseTargetsRejectStaleGenerationAndUnboundWork(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	// Credential-only rotation keeps the accepted target valid.
	rotated := f.catalog
	legacy := *f.catalog.Profiles[0].LegacyPostgres
	secret, err := os.ReadFile(filepath.Join(f.secretDir, "legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.secretDir, "legacy-rotated"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy.CredentialRef = "secret:legacy-rotated"
	rotated.Profiles = []database.DeploymentProfile{f.catalog.Profiles[0]}
	rotated.Profiles[0].LegacyPostgres = &legacy
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, rotated, "operator"); err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, accepted.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("execution after credential rotation = %+v, %v", result, err)
	}

	// A target generation bump fences work accepted under the old generation.
	stale, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	bumped := rotated
	bumpedLegacy := legacy
	bumpedLegacy.Generation = 2
	bumped.Profiles = []database.DeploymentProfile{rotated.Profiles[0]}
	bumped.Profiles[0].LegacyPostgres = &bumpedLegacy
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 2, bumped, "operator"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(f.snapshots)
	var resolverErr *database.ResolverError
	if _, err := f.execute(t, stale.ID); !errors.As(err, &resolverErr) || resolverErr.Code != database.CodeStaleTarget {
		t.Fatalf("stale generation execution = %v", err)
	}
	after, _ := os.ReadDir(f.snapshots)
	if len(after) != len(before) {
		t.Fatal("stale target execution produced a snapshot")
	}
	// New work binds the new generation and runs.
	fresh, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if recorded, _ := recordedTargetFromPayload(fresh.Payload); recorded == nil || recorded.Target.BindingGeneration != 2 || recorded.CatalogRevision != 3 {
		t.Fatalf("fresh binding = %+v", recorded)
	}
	if result, err := f.execute(t, fresh.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("fresh execution = %+v, %v", result, err)
	}

	// Work that reached the queue without a recorded target is never routed.
	unbound := &model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: f.app, SagaID: uuid.NewString(), Status: model.OperationQueued, Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	if err := f.db.InsertOperation(ctx, unbound); err != nil {
		t.Fatal(err)
	}
	var targetErr *DatabaseTargetError
	if _, err := f.execute(t, unbound.ID); !errors.As(err, &targetErr) {
		t.Fatalf("unbound execution = %v", err)
	}
	// And recorded work is refused by a process with no database profile.
	targets := f.p.DatabaseTargets
	f.p.DatabaseTargets = nil
	recordedOnly, err := func() (model.Operation, error) {
		f.p.DatabaseTargets = targets
		defer func() { f.p.DatabaseTargets = nil }()
		return f.queue(t, "app.snapshot", map[string]interface{}{})
	}()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, recordedOnly.ID); !errors.As(err, &targetErr) {
		t.Fatalf("recorded work without a profile = %v", err)
	}
	f.p.DatabaseTargets = targets
}
