package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

const exportRecoverySignerKey = "worker-export-recovery-signing-key-000000000"

type workerSnapshotObjects struct {
	root  string
	crash bool
}

func (s workerSnapshotObjects) PutObject(ctx context.Context, bucket, key, path string) error {
	return s.PutObjectIfAbsent(ctx, bucket, key, path)
}

func (s workerSnapshotObjects) PutObjectIfAbsent(_ context.Context, bucket, key, path string) error {
	destination := filepath.Join(s.root, bucket, key)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if s.crash && !strings.HasSuffix(key, ".manifest.json") {
		os.Exit(47)
	}
	return nil
}

func (s workerSnapshotObjects) GetObject(_ context.Context, bucket, key, path string) error {
	in, err := os.Open(filepath.Join(s.root, bucket, key))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

func exportRecoveryPipeline(db *store.DB, appsDir, objectRoot string, crash bool) (*pipeline.Pipeline, error) {
	signer, err := store.NewHMACAcceptanceSigner(exportRecoverySignerKey)
	if err != nil {
		return nil, err
	}
	operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		return nil, err
	}
	return &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool), OperationStore: operations,
		AppsDir: appsDir, SnapshotObjects: workerSnapshotObjects{root: objectRoot, crash: crash}}, nil
}

func TestOperationWorkerRecoversReservedSnapshotExportAfterProcessExit(t *testing.T) {
	if os.Getenv("NORN_EXPORT_WORKER_CHILD") == "1" {
		db, err := store.Connect(os.Getenv("NORN_EXPORT_WORKER_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		p, err := exportRecoveryPipeline(db, os.Getenv("NORN_EXPORT_WORKER_APPS"), os.Getenv("NORN_EXPORT_WORKER_OBJECTS"), true)
		if err != nil {
			t.Fatal(err)
		}
		w := NewOperationWorkerForKinds(db, p, []string{"app.snapshot-export"})
		if err := w.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("first worker returned without crashing after the dump write")
	}
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_worker_export_crash")
	databaseURL := server.URL("norn_worker_export_crash")
	db, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	workspace, appsDir, objectRoot := t.TempDir(), t.TempDir(), t.TempDir()
	const filename = "shop_abc1234_20260925T120000.dump"
	if err := os.MkdirAll(filepath.Join(workspace, "snapshots"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "snapshots", filename), []byte("worker process recovery dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(appsDir, "demo")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: demo\ndeploy: true\nprocesses:\n  web:\n    command: ./web\ninfrastructure:\n  postgres:\n    database: shop\nsnapshots:\n  exportBucket: review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := exportRecoveryPipeline(db, appsDir, objectRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := p.OperationStore.(*store.PGOperationStore).Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	op := model.Operation{ID: uuid.NewString(), Kind: "app.snapshot-export", App: "demo", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Source: "control-api", Payload: map[string]interface{}{"bucket": "review", "snapshot": filename}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	accepted, err := p.QueueOperation(context.Background(), op, pipeline.EnqueueRequest{Authority: authority,
		Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "worker-export-process-crash",
		Audit: store.AcceptanceAuditContext{Source: "worker-integration"}})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOperationWorkerRecoversReservedSnapshotExportAfterProcessExit$")
	child.Dir = workspace
	child.Env = append(os.Environ(), "NORN_EXPORT_WORKER_CHILD=1", "NORN_EXPORT_WORKER_DB="+databaseURL,
		"NORN_EXPORT_WORKER_APPS="+appsDir, "NORN_EXPORT_WORKER_OBJECTS="+objectRoot)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatal("first operation worker did not exit during remote publication")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("first operation worker exit = %v: %s", err, output)
		}
	}
	key := "snapshots/demo/operations/" + accepted.Operation.ID + "/" + filename
	var state string
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, accepted.Operation.ID, key).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("crashed worker intent = %q, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "review", key)); err != nil {
		t.Fatalf("crashed worker dump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "review", key+".manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crashed worker unexpectedly wrote manifest: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	w := NewOperationWorkerForKinds(db, p, []string{"app.snapshot-export"})
	if err := w.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := db.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Attempts != 2 {
		t.Fatalf("successor worker outcome = %+v, %v", finished, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, accepted.Operation.ID, key).Scan(&state); err != nil || state != "published" {
		t.Fatalf("successor worker receipt = %q, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(objectRoot, "review", key+".manifest.json")); err != nil {
		t.Fatalf("successor worker manifest: %v", err)
	}
}
