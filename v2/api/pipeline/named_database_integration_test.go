package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
)

const namedCanary = "NORN_NAMED_CANARY_c41"

type namedFixture struct {
	*targetFixture
	primary, analytics *pgtest.Server
	spec               *model.InfraSpec
}

// newNamedFixture declares two named databases with the SAME database and
// role names on two scoped servers (primary on B, analytics on A), plus a
// MySQL-backed "reports" database that has no adapter.
func newNamedFixture(t *testing.T) *namedFixture {
	t.Helper()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	p, db, request := acceptancePipelineFixture(t)
	servers := map[string]*pgtest.Server{}
	for _, name := range []string{"primary", "analytics"} {
		server := pgtest.Start(t)
		server.CreateDatabase(t, "shop")
		server.CreatePasswordRole(t, "shop_app", namedCanary, "shop")
		server.Exec(t, "shop", fmt.Sprintf(`CREATE TABLE orders (id int, state text); INSERT INTO orders VALUES (1, '%s-original'); ALTER TABLE orders OWNER TO shop_app`, name))
		servers[name] = server
	}
	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "shop"), []byte(fmt.Sprintf(`{"password":%q}`, namedCanary)), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := database.NewDirectorySecretSource(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })

	app := "named-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	appsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appsDir, app), 0o755); err != nil {
		t.Fatal(err)
	}
	specText := fmt.Sprintf(`schemaVersion: norn.app/v2
name: %s
deploy: true
processes:
  web:
    command: node server.js
databases:
  - name: primary
    purpose: application
    capabilities: [runtime, snapshot, restore, health]
    runtime: {env: DATABASE_URL}
  - name: analytics
    purpose: application
    capabilities: [snapshot, restore, health]
  - name: reports
    purpose: application
    capabilities: [health]
`, app)
	if err := os.WriteFile(filepath.Join(appsDir, app, "infraspec.yaml"), []byte(specText), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appsDir, app, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.AppsDir = appsDir
	snapshots := t.TempDir()
	p.DatabaseTargets = &DatabaseTargets{ProfileID: "mini", Catalog: db.ActiveDatabaseCatalog, Secrets: secrets, SnapshotRoot: snapshots}
	// The named-database tests exercise snapshot execution, so give this
	// disposable PostgreSQL fixture the same contained test supervisor used by
	// the target-binding suite. Production admission still requires its own
	// qualified Linux backend.
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Fatal(err)
	}
	pgDump, err = filepath.EvalSymlinks(pgDump)
	if err != nil {
		t.Fatal(err)
	}
	pgDumpBytes, err := os.ReadFile(pgDump)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pgDumpBytes)
	backend := newPortableSnapshotBackend()
	manager, err := supervisor.NewManager(t.TempDir(), backend.key, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(2 * supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	p.SnapshotEffects, err = NewSnapshotEffects(db, manager, pgDump, hex.EncodeToString(digest[:]), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := func(id string, engine database.Engine, endpoint database.DatabaseEndpoint) database.DatabaseService {
		return database.DatabaseService{APIVersion: database.APIVersion, ID: id, Generation: 1, Purpose: database.PurposeApplication, Engine: engine,
			EngineVersion: "16", ProviderRef: "local:" + id, Endpoint: endpoint,
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot, database.CapabilityRestore, database.CapabilityHealth}}}
	}
	binding := func(id, serviceID string) database.DatabaseBinding {
		return database.DatabaseBinding{APIVersion: database.APIVersion, ID: id, ServiceID: serviceID, Database: "shop", Role: "shop_app", Generation: 1, CredentialRef: "secret:shop", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}
	}
	mysql := service("reports-mysql", database.EngineMySQL, database.DatabaseEndpoint{Host: "127.0.0.1", Port: 3306})
	mysql.EngineVersion = "8.0"
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{
			service("pg-b", database.EnginePostgreSQL, database.DatabaseEndpoint{Host: servers["primary"].SocketDir, Port: servers["primary"].Port}),
			service("pg-a", database.EnginePostgreSQL, database.DatabaseEndpoint{Host: servers["analytics"].SocketDir, Port: servers["analytics"].Port}),
			mysql,
		},
		Bindings: []database.DatabaseBinding{binding("shop-primary", "pg-b"), binding("shop-analytics", "pg-a"), binding("shop-reports", "reports-mysql")},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"primary": "shop-primary", "analytics": "shop-analytics", "reports": "shop-reports"}}},
	}
	if _, err := db.ActivateDatabaseCatalog(context.Background(), 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	base := &targetFixture{p: p, db: db, request: request, app: app, catalog: catalog, snapshots: snapshots, secretDir: secretDir}
	return &namedFixture{targetFixture: base, primary: servers["primary"], analytics: servers["analytics"], spec: spec}
}

