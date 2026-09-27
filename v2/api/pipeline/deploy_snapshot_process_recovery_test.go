package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/effect"
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
		var status string
		var message string
		var attempts, maxAttempts int
		_ = f.db.Pool.QueryRow(ctx, `SELECT status, message, attempts, max_attempts FROM operations WHERE id=$1`, accepted.Operation.ID).Scan(&status, &message, &attempts, &maxAttempts)
		rows, _ := f.db.Pool.Query(ctx, `SELECT step, kind, status FROM deployment_steps WHERE deployment_id=$1 ORDER BY started_at`, accepted.Intent.DeploymentID)
		var steps []string
		if rows != nil {
			for rows.Next() {
				var step, kind, state string
				_ = rows.Scan(&step, &kind, &state)
				steps = append(steps, step+":"+kind+":"+state)
			}
			rows.Close()
		}
		t.Fatalf("successor claim = %+v, %v; operation status=%s attempts=%d/%d message=%q steps=%v", second, err, status, attempts, maxAttempts, message, steps)
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

func TestRecoveredDeploymentMigrationReusesAcceptedPreMigrationSnapshots(t *testing.T) {
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
	image := "registry.example/demo@sha256:" + strings.Repeat("a", 64)
	if err := os.WriteFile(specPath, []byte(specText+"build:\n  image: "+image+"\n"), 0o644); err != nil {
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
	first, firstClaim, err := f.db.ClaimNextOperation(ctx, "original-migration-owner", time.Minute, []string{"app.deploy"})
	if err != nil || first == nil || first.ID != accepted.Operation.ID {
		t.Fatalf("first deployment claim = %+v, %v", first, err)
	}
	f.p.WS = hub.New(nil)
	f.p.CheckpointStore = f.db
	stopBeforeMigration := errors.New("stop after recording migration step")
	f.p.StartDeploymentStep = func(ctx context.Context, step model.DeploymentStep) error {
		if err := f.db.StartDeploymentStep(ctx, step); err != nil {
			return err
		}
		if step.Step == "migrate" {
			return stopBeforeMigration
		}
		return nil
	}
	firstResult, err := f.p.ExecuteOperation(ctx, first, firstClaim)
	if err != nil || firstResult == nil || firstResult.Status != model.OperationFailed || !strings.Contains(firstResult.Message, stopBeforeMigration.Error()) {
		t.Fatalf("first deployment predecessor stages = %+v, %v", firstResult, err)
	}
	for _, stage := range []string{store.CheckpointSource, store.CheckpointBuild} {
		checkpoint, err := f.db.LoadOperationCheckpoint(ctx, first.ID, stage)
		if err != nil || checkpoint == nil || checkpoint.ClaimGeneration != firstClaim.Generation() {
			t.Fatalf("first deployment %s checkpoint = %+v, %v", stage, checkpoint, err)
		}
	}
	bound, err := f.p.openDatabaseTargets(ctx, first.Payload, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	log := saga.New(discardSagaStore{}, f.app, "test", "deploy")
	original := map[string]map[string][sha256.Size]byte{}
	var primaryDump string
	for _, name := range []string{"primary", "analytics"} {
		target := bound.named[name]
		location, err := f.p.prepareSnapshotLocation("shop", target)
		if err != nil {
			t.Fatal(err)
		}
		original[name] = map[string][sha256.Size]byte{}
		for _, filename := range directoryNames(t, location.dir) {
			data, err := os.ReadFile(filepath.Join(location.dir, filename))
			if err != nil {
				t.Fatal(err)
			}
			original[name][filename] = sha256.Sum256(data)
			if name == "primary" && strings.HasSuffix(filename, ".dump") {
				primaryDump = filepath.Join(location.dir, filename)
			}
		}
		if len(original[name]) != 2 {
			t.Fatalf("original %s snapshot files = %d", name, len(original[name]))
		}
	}
	if primaryDump == "" {
		t.Fatal("primary pre-migration dump is missing")
	}
	effects, err := store.NewPGEffectStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := effects.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "database/shop-primary/migration",
		OperationClaim: effect.OperationClaim{OperationID: first.ID, OwnerID: firstClaim.OwnerID(), Generation: firstClaim.Generation()},
		Stage:          supervisor.MigrationStage, Supervisor: "migration-runner", SupervisorExecutionID: "migration-" + first.ID,
		LaunchPayload: json.RawMessage(`{}`)}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effects.Reserve(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM operation_effects WHERE operation_id=$1`, first.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM operation_checkpoints WHERE operation_id=$1`, first.ID)
	})
	setOrderState(t, f.primary, "after-migration")
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.db.GetOperation(ctx, first.ID)
	if err != nil || recovered.Status != model.OperationQueued || recovered.Metadata["replayMigration"] != true {
		steps, _ := f.db.ListDeploymentSteps(ctx, accepted.Intent.DeploymentID)
		var checkpoints []string
		rows, _ := f.db.Pool.Query(ctx, `SELECT stage FROM operation_checkpoints WHERE operation_id=$1 ORDER BY stage`, first.ID)
		if rows != nil {
			for rows.Next() {
				var stage string
				_ = rows.Scan(&stage)
				checkpoints = append(checkpoints, stage)
			}
			rows.Close()
		}
		t.Fatalf("guarded deployment recovery = %+v, %v; steps=%+v checkpoints=%v", recovered, err, steps, checkpoints)
	}
	second, secondClaim, err := f.db.ClaimNextOperation(ctx, "successor-migration-owner", time.Minute, []string{"app.deploy"})
	if err != nil || second == nil || second.ID != first.ID || secondClaim.Generation() <= firstClaim.Generation() {
		t.Fatalf("successor claim = %+v, %v", second, err)
	}
	reservation.OperationClaim = effect.OperationClaim{OperationID: second.ID, OwnerID: secondClaim.OwnerID(), Generation: secondClaim.Generation()}
	reused, err := effects.Reserve(ctx, reservation)
	if err != nil || reused.Created || reused.Record.Reservation.OperationClaim.Generation != firstClaim.Generation() {
		t.Fatalf("successor replaced original migration effect: %+v, %v", reused, err)
	}
	secondResult, err := f.p.ExecuteOperation(ctx, second, secondClaim)
	if err != nil || secondResult == nil || secondResult.Status != model.OperationFailed || !strings.Contains(secondResult.Message, stopBeforeMigration.Error()) {
		t.Fatalf("successor deployment replay = %+v, %v", secondResult, err)
	}
	secondState := &state{spec: f.spec, commitSHA: "abc1234", deploymentID: accepted.Intent.DeploymentID,
		claim: secondClaim, operationStartedAt: second.StartedAt, operationPayload: second.Payload, replayMigration: second.Metadata["replayMigration"] == true}
	secondState.database, err = f.p.openDatabaseTargets(ctx, second.Payload, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	secondState.databaseOpened = true
	defer secondState.database.Close()
	for _, name := range []string{"primary", "analytics"} {
		location, err := f.p.prepareSnapshotLocation("shop", secondState.database.named[name])
		if err != nil {
			t.Fatal(err)
		}
		if len(directoryNames(t, location.dir)) != len(original[name]) {
			t.Fatalf("successor created a new %s dump", name)
		}
		for filename, digest := range original[name] {
			data, err := os.ReadFile(filepath.Join(location.dir, filename))
			if err != nil || sha256.Sum256(data) != digest {
				t.Fatalf("successor changed %s/%s: %v", name, filename, err)
			}
		}
	}
	if err := os.Remove(primaryDump); err != nil {
		t.Fatal(err)
	}
	if err := f.p.snapshot(ctx, secondState, log); err == nil || !strings.Contains(err.Error(), "original pre-migration snapshot is unavailable") {
		t.Fatalf("successor accepted missing original pre-migration dump: %v", err)
	}
}
