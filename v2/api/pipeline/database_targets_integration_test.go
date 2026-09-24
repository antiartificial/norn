package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/internal/pgtest"
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
	p               *Pipeline
	db              *store.DB
	request         EnqueueRequest
	app             string
	appSchema       string
	appDB           *pgx.Conn
	appDBName       string
	appRole         string
	appHost         string
	appPort         int
	catalog         database.Catalog
	snapshots       string
	controlDB       string
	secretDir       string
	requestSeq      int
	snapshotBackend *portableSnapshotBackend
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
	// app.snapshot is fail closed unless it has a supervisor. This portable
	// backend is test-only: it runs the pinned pg_dump against this fixture's
	// disposable PostgreSQL target and reports containment after the synchronous
	// child has exited. It never stands in for the Linux cgroup backend.
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
	manager, err := supervisor.NewManager(t.TempDir(), []byte("snapshot-pg-integration-signing-key-32"), backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	effects, err := NewSnapshotEffects(db, manager, pgDump, hex.EncodeToString(digest[:]), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p.SnapshotEffects = effects
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
	return &targetFixture{p: p, db: db, request: request, app: app, appSchema: appSchema, appDB: appDB, appDBName: appDBName, appRole: parsed.User.Username(), appHost: host, appPort: port, catalog: catalog, snapshots: snapshots, controlDB: controlDB, secretDir: secretDir, snapshotBackend: backend}
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
	result, _, err := f.executeWithClaim(t, operationID)
	return result, err
}

func (f *targetFixture) executeWithClaim(t *testing.T, operationID string) (*OperationResult, store.OperationClaim, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, operationID); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "target-worker", 60_000_000_000, []string{"app.snapshot", "app.snapshot-restore", "app.snapshot-prune", "app.migrate", DatabaseBaselineKind})
	if err != nil || claimed == nil || claimed.ID != operationID {
		t.Fatalf("claim %s = %+v, %v", operationID, claimed, err)
	}
	result, err := f.p.ExecuteOperation(ctx, claimed, claim)
	return result, claim, err
}

type portableSnapshotArtifact struct {
	manifest supervisor.SnapshotManifest
	data     []byte
}

// portableSnapshotBackend is a test seam for the real PostgreSQL integration
// suite. It has no process-tree containment mechanism and therefore must not
// be used by startup or Linux cgroup qualification tests.
type portableSnapshotBackend struct {
	artifacts map[string]portableSnapshotArtifact
	failures  map[string][]byte
	key       []byte
	starts    int
	failCopy  bool
	rebooted  bool
}

func newPortableSnapshotBackend() *portableSnapshotBackend {
	return &portableSnapshotBackend{artifacts: map[string]portableSnapshotArtifact{}, failures: map[string][]byte{}, key: []byte("snapshot-pg-integration-signing-key-32")}
}