// dirObjectStore is a local-directory object store with the storage
// client's PutObject/GetObject file contract.
type dirObjectStore struct {
	root      string
	puts      int
	beforePut func(key string)
}

func (s *dirObjectStore) path(bucket, key string) string { return filepath.Join(s.root, bucket, key) }

func (s *dirObjectStore) PutObject(_ context.Context, bucket, key, filePath string) error {
	if s.beforePut != nil {
		s.beforePut(key)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path(bucket, key)), 0o700); err != nil {
		return err
	}
	s.puts++
	return os.WriteFile(s.path(bucket, key), data, 0o600)
}

func (s *dirObjectStore) GetObject(_ context.Context, bucket, key, destPath string) error {
	data, err := os.ReadFile(s.path(bucket, key))
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o600)
}

func (s *dirObjectStore) read(t *testing.T, bucket, key string) []byte {
	t.Helper()
	data, err := os.ReadFile(s.path(bucket, key))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (s *dirObjectStore) write(t *testing.T, bucket, key string, data []byte) {
	t.Helper()
	if err := os.WriteFile(s.path(bucket, key), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func orderStateOn(t *testing.T, server *pgtest.Server) string {
	t.Helper()
	connection, err := pgx.Connect(context.Background(), server.URL("shop"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	var state string
	if err := connection.QueryRow(context.Background(), `SELECT state FROM orders WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func setOrderState(t *testing.T, server *pgtest.Server, state string) {
	t.Helper()
	server.Exec(t, "shop", fmt.Sprintf(`UPDATE orders SET state='%s'`, state))
}

func TestNamedDatabasesSnapshotRestoreInventoryExportAndHealthAreTargetBound(t *testing.T) {
	f := newNamedFixture(t)
	ctx := context.Background()

	// Several databases: an unselected snapshot is refused at acceptance,
	// and a selection must be a declared snapshot-capable database.
	var targetErr *DatabaseTargetError
	if _, err := f.queue(t, "app.snapshot", map[string]interface{}{}); !errors.As(err, &targetErr) {
		t.Fatalf("ambiguous snapshot = %v", err)
	}
	if _, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "reports"}); !errors.As(err, &targetErr) {
		t.Fatalf("snapshot of a database without the capability = %v", err)
	}
	for _, name := range []string{"primary", "analytics"} {
		op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": name})
		if err != nil {
			t.Fatal(err)
		}
		set, err := recordedTargetSetFromPayload(op.Payload)
		if err != nil || set == nil || len(set.Targets) != 1 || set.Targets[0].Name != name {
			t.Fatalf("%s recorded set = %+v, %v", name, set, err)
		}
		if result, err := f.execute(t, op.ID); err != nil || result.Status != model.OperationSucceeded || result.Metadata["logicalDatabase"] != name {
			t.Fatalf("%s snapshot = %+v, %v", name, result, err)
		}
	}

	// Inventory: one group per snapshot-capable database, each only listing
	// its own target's dump in its own namespace.
	groups, err := f.p.TargetSnapshots(ctx, f.spec)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	files := map[string]string{}
	for _, group := range groups {
		if group.Unavailable != "" || len(group.Snapshots) != 1 || group.DatabaseName != "shop" || group.Snapshots[0].Provenance != "sidecar" {
			t.Fatalf("group %+v", group)
		}
		files[group.Database] = group.Snapshots[0].Filename
		if group.BindingID != "shop-"+group.Database {
			t.Fatalf("group %s binding = %s", group.Database, group.BindingID)
		}
	}

	// Restore is confined to the selected database's namespace: the
	// analytics dump name is not restorable as primary.
	setOrderState(t, f.primary, "primary-mutated")
	setOrderState(t, f.analytics, "analytics-mutated")
	resolvedAnalytics, err := f.p.inventoryLocation(ctx, f.spec, "analytics")
	if err != nil {
		t.Fatal(err)
	}
	onlyInAnalytics := "shop_manual_20990101T000000.dump"
	for _, suffix := range []string{"", sidecarSuffix} {
		data, err := os.ReadFile(filepath.Join(resolvedAnalytics.dir, files["analytics"]+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(resolvedAnalytics.dir, onlyInAnalytics+suffix), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	crossed, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"database": "primary", "snapshot": onlyInAnalytics})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, crossed.ID); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("restore of another database's dump into primary = %v", err)
	}
	if state := orderStateOn(t, f.primary); state != "primary-mutated" {
		t.Fatalf("refused cross-namespace restore changed primary: %q", state)
	}
	restore, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"database": "primary", "snapshot": files["primary"]})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, restore.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("primary restore = %+v, %v", result, err)
	}
	if state := orderStateOn(t, f.primary); state != "primary-original" {
		t.Fatalf("primary after restore = %q", state)
	}
	if state := orderStateOn(t, f.analytics); state != "analytics-mutated" {
		t.Fatalf("restore reached the same-named database on the other server: %q", state)
	}

	// Export → loss → import → durable restore, with real bytes.
	analyticsLocation, err := f.p.inventoryLocation(ctx, f.spec, "analytics")
	if err != nil {
		t.Fatal(err)
	}
	analyticsPath := filepath.Join(analyticsLocation.dir, files["analytics"])
	original, err := os.ReadFile(analyticsPath)
	if err != nil {
		t.Fatal(err)
	}
	objects := &dirObjectStore{root: t.TempDir()}
	// The namespace file is replaced while the upload runs: the uploaded
	// bytes must still be the verified ones.
	objects.beforePut = func(key string) {
		if !strings.HasSuffix(key, snapshotManifestSuffix) {
			if err := os.WriteFile(analyticsPath, []byte("replaced during upload"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest, key, err := f.p.ExportTargetSnapshot(ctx, f.spec, "analytics", files["analytics"], objects, "exports")
	objects.beforePut = nil
	if err != nil || !strings.HasPrefix(key, "snapshots/"+f.app+"/databases/analytics/") {
		t.Fatalf("export = %+v %s, %v", manifest, key, err)
	}
	exported := objects.read(t, "exports", key)
	sum := sha256.Sum256(exported)
	if string(exported) != string(original) || hex.EncodeToString(sum[:]) != manifest.SHA256 || manifest.Size != int64(len(original)) {
		t.Fatal("uploaded bytes are not the verified snapshot bytes")
	}
	var stored SnapshotExportManifest
	if err := json.Unmarshal(objects.read(t, "exports", key+snapshotManifestSuffix), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Target != (database.TargetIdentity{ServiceID: "pg-a", ServiceGeneration: 1, BindingID: "shop-analytics", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop_app"}) ||
		stored.CatalogRevision != 1 || stored.SHA256 != manifest.SHA256 || stored.Database != "analytics" || stored.Provenance != "sidecar" {
		t.Fatalf("stored manifest = %+v", stored)
	}
	if _, _, err := f.p.ExportTargetSnapshot(ctx, f.spec, "", "", objects, "exports"); !errors.As(err, &targetErr) {
		t.Fatalf("ambiguous export = %v", err)
	}
	// The replaced namespace file no longer matches its sidecar: export and
	// restore refuse it, and nothing is uploaded.
	puts := objects.puts
	if _, _, err := f.p.ExportTargetSnapshot(ctx, f.spec, "analytics", files["analytics"], objects, "exports"); err == nil || objects.puts != puts {
		t.Fatalf("tampered export = %v (uploads %d)", err, objects.puts-puts)
	}
	// A symlink in the namespace is never followed.
	if err := os.Remove(analyticsPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.dump")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, analyticsPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.p.ExportTargetSnapshot(ctx, f.spec, "analytics", files["analytics"], objects, "exports"); err == nil || objects.puts != puts {
		t.Fatalf("symlinked export = %v", err)
	}
	// Loss: the dump and its provenance are gone locally.
	_ = os.Remove(analyticsPath)
	_ = os.Remove(analyticsPath + sidecarSuffix)
	// Imports that do not match the current target or their manifest fail.
	manifestKey := key + snapshotManifestSuffix
	goodManifest := objects.read(t, "exports", manifestKey)
	foreignManifest := stored
	foreignManifest.Target.BindingGeneration = 9
	objects.write(t, "exports", manifestKey, mustJSON(t, foreignManifest))
	if _, err := f.p.ImportTargetSnapshot(ctx, f.spec, "analytics", objects, "exports", key); !errors.Is(err, errSnapshotTargetMismatch) {
		t.Fatalf("foreign-target import = %v", err)
	}
	objects.write(t, "exports", manifestKey, goodManifest)
	objects.write(t, "exports", key, append([]byte("x"), original...))
	if _, err := f.p.ImportTargetSnapshot(ctx, f.spec, "analytics", objects, "exports", key); err == nil {
		t.Fatal("import accepted bytes that differ from the manifest")
	}
	objects.write(t, "exports", key, original)
	if _, err := os.Lstat(analyticsPath); !os.IsNotExist(err) {
		t.Fatal("a refused import left a file behind")
	}
	// Recovery: import republishes the dump with its provenance, and the
	// ordinary durable restore brings the database back.
	imported, err := f.p.ImportTargetSnapshot(ctx, f.spec, "analytics", objects, "exports", key)
	if err != nil || imported.Filename != files["analytics"] {
		t.Fatalf("import = %+v, %v", imported, err)
	}
	sidecar, err := readSidecar(analyticsLocation, files["analytics"])
	if err != nil || sidecar == nil || sidecar.Target != stored.Target || sidecar.SHA256 != stored.SHA256 || sidecar.CatalogRevision != 1 {
		t.Fatalf("imported sidecar = %+v, %v", sidecar, err)
	}
	if _, err := f.p.ImportTargetSnapshot(ctx, f.spec, "analytics", objects, "exports", key); err != nil {
		t.Fatalf("idempotent re-import = %v", err)
	}
	recover, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"database": "analytics", "snapshot": files["analytics"]})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, recover.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("restore of imported snapshot = %+v, %v", result, err)
	}
	if state := orderStateOn(t, f.analytics); state != "analytics-original" {
		t.Fatalf("analytics after import+restore = %q", state)
	}
	if state := orderStateOn(t, f.primary); state != "primary-original" {
		t.Fatalf("import/restore reached primary: %q", state)
	}

	// Health: both PostgreSQL targets prove their identity. This fixture has
	// no MySQL server, so its MySQL probe fails without exposing credentials.
	health, err := f.p.DatabaseHealth(ctx, f.spec)
	if err != nil || len(health) != 3 {
		t.Fatalf("health = %+v, %v", health, err)
	}
	for _, entry := range health {
		switch entry.Database {
		case "primary", "analytics":
			if entry.Status != "ok" || entry.ServerVersion == "" || entry.BindingID != "shop-"+entry.Database {
				t.Fatalf("%s health = %+v", entry.Database, entry)
			}
		case "reports":
			if entry.Status != "failed" || entry.Engine != "mysql" {
				t.Fatalf("reports health = %+v", entry)
			}
		}
		if strings.Contains(fmt.Sprintf("%+v", entry), namedCanary) {
			t.Fatal("health leaked the credential")
		}
	}
	withoutHealth := *f.spec
	withoutHealth.Databases = append([]model.DatabaseRequirement(nil), f.spec.Databases...)
	for i := range withoutHealth.Databases {
		if withoutHealth.Databases[i].Name == "reports" {
			withoutHealth.Databases[i].Capabilities = nil
		}
	}
	withoutHealthResult, err := f.p.DatabaseHealth(ctx, &withoutHealth)
	if err != nil || len(withoutHealthResult) != 3 || withoutHealthResult[2].Database != "reports" || withoutHealthResult[2].Status != "unsupported" {
		t.Fatalf("undeclared health capability was probed: %+v, %v", withoutHealthResult, err)
	}
	// A wrong credential is a failed probe with only a SQLSTATE.
	if err := os.WriteFile(filepath.Join(f.secretDir, "shop"), []byte(`{"password":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	health, _ = f.p.DatabaseHealth(ctx, f.spec)
	for _, entry := range health {
		if entry.Database == "primary" && (entry.Status != "failed" || !strings.Contains(entry.Detail, "SQLSTATE 28P01")) {
			t.Fatalf("wrong-credential health = %+v", entry)
		}
	}

	// No persisted operation or saga record carries the credential.
	var leaked int
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operations WHERE payload::text LIKE $1 OR metadata::text LIKE $1 OR message LIKE $1 OR last_error LIKE $1)
		+ (SELECT count(*) FROM saga_events WHERE message LIKE $1 OR metadata::text LIKE $1)`, "%"+namedCanary+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("credential persisted %d times (%v)", leaked, err)
	}
}

// A legacy-to-named transition has no recorded writer targets: deploys are
// refused as ambiguous until an audited baseline is accepted and executed,
// which probes the targets. A baseline may not contradict recorded targets.
func TestDatabaseBaselineResolvesLegacyToNamedTransition(t *testing.T) {
	f := newNamedFixture(t)
	ctx := context.Background()
	legacy := &model.Operation{ID: uuid.NewString(), App: f.app, Kind: "app.deploy", Status: model.OperationSucceeded, SagaID: uuid.NewString(),
		Payload: map[string]interface{}{"app": f.app}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	if err := f.db.InsertCompletedOperation(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	var targetErr *DatabaseTargetError
	if _, err := f.queue(t, "app.deploy", map[string]interface{}{}); !errors.As(err, &targetErr) || !targetErr.Ambiguous {
		t.Fatalf("named deploy over legacy history = %v", err)
	}
	baseline, err := f.queue(t, DatabaseBaselineKind, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	set, err := recordedTargetSetFromPayload(baseline.Payload)
	if err != nil || set == nil || len(set.Targets) != 1 || set.Targets[0].Name != "primary" {
		t.Fatalf("baseline records only writer databases: %+v, %v", set, err)
	}
	result, err := f.execute(t, baseline.ID)
	if err != nil || result.Status != model.OperationSucceeded || result.Metadata["probed"] != true {
		t.Fatalf("baseline execution = %+v, %v", result, err)
	}
	if err := f.db.FinishClaimedOperation(ctx, result.Claim, result.Status, result.Message, result.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue(t, "app.deploy", map[string]interface{}{}); err != nil {
		t.Fatalf("named deploy after a baseline = %v", err)
	}
	// Re-baselining primary onto another target is a move, not a baseline.
	moved := f.catalog
	moved.Bindings = append([]database.DatabaseBinding(nil), f.catalog.Bindings...)
	moved.Bindings[0].ServiceID, moved.Bindings[0].Generation = "reports-mysql", 2
	moved.Bindings[0].Database = "shop2"
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, moved, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue(t, DatabaseBaselineKind, map[string]interface{}{}); !errors.As(err, &targetErr) || targetErr.Ambiguous {
		t.Fatalf("contradicting baseline = %v", err)
	}
}

// Named databases never fall back to ambient routing: without a profile
// they are refused at acceptance and at execution.
func TestNamedDatabasesRequireADatabaseProfile(t *testing.T) {
	f := newNamedFixture(t)
	targets := f.p.DatabaseTargets
	f.p.DatabaseTargets = nil
	var targetErr *DatabaseTargetError
	if _, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"}); !errors.As(err, &targetErr) {
		t.Fatalf("acceptance without a profile = %v", err)
	}
	f.p.DatabaseTargets = targets
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	f.p.DatabaseTargets = nil
	if _, err := f.execute(t, op.ID); !errors.As(err, &targetErr) {
		t.Fatalf("execution without a profile = %v", err)
	}
	if groups, err := f.p.TargetSnapshots(context.Background(), f.spec); err == nil || groups != nil {
		t.Fatal("inventory without a profile")
	}
}

func TestMySQLTLSRuntimeIsRejectedBeforeDeployAcceptance(t *testing.T) {
	f := newNamedFixture(t)
	path := filepath.Join(f.p.AppsDir, f.app, "infraspec.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), "    capabilities: [health]\n", `    capabilities: [runtime, health]
    runtime:
      components:
        host: WORDPRESS_DB_HOST
        user: WORDPRESS_DB_USER
        password: WORDPRESS_DB_PASSWORD
        name: WORDPRESS_DB_NAME
`, 1)
	if updated == string(raw) {
		t.Fatal("MySQL fixture declaration was not replaced")
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	tlsCatalog := f.catalog
	tlsCatalog.Services = append([]database.DatabaseService(nil), f.catalog.Services...)
	tlsCatalog.Bindings = append([]database.DatabaseBinding(nil), f.catalog.Bindings...)
	tlsCatalog.Services[2].Generation++
	tlsCatalog.Services[2].TLS.MinimumMode = database.TLSVerifyFull
	tlsCatalog.Bindings[2].Generation++
	tlsCatalog.Bindings[2].TLS = database.DatabaseTLS{Mode: database.TLSVerifyFull, ServerName: "127.0.0.1", CARef: "secret:mysql/ca"}
	if _, err := f.db.ActivateDatabaseCatalog(context.Background(), 1, tlsCatalog, "operator"); err != nil {
		t.Fatal(err)
	}
	_, err = f.queue(t, "app.deploy", map[string]interface{}{})
	var resolverErr *database.ResolverError
	if !errors.As(err, &resolverErr) || resolverErr.Code != database.CodeUnsupportedCapability {
		t.Fatalf("TLS MySQL deploy acceptance = %v, want unsupported capability", err)
	}
}
