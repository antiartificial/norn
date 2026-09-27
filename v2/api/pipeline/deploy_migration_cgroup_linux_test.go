//go:build linux

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/effect/supervisor"
	"norn/v2/api/hub"
	"norn/v2/api/model"
)

func configureProcessCrashMigrationEffects(t *testing.T, p *Pipeline) {
	t.Helper()
	runner := os.Getenv("NORN_EFFECT_RUNNER_BINARY")
	binary, err := os.ReadFile(runner)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	key := []byte("pipeline-migration-cgroup-signing-key-32")
	backend, err := supervisor.NewCgroupBackend(os.Getenv("NORN_DEPLOY_MIGRATION_CGROUP"), runner, hex.EncodeToString(digest[:]), key)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := supervisor.NewManager(os.Getenv("NORN_DEPLOY_MIGRATION_JOURNAL"), key, backend)
	if err != nil {
		t.Fatal(err)
	}
	p.MigrationEffects, err = NewMigrationEffects(p.DB, manager, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
}

// This is opt-in because its command runner requires a disposable privileged
// cgroup-v2 container. The first API process exits after a real PostgreSQL
// commit while the migration command is still contained and running.
func TestDeployMigrationRecoversAfterLiteralProcessExit(t *testing.T) {
	if os.Getenv("NORN_REAL_CGROUP_TEST") != "1" || os.Getenv("NORN_PIPELINE_EXTERNAL_PG_ROOT") == "" {
		t.Skip("run with the disposable Linux pipeline cgroup harness")
	}
	marker := os.Getenv("NORN_DEPLOY_MIGRATION_MARKER")
	if os.Getenv("NORN_DEPLOY_MIGRATION_CHILD") == "1" {
		p, db := deploySnapshotProcessPipeline(t, false)
		operation, claim, err := db.ClaimNextOperation(context.Background(), "first-migration-process", time.Minute, []string{"app.deploy"})
		if err != nil || operation == nil {
			t.Fatalf("first migration claim = %+v, %v", operation, err)
		}
		_, _ = p.ExecuteOperation(context.Background(), operation, claim)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				os.Exit(47)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("migration command did not commit before API exit")
	}

	f := newNamedFixture(t)
	ctx := context.Background()
	specPath := filepath.Join(f.p.AppsDir, f.app, "infraspec.yaml")
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	specText := strings.Replace(string(specBytes), "  - name: reports\n    purpose: application\n    capabilities: [health]\n", "", 1)
	specText = strings.Replace(specText, "capabilities: [runtime, snapshot, restore, health]", "capabilities: [runtime, migration, snapshot, restore, health]", 1)
	if strings.Contains(specText, "  - name: reports") || !strings.Contains(specText, "runtime, migration, snapshot") {
		t.Fatal("migration fixture requirements were not found")
	}
	marker = filepath.Join(t.TempDir(), "committed")
	command := fmt.Sprintf("psql \"$DATABASE_URL\" -X -v ON_ERROR_STOP=1 -c 'INSERT INTO migration_probe (id) VALUES (1)' >/dev/null && printf committed > %q && sleep 3", marker)
	image := "registry.example/demo@sha256:" + strings.Repeat("a", 64)
	specText += fmt.Sprintf("build:\n  image: %s\nmigrationDatabase: primary\nmigrations: %q\nmigrationPostcondition:\n  query: 'SELECT count(*) FROM migration_probe'\n  expectedValue: '1'\n", image, command)
	if err := os.WriteFile(specPath, []byte(specText), 0o644); err != nil {
		t.Fatal(err)
	}
	f.spec, err = model.LoadInfraSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	f.primary.Exec(t, "shop", "CREATE TABLE migration_probe (id integer PRIMARY KEY); ALTER TABLE migration_probe OWNER TO shop_app")
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
	cgroupRoot := filepath.Join("/sys/fs/cgroup", "norn-pipeline-migration-"+accepted.Operation.ID[:8])
	if err := os.Mkdir(cgroupRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(cgroupRoot, "cgroup.kill"), []byte("1\n"), 0o200)
		_ = os.Remove(cgroupRoot)
	})
	journal := t.TempDir()
	t.Setenv("NORN_DEPLOY_MIGRATION_CGROUP", cgroupRoot)
	t.Setenv("NORN_DEPLOY_MIGRATION_JOURNAL", journal)
	child := exec.Command(os.Args[0], "-test.run=^TestDeployMigrationRecoversAfterLiteralProcessExit$")
	child.Env = append(os.Environ(),
		"NORN_DEPLOY_MIGRATION_CHILD=1", "NORN_DEPLOY_MIGRATION_MARKER="+marker,
		"NORN_DEPLOY_CRASH_DB="+os.Getenv("NORN_TEST_DATABASE_URL"), "NORN_DEPLOY_CRASH_SCHEMA="+schema,
		"NORN_DEPLOY_CRASH_APPS="+f.p.AppsDir, "NORN_DEPLOY_CRASH_SECRETS="+f.secretDir,
		"NORN_DEPLOY_CRASH_SNAPSHOTS="+f.snapshots, "NORN_DEPLOY_CRASH_SUPERVISOR="+t.TempDir(),
		"NORN_DEPLOY_CRASH_OBJECTS="+t.TempDir())
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatalf("first API process did not exit: %s", output)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("first API process exit = %v: %s", err, output)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("migration did not commit: %v", err)
	}
	var originalRuntime, originalLifecycle string
	if err := f.db.Pool.QueryRow(ctx, `SELECT runtime_instance_id,lifecycle FROM operation_effects WHERE operation_id=$1 AND stage=$2`, accepted.Operation.ID, supervisor.MigrationStage).Scan(&originalRuntime, &originalLifecycle); err != nil || originalRuntime == "" || originalLifecycle != "launched" {
		t.Fatalf("original migration effect = %q %q, %v", originalRuntime, originalLifecycle, err)
	}
	cgroupDigest := sha256.Sum256([]byte(originalRuntime))
	commandCgroup := filepath.Join(cgroupRoot, hex.EncodeToString(cgroupDigest[:]), "command")
	before, err := os.ReadFile(filepath.Join(commandCgroup, "cgroup.events"))
	if err != nil || !strings.Contains(string(before), "populated 1") {
		t.Fatalf("migration command did not survive API exit: %q, %v", before, err)
	}
	var sourceBuild, snapshot int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_checkpoints WHERE operation_id=$1 AND stage IN ('source','build')`, accepted.Operation.ID).Scan(&sourceBuild); err != nil || sourceBuild != 2 {
		t.Fatalf("pre-migration checkpoints = %d, %v", sourceBuild, err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployment_steps WHERE deployment_id=$1 AND step='snapshot' AND status='complete'`, accepted.Intent.DeploymentID).Scan(&snapshot); err != nil || snapshot != 1 {
		t.Fatalf("pre-migration snapshot step = %d, %v", snapshot, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	configureProcessCrashMigrationEffects(t, f.p)
	f.p.CheckpointStore = f.db
	f.p.WS = hub.New(nil)
	go f.p.WS.Run()
	second, claim, err := f.db.ClaimNextOperation(ctx, "successor-migration-process", time.Minute, []string{"app.deploy"})
	if err != nil || second == nil || second.ID != accepted.Operation.ID || claim.Generation() < 2 {
		t.Fatalf("successor claim = %+v, %v", second, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = f.p.ExecuteOperation(ctx, second, claim)
		var state string
		if err := f.db.Pool.QueryRow(ctx, `SELECT lifecycle FROM operation_effects WHERE operation_id=$1 AND stage=$2`, second.ID, supervisor.MigrationStage).Scan(&state); err == nil && state == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	connection, err := pgx.Connect(ctx, f.primary.URL("shop"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	var writes, effects int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM migration_probe`).Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("migration write count = %d, %v", writes, err)
	}
	var lifecycle, recoveredRuntime string
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(lifecycle),''), COALESCE(max(runtime_instance_id),'') FROM operation_effects WHERE operation_id=$1 AND stage=$2`, second.ID, supervisor.MigrationStage).Scan(&effects, &lifecycle, &recoveredRuntime); err != nil || effects != 1 || lifecycle != "completed" || recoveredRuntime != originalRuntime {
		t.Fatalf("migration effects = %d lifecycle=%q runtime preserved=%v, %v", effects, lifecycle, recoveredRuntime == originalRuntime, err)
	}
	var migrationStep int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployment_steps WHERE deployment_id=$1 AND step='migrate' AND status='complete'`, accepted.Intent.DeploymentID).Scan(&migrationStep); err != nil || migrationStep != 1 {
		t.Fatalf("recovered migration step = %d, %v", migrationStep, err)
	}
	after, err := os.ReadFile(filepath.Join(commandCgroup, "cgroup.events"))
	if err != nil || !strings.Contains(string(after), "populated 0") {
		t.Fatalf("completed migration still has a command process: %q, %v", after, err)
	}
}