func (b *portableSnapshotBackend) Start(context.Context, supervisor.BackendExecution, effect.LaunchMaterial) error {
	return fmt.Errorf("portable snapshot backend accepts only snapshot work")
}
func (b *portableSnapshotBackend) Observe(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (b *portableSnapshotBackend) Revoke(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorStopped, ContainmentProven: true, EvidenceReference: "portable-test-revoked"}, nil
}
func (b *portableSnapshotBackend) RetrieveResult(context.Context, supervisor.BackendExecution, string) ([]byte, error) {
	return nil, os.ErrNotExist
}
func (b *portableSnapshotBackend) StartSnapshot(ctx context.Context, execution supervisor.BackendExecution, descriptor supervisor.SnapshotDescriptor, material supervisor.SnapshotLaunchMaterial) error {
	binary, err := os.ReadFile(material.PGDumpPath)
	if err != nil {
		return err
	}
	binaryDigest := sha256.Sum256(binary)
	if hex.EncodeToString(binaryDigest[:]) != material.PGDumpSHA256 || descriptor.PGDumpSHA256 != material.PGDumpSHA256 {
		return fmt.Errorf("portable pg_dump does not match the reserved descriptor")
	}
	private, err := os.MkdirTemp(execution.StateDirectory, ".portable-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	service, passfile, output := filepath.Join(private, "pg_service.conf"), filepath.Join(private, "pgpass"), filepath.Join(private, "archive.dump")
	if err := os.WriteFile(service, material.ServiceFile, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(passfile, []byte("*:*:*:*:"+material.Password+"\n"), 0o600); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, material.PGDumpPath, "-Fc", "--no-owner", "--no-privileges", "--file", output, "--dbname=service="+material.ServiceName)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "PGSERVICEFILE=" + service, "PGPASSFILE=" + passfile}
	outputBytes, commandErr := command.CombinedOutput()
	b.starts++
	if commandErr != nil {
		// The process has exited before this synchronous test backend returns.
		// Preserve a bounded terminal failure so the effect executor can record
		// its exact no-replay outcome and release the snapshot admission.
		b.failures[execution.SupervisorExecutionID] = append([]byte(nil), outputBytes...)
		return nil
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	descriptorBytes, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	descriptorDigest := sha256.Sum256(descriptorBytes)
	manifest := supervisor.SnapshotManifest{Protocol: supervisor.SnapshotProtocolV1, RuntimeInstanceID: execution.RuntimeInstanceID, DescriptorSHA256: hex.EncodeToString(descriptorDigest[:]), Artifact: supervisor.SnapshotArtifact{Reference: "portable/" + execution.SupervisorExecutionID, Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Regular: true, NoFollow: true}, ContainmentProven: true, ObservedAt: time.Now().UTC()}
	manifest.MAC = portableSnapshotManifestMAC(b.key, manifest)
	b.artifacts[execution.SupervisorExecutionID] = portableSnapshotArtifact{data: data, manifest: manifest}
	return nil
}

func portableSnapshotManifestMAC(key []byte, manifest supervisor.SnapshotManifest) string {
	payload, _ := json.Marshal(struct {
		Protocol          string                      `json:"protocol"`
		RuntimeInstanceID string                      `json:"runtimeInstanceId"`
		DescriptorSHA256  string                      `json:"descriptorSha256"`
		Artifact          supervisor.SnapshotArtifact `json:"artifact"`
		ContainmentProven bool                        `json:"containmentProven"`
		ObservedAt        time.Time                   `json:"observedAt"`
	}{manifest.Protocol, manifest.RuntimeInstanceID, manifest.DescriptorSHA256, manifest.Artifact, manifest.ContainmentProven, manifest.ObservedAt})
	statusKey := hmac.New(sha256.New, key)
	statusKey.Write([]byte("norn-effect-runner-status-v1\x00"))
	statusKey.Write([]byte(manifest.RuntimeInstanceID))
	mac := hmac.New(sha256.New, statusKey.Sum(nil))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
func (b *portableSnapshotBackend) ObserveSnapshot(_ context.Context, execution supervisor.BackendExecution, _ supervisor.SnapshotDescriptor) (supervisor.BackendState, error) {
	if b.rebooted {
		// A host reboot removes the execution containment namespace. The
		// already-completed effect must retrieve its signed output without
		// re-observing this now-unknown runtime.
		return supervisor.BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "portable-test-rebooted"}, nil
	}
	if output, failed := b.failures[execution.SupervisorExecutionID]; failed {
		exit := 1
		return supervisor.BackendState{Phase: effect.SupervisorFailed, ExitCode: &exit, Output: append([]byte(nil), output...), ContainmentProven: true, EvidenceReference: "portable-test-failed/" + execution.RuntimeInstanceID}, nil
	}
	artifact, ok := b.artifacts[execution.SupervisorExecutionID]
	if !ok {
		return supervisor.BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "portable-test-missing"}, nil
	}
	output, err := json.Marshal(artifact.manifest)
	if err != nil {
		return supervisor.BackendState{}, err
	}
	exit := 0
	return supervisor.BackendState{Phase: effect.SupervisorSucceeded, ExitCode: &exit, Output: output, ContainmentProven: true, EvidenceReference: "portable-test/" + execution.RuntimeInstanceID}, nil
}

