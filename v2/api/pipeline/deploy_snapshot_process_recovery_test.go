package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/hub"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func deploySnapshotProcessPipeline(t *testing.T, crash bool) (*Pipeline, *store.DB) {
	t.Helper()
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(os.Getenv("NORN_DEPLOY_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NORN_DEPLOY_CRASH_SCHEMA")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	t.Cleanup(db.Close)
	signer, err := store.NewHMACAcceptanceSigner("pipeline-acceptance-test-signing-key-000000000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := database.NewDirectorySecretSource(os.Getenv("NORN_DEPLOY_CRASH_SECRETS"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Fatal(err)
	}
	pgDump, err = filepath.EvalSymlinks(pgDump)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(pgDump)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	backend := newPortableSnapshotBackend()
	manager, err := supervisor.NewManager(os.Getenv("NORN_DEPLOY_CRASH_SUPERVISOR"), backend.key, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(2 * supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	effects, err := NewSnapshotEffects(db, manager, pgDump, hex.EncodeToString(digest[:]), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return &Pipeline{DB: db, CheckpointStore: db, OperationStore: operations,
		SagaStore: saga.NewPostgresStore(pool), AppsDir: os.Getenv("NORN_DEPLOY_CRASH_APPS"),
		DatabaseTargets: &DatabaseTargets{ProfileID: "mini", Catalog: db.ActiveDatabaseCatalog,
			Secrets: secrets, SnapshotRoot: os.Getenv("NORN_DEPLOY_CRASH_SNAPSHOTS")},
		SnapshotEffects: effects, SnapshotObjects: processCrashSnapshotObjects{
			root: os.Getenv("NORN_DEPLOY_CRASH_OBJECTS"), exitAfterDump: crash}, WS: hub.New(nil)}, db
}

func TestDeploySnapshotRecoversAfterLiteralProcessExit(t *testing.T) {
	if os.Getenv("NORN_DEPLOY_CRASH_CHILD") == "1" {
		p, db := deploySnapshotProcessPipeline(t, true)
		first, claim, err := db.ClaimNextOperation(context.Background(), "first-deploy-process", time.Minute, []string{"app.deploy"})
		if err != nil || first == nil {
			t.Fatalf("first process claim = %+v, %v", first, err)
		}
		if _, err := p.ExecuteOperation(context.Background(), first, claim); err != nil {
			t.Fatal(err)
		}
		t.Fatal("first process returned without exiting after dump publication")
	}
	control := pgtest.Start(t)
	control.CreateDatabase(t, "norn_deploy_process_crash")
	t.Setenv("NORN_TEST_DATABASE_URL", control.URL("norn_deploy_process_crash"))
	f := newNamedFixture(t)
	ctx := context.Background()
	specPath := filepath.Join(f.p.AppsDir, f.app, "infraspec.yaml")
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	specText := strings.Replace(string(specBytes), "  - name: reports\n    purpose: application\n    capabilities: [health]\n", "", 1)
	if specText == string(specBytes) {
		t.Fatal("reports fixture requirement was not found")
	}
	pinnedImage := "registry.example/demo@sha256:" + strings.Repeat("a", 64)
	if err := os.WriteFile(specPath, []byte(specText+"build:\n  image: "+pinnedImage+"\nsnapshots:\n  exportBucket: review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.spec, err = model.LoadInfraSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.catalog.Profiles[0].DatabaseBindings, "reports")
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	baseline, err := f.queue(t, DatabaseBaselineKind, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	baselineResult, err := f.execute(t, baseline.ID)
	if err != nil || baselineResult.Status != model.OperationSucceeded {
		t.Fatalf("database baseline = %+v, %v", baselineResult, err)
	}
	if err := f.db.FinishClaimedOperation(ctx, baselineResult.Claim, baselineResult.Status, baselineResult.Message, baselineResult.Metadata); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.p.Run(ctx, f.spec, "abc1234", f.request)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := f.db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	objects, supervisorRoot := t.TempDir(), t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestDeploySnapshotRecoversAfterLiteralProcessExit$")
	child.Env = append(os.Environ(),
		"NORN_DEPLOY_CRASH_CHILD=1", "NORN_DEPLOY_CRASH_DB="+control.URL("norn_deploy_process_crash"),
		"NORN_DEPLOY_CRASH_SCHEMA="+schema, "NORN_DEPLOY_CRASH_APPS="+f.p.AppsDir,
		"NORN_DEPLOY_CRASH_SECRETS="+f.secretDir, "NORN_DEPLOY_CRASH_SNAPSHOTS="+f.snapshots,
		"NORN_DEPLOY_CRASH_OBJECTS="+objects, "NORN_DEPLOY_CRASH_SUPERVISOR="+supervisorRoot)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatalf("first process did not exit during snapshot export: %s", output)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("first process exit = %v: %s", err, output)
		}
	}
	var key, exportState string
	if err := f.db.Pool.QueryRow(ctx, `SELECT object_key,state FROM snapshot_export_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&key, &exportState); err != nil || exportState != "prepared" {
		t.Fatalf("first process export = %q %q, %v", key, exportState, err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key)); err != nil {
		t.Fatalf("first process dump = %v", err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key+snapshotManifestSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first process unexpectedly published manifest: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	f.p.CheckpointStore = f.db
	f.p.SnapshotObjects = processCrashSnapshotObjects{root: objects}
	f.p.WS = hub.New(nil)
	second, secondClaim, err := f.db.ClaimNextOperation(ctx, "successor-deploy-process", time.Minute, []string{"app.deploy"})
	if err != nil || second == nil || second.ID != accepted.Operation.ID || secondClaim.Generation() < 2 {
		t.Fatalf("successor claim = %+v, %v", second, err)
	}
	stopBeforeMigration := errors.New("stop after recovered snapshot")
	f.p.StartDeploymentStep = func(ctx context.Context, step model.DeploymentStep) error {
		if step.Step == "migrate" {
			return stopBeforeMigration
		}
		return f.db.StartDeploymentStep(ctx, step)
	}
	result, err := f.p.ExecuteOperation(ctx, second, secondClaim)
	if err != nil || result == nil || result.Status != model.OperationFailed || !strings.Contains(result.Message, stopBeforeMigration.Error()) {
		t.Fatalf("successor deploy prefix = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key+snapshotManifestSuffix)); err != nil {
		t.Fatalf("successor manifest = %v", err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, second.ID, key).Scan(&exportState); err != nil || exportState != "published" {
		t.Fatalf("successor export receipt = %q, %v", exportState, err)
	}
	var snapshotStatus string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM deployment_steps WHERE deployment_id=$1 AND step='snapshot'`, accepted.Intent.DeploymentID).Scan(&snapshotStatus); err != nil || snapshotStatus != "complete" {
		t.Fatalf("successor snapshot step = %q, %v", snapshotStatus, err)
	}
	var laterMutable int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployment_steps WHERE deployment_id=$1 AND kind='mutable' AND step<>'snapshot'`, accepted.Intent.DeploymentID).Scan(&laterMutable); err != nil || laterMutable != 0 {
		t.Fatalf("later mutable steps = %d, %v", laterMutable, err)
	}
}
