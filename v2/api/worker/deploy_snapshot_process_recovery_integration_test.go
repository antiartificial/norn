package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/hub"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func TestOperationWorkerRecoversDeploySnapshotAfterProcessExit(t *testing.T) {
	if os.Getenv("NORN_DEPLOY_WORKER_CHILD") == "1" {
		ctx := context.Background()
		config, err := pgxpool.ParseConfig(os.Getenv("NORN_DEPLOY_WORKER_DB"))
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NORN_DEPLOY_WORKER_SCHEMA")
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		db := &store.DB{Pool: pool}
		signer, err := store.NewHMACAcceptanceSigner("database-deploy-test-signing-key-0000000000")
		if err != nil {
			t.Fatal(err)
		}
		operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
		if err != nil {
			t.Fatal(err)
		}
		secrets, err := database.NewDirectorySecretSource(os.Getenv("NORN_DEPLOY_WORKER_SECRETS"))
		if err != nil {
			t.Fatal(err)
		}
		defer secrets.Close()
		p := &pipeline.Pipeline{DB: db, CheckpointStore: db, OperationStore: operations,
			SagaStore: saga.NewPostgresStore(pool), WS: hub.New(nil), AppsDir: os.Getenv("NORN_DEPLOY_WORKER_APPS"),
			DatabaseTargets: &pipeline.DatabaseTargets{ProfileID: "mini", Catalog: db.ActiveDatabaseCatalog,
				Secrets: secrets, SnapshotRoot: os.Getenv("NORN_DEPLOY_WORKER_SNAPSHOTS")},
			SnapshotObjects: workerSnapshotObjects{root: os.Getenv("NORN_DEPLOY_WORKER_OBJECTS"), crash: true}}
		w := NewOperationWorkerForKinds(db, p, []string{"app.deploy"})
		if err := w.runOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var status, message string
		queryErr := db.Pool.QueryRow(ctx, `SELECT status,message FROM operations WHERE kind='app.deploy' LIMIT 1`).Scan(&status, &message)
		t.Fatalf("first worker returned without exiting after dump publication: status=%s message=%s query=%v", status, message, queryErr)
	}
	control := pgtest.Start(t)
	control.CreateDatabase(t, "norn_worker_deploy_crash")
	t.Setenv("NORN_TEST_DATABASE_URL", control.URL("norn_worker_deploy_crash"))
	f := newDatabaseDeployFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(f.pipeline.AppsDir, f.spec.App, "infraspec.yaml")
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(specBytes), ":fixture", "@sha256:"+strings.Repeat("a", 64), 1)
	if updated == string(specBytes) {
		t.Fatal("prebuilt image fixture requirement was not found")
	}
	updated += "snapshots:\n  exportBucket: review\n"
	if err := os.WriteFile(specPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	f.spec, err = model.LoadInfraSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	baselineRequest := f.request
	baselineRequest.Key = "baseline-before-deploy"
	baseline, err := f.pipeline.QueueOperation(ctx, model.Operation{ID: uuid.NewString(), Kind: pipeline.DatabaseBaselineKind,
		App: f.spec.App, SagaID: uuid.NewString(), Status: model.OperationQueued, Source: "control-api",
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1}, baselineRequest)
	if err != nil {
		t.Fatal(err)
	}
	baselineWorker := NewOperationWorkerForKinds(f.db, f.pipeline, []string{pipeline.DatabaseBaselineKind})
	if err := baselineWorker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	baselineFinished, err := f.db.GetOperation(ctx, baseline.Operation.ID)
	if err != nil || baselineFinished.Status != model.OperationSucceeded {
		t.Fatalf("database baseline = %+v, %v", baselineFinished, err)
	}
	accepted, err := f.pipeline.Run(ctx, f.spec, "HEAD", f.request)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := f.db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	objects := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestOperationWorkerRecoversDeploySnapshotAfterProcessExit$")
	child.Env = append(os.Environ(), "NORN_DEPLOY_WORKER_CHILD=1", "NORN_DEPLOY_WORKER_DB="+control.URL("norn_worker_deploy_crash"),
		"NORN_DEPLOY_WORKER_SCHEMA="+schema, "NORN_DEPLOY_WORKER_SECRETS="+f.secretDir,
		"NORN_DEPLOY_WORKER_APPS="+f.pipeline.AppsDir, "NORN_DEPLOY_WORKER_SNAPSHOTS="+f.snapshots,
		"NORN_DEPLOY_WORKER_OBJECTS="+objects)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatalf("first worker did not exit during snapshot export: %s", output)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("first worker exit = %v: %s", err, output)
		}
	}
	var key, exportState string
	if err := f.db.Pool.QueryRow(ctx, `SELECT object_key,state FROM snapshot_export_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&key, &exportState); err != nil || exportState != "prepared" {
		t.Fatalf("first worker export = %q %q, %v", key, exportState, err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key)); err != nil {
		t.Fatalf("first worker dump = %v", err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key+".manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first worker unexpectedly published manifest: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	f.pipeline.CheckpointStore = f.db
	f.pipeline.SnapshotObjects = workerSnapshotObjects{root: objects}
	stopBeforeMigration := errors.New("stop recovered deploy before migration")
	f.pipeline.StartDeploymentStep = func(ctx context.Context, step model.DeploymentStep) error {
		if step.Step == "migrate" {
			return stopBeforeMigration
		}
		return f.db.StartDeploymentStep(ctx, step)
	}
	f.worker = NewOperationWorkerForKinds(f.db, f.pipeline, []string{"app.deploy"})
	if err := f.worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := f.db.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || finished.Status != model.OperationFailed || finished.Attempts != 2 || !strings.Contains(finished.Message, stopBeforeMigration.Error()) {
		t.Fatalf("successor worker outcome = %+v, %v", finished, err)
	}
	if _, err := os.Stat(filepath.Join(objects, "review", key+".manifest.json")); err != nil {
		t.Fatalf("successor manifest = %v", err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, finished.ID, key).Scan(&exportState); err != nil || exportState != "published" {
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