func TestSupervisedPostgresSnapshotPGDumpFailureReleasesAdmissionForRetry(t *testing.T) {
	server := pgtest.Start(t)
	controlDatabase, targetDatabase := "norn_snapshot_failure_control", "norn_snapshot_failure_target"
	server.CreateDatabase(t, controlDatabase)
	server.CreateDatabase(t, targetDatabase)
	t.Setenv("NORN_TEST_DATABASE_URL", server.URL(controlDatabase))
	t.Setenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL", server.URL(targetDatabase))
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	// Exercise a real pg_dump connection to the disposable PostgreSQL target,
	// then force its wrapper to return a terminal non-zero status.
	real, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "pg_dump-fail")
	script := fmt.Sprintf("#!/bin/sh\n%q \"$@\"\nexit 1\n", real)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bytes)
	f.p.SnapshotEffects.PGDumpPath = wrapper
	f.p.SnapshotEffects.PGDumpSHA256 = hex.EncodeToString(digest[:])
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if result, _, err := f.executeWithClaim(t, op.ID); err == nil || result != nil {
		t.Fatalf("failed pg_dump snapshot = %+v, %v", result, err)
	}
	record, found, err := f.p.SnapshotEffects.Store.LatestForOperation(ctx, op.ID, "app.snapshot")
	if err != nil || !found || record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeFailed {
		t.Fatalf("failed pg_dump effect = %+v, found=%v, err=%v", record, found, err)
	}
	realBytes, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	realDigest := sha256.Sum256(realBytes)
	f.p.SnapshotEffects.PGDumpPath = real
	f.p.SnapshotEffects.PGDumpSHA256 = hex.EncodeToString(realDigest[:])
	retry, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, retry.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("snapshot retry after failed pg_dump = %+v, %v", result, err)
	}
	if f.snapshotBackend.starts != 2 {
		t.Fatalf("snapshot retry did not receive a fresh admission: starts=%d", f.snapshotBackend.starts)
	}
}
func (b *portableSnapshotBackend) QuerySnapshot(_ context.Context, execution supervisor.BackendExecution, _ supervisor.SnapshotDescriptor) (supervisor.SnapshotManifest, error) {
	artifact, ok := b.artifacts[execution.SupervisorExecutionID]
	if !ok {
		return supervisor.SnapshotManifest{}, os.ErrNotExist
	}
	return artifact.manifest, nil
}
func (b *portableSnapshotBackend) CopySnapshotArtifact(_ context.Context, execution supervisor.BackendExecution, _ supervisor.SnapshotDescriptor, destination io.Writer) (supervisor.SnapshotManifest, error) {
	if b.failCopy {
		b.failCopy = false
		return supervisor.SnapshotManifest{}, fmt.Errorf("portable publication interruption")
	}
	artifact, ok := b.artifacts[execution.SupervisorExecutionID]
	if !ok {
		return supervisor.SnapshotManifest{}, os.ErrNotExist
	}
	_, err := destination.Write(artifact.data)
	return artifact.manifest, err
}

func (f *targetFixture) orderState(t *testing.T) string {
	t.Helper()
	var state string
	if err := f.appDB.QueryRow(context.Background(), `SELECT state FROM `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// This exercises the production pipeline path against PostgreSQL while using
// only a portable, test-scoped containment assertion. A publication failure
// happens after the effect is durably complete; the next claim must reuse that
// reservation and artifact instead of launching pg_dump a second time.
func TestSupervisedPostgresSnapshotReservationReplayAfterPublicationCrash(t *testing.T) {
	server := pgtest.Start(t)
	controlDatabase, targetDatabase := "norn_snapshot_control", "norn_snapshot_target"
	server.CreateDatabase(t, controlDatabase)
	server.CreateDatabase(t, targetDatabase)
	t.Setenv("NORN_TEST_DATABASE_URL", server.URL(controlDatabase))
	t.Setenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL", server.URL(targetDatabase))
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.queueKey(t, "supervised-snapshot-publication-crash", "app.snapshot", map[string]interface{}{})
	if err != nil || accepted.Replayed {
		t.Fatalf("snapshot acceptance = %+v, %v", accepted, err)
	}
	// The failure is at publication, after the effect's reservation, launch,
	// result and durable completion have all been recorded.
	f.snapshotBackend.failCopy = true
	if result, claim, err := f.executeWithClaim(t, accepted.Operation.ID); err == nil || result != nil {
		t.Fatalf("interrupted publication = %+v, %v", result, err)
	} else if err := f.db.DeferClaimedOperation(ctx, claim, "test publication interruption", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.p.SnapshotEffects.Store.LatestForOperation(ctx, accepted.Operation.ID, "app.snapshot")
	if err != nil || !found || record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeSucceeded {
		t.Fatalf("durable snapshot effect after crash = %+v, found=%v, err=%v", record, found, err)
	}
	if f.snapshotBackend.starts != 1 {
		t.Fatalf("pg_dump starts after interrupted publication = %d, want 1", f.snapshotBackend.starts)
	}
	// Effect completion committed before operation publication. Simulate the
	// post-crash host boot where the cgroup/containment namespace has gone away;
	// replay must use the exact authenticated output that completion bound.
	f.snapshotBackend.rebooted = true
	replayedOutput, err := f.p.SnapshotEffects.Manager.RetrieveSnapshotResult(ctx, record.Reservation, record.Execution, record.Completion.Verification.ResultReference)
	if err != nil || effect.DigestInput(replayedOutput) != record.Completion.Verification.ResultDigest {
		t.Fatalf("reboot replay result = %q, %v", replayedOutput, err)
	}

	// Idempotent request acceptance returns the original operation. Its later
	// claim recovers the completed effect and publishes the original artifact.
	replay, err := f.queueKey(t, "supervised-snapshot-publication-crash", "app.snapshot", map[string]interface{}{})
	if err != nil || !replay.Replayed || replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("snapshot replay acceptance = %+v, %v", replay, err)
	}
	result, err := f.execute(t, accepted.Operation.ID)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("replayed publication = %+v, %v", result, err)
	}
	if f.snapshotBackend.starts != 1 {
		t.Fatalf("replay launched pg_dump again: starts=%d", f.snapshotBackend.starts)
	}
	snapshot, _ := result.Metadata["snapshot"].(string)
	sidecar, err := readSidecar(snapshotLocation{dir: f.snapshots}, snapshot)
	if err != nil || sidecar == nil || len(sidecar.SHA256) != 64 || sidecar.Size <= 0 {
		t.Fatalf("replayed publication sidecar = %+v, %v", sidecar, err)
	}
}

// This deliberately pauses at os.Link while WithOperationClaimFence holds the
// operation row. A concurrent claim transition must remain blocked until the
// public sidecar/dump pair is complete; checking ownership before Link alone
// would let this test turn the claim over and then publish stale bytes.
func TestAttestedSnapshotPublicationHoldsClaimFenceThroughLink(t *testing.T) {
	server := pgtest.Start(t)
	controlDatabase, targetDatabase := "norn_snapshot_fence_control", "norn_snapshot_fence_target"
	server.CreateDatabase(t, controlDatabase)
	server.CreateDatabase(t, targetDatabase)
	t.Setenv("NORN_TEST_DATABASE_URL", server.URL(controlDatabase))
	t.Setenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL", server.URL(targetDatabase))
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "publication-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}

	originalLink := linkAttestedSnapshot
	enteredLink := make(chan struct{})
	releaseLink := make(chan struct{})
	linkAttestedSnapshot = func(oldname, newname string) error {
		close(enteredLink)
		<-releaseLink
		return originalLink(oldname, newname)
	}
	t.Cleanup(func() { linkAttestedSnapshot = originalLink })

	artifact := testAttestedArtifact(op.ID, []byte("attested snapshot bytes"), func() error { return nil })
	artifact.Fence = func(publish func() error) error {
		return f.db.WithOperationClaimFence(ctx, claim, publish)
	}
	location := testAttestedSnapshotLocation(t)
	published := make(chan error, 1)
	go func() {
		_, err := PublishAttestedSnapshot(location, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), artifact)
		published <- err
	}()
	<-enteredLink

	transitioned := make(chan error, 1)
	go func() {
		transitioned <- f.db.DeferClaimedOperation(ctx, claim, "claim turnover", time.Now().Add(-time.Second), nil)
	}()
	select {
	case err := <-transitioned:
		t.Fatalf("claim transitioned while Link was paused: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLink)
	if err := <-published; err != nil {
		t.Fatalf("publication = %v", err)
	}
	if err := <-transitioned; err != nil {
		t.Fatalf("claim transition after publication = %v", err)
	}
}

// The publication fence has only a row lock to release, so a caller deadline
// that fires immediately after the irreversible Link must not turn a complete
// public pair into a reported failure.
func TestAttestedSnapshotPublicationSurvivesFenceContextCancellationAfterLink(t *testing.T) {
	server := pgtest.Start(t)
	controlDatabase, targetDatabase := "norn_snapshot_cancel_control", "norn_snapshot_cancel_target"
	server.CreateDatabase(t, controlDatabase)
	server.CreateDatabase(t, targetDatabase)
	t.Setenv("NORN_TEST_DATABASE_URL", server.URL(controlDatabase))
	t.Setenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL", server.URL(targetDatabase))
	f := newTargetFixture(t)
	if _, err := f.db.ActivateDatabaseCatalog(context.Background(), 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(context.Background(), "publication-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}

	fenceCtx, cancelFence := context.WithCancel(context.Background())
	defer cancelFence()
	originalLink := linkAttestedSnapshot
	linkAttestedSnapshot = func(oldname, newname string) error {
		err := originalLink(oldname, newname)
		cancelFence()
		return err
	}
	t.Cleanup(func() { linkAttestedSnapshot = originalLink })
	artifact := testAttestedArtifact(op.ID, []byte("attested snapshot bytes"), func() error { return nil })
	artifact.Fence = func(publish func() error) error {
		return f.db.WithOperationClaimFence(fenceCtx, claim, publish)
	}
	location := testAttestedSnapshotLocation(t)
	published, err := PublishAttestedSnapshot(location, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), artifact)
	if err != nil {
		t.Fatalf("publication after Link cancellation = %v", err)
	}
	if err := verifyBoundDump(location, published.Filename, published.Size); err != nil {
		t.Fatalf("published pair after cancellation = %v", err)
	}
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
