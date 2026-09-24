package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type startupBackend struct{}

func (startupBackend) Start(context.Context, supervisor.BackendExecution, effect.LaunchMaterial) error {
	return errors.New("not used")
}
func (startupBackend) Observe(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (startupBackend) Revoke(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (startupBackend) RetrieveResult(context.Context, supervisor.BackendExecution, string) ([]byte, error) {
	return nil, errors.New("not used")
}

type startupSnapshotBackend struct{ startupBackend }

func (startupSnapshotBackend) StartSnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor, supervisor.SnapshotLaunchMaterial) error {
	return nil
}
func (startupSnapshotBackend) ObserveSnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (startupSnapshotBackend) QuerySnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor) (supervisor.SnapshotManifest, error) {
	return supervisor.SnapshotManifest{}, nil
}
func (startupSnapshotBackend) CopySnapshotArtifact(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor, io.Writer) (supervisor.SnapshotManifest, error) {
	return supervisor.SnapshotManifest{}, errors.New("not used")
}

// startupRebootedSnapshotBackend models the post-reboot state where the
// ephemeral cgroup tree disappeared. Its unknown observation must never be
// treated as proof that an unresolved helper stopped, but must not prevent
// cleanup of terminal effects already attested in the durable effect store.
type startupRebootedSnapshotBackend struct{ startupSnapshotBackend }

func (startupRebootedSnapshotBackend) ObserveSnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "rebooted-cgroup-missing"}, nil
}

func (startupRebootedSnapshotBackend) QuerySnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor) (supervisor.SnapshotManifest, error) {
	return supervisor.SnapshotManifest{}, errors.New("snapshot cgroup state disappeared after reboot")
}

type startupFailedSnapshotBackend struct{ startupSnapshotBackend }

func (startupFailedSnapshotBackend) ObserveSnapshot(context.Context, supervisor.BackendExecution, supervisor.SnapshotDescriptor) (supervisor.BackendState, error) {
	exit := 1
	return supervisor.BackendState{Phase: effect.SupervisorFailed, ExitCode: &exit, Output: []byte("pg_dump failed"), ContainmentProven: true, EvidenceReference: "startup-failed-snapshot"}, nil
}

func supervisedConfig(t *testing.T) *config.Config {
	return &config.Config{
		BuildTestExecution: buildTestSupervised, BuildTestTimeout: 30 * time.Minute, BuildTestPath: "/usr/bin:/bin",
		EffectSupervisorDir: filepath.Join(t.TempDir(), "supervisor"), EffectSigningKey: strings.Repeat("k", 32),
		EffectCgroupRoot: "/sys/fs/cgroup/norn-effects", EffectRunnerBinary: "/opt/norn/bin/norn-effect-runner",
	}
}

func TestBuildTestExecutionModeIsExplicitWithoutFallback(t *testing.T) {
	fakeDB := &store.DB{}
	succeed := func(string, string, string, []byte) (supervisor.Backend, error) { return startupBackend{}, nil }

	if effects, err := configureBuildTestEffects(&config.Config{BuildTestExecution: buildTestLegacyUnfenced}, fakeDB, succeed); err != nil || effects != nil {
		t.Fatalf("legacy mode = %+v, %v", effects, err)
	}
	if _, err := configureBuildTestEffects(&config.Config{BuildTestExecution: "auto"}, fakeDB, succeed); err == nil {
		t.Fatal("unknown execution mode accepted")
	}
	for name, mutate := range map[string]func(*config.Config){
		"relative root":  func(c *config.Config) { c.EffectSupervisorDir = "supervisor" },
		"short key":      func(c *config.Config) { c.EffectSigningKey = "short" },
		"missing cgroup": func(c *config.Config) { c.EffectCgroupRoot = "" },
		"no timeout":     func(c *config.Config) { c.BuildTestTimeout = 0 },
		"huge timeout":   func(c *config.Config) { c.BuildTestTimeout = 48 * time.Hour },
		"bad path":       func(c *config.Config) { c.BuildTestPath = "/bin\nX=1" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := supervisedConfig(t)
			mutate(cfg)
			if effects, err := configureBuildTestEffects(cfg, fakeDB, succeed); err == nil || effects != nil {
				t.Fatalf("incomplete supervised configuration = %+v, %v", effects, err)
			}
		})
	}
	backendErr := errors.New("runner handshake failed")
	if effects, err := configureBuildTestEffects(supervisedConfig(t), fakeDB, func(string, string, string, []byte) (supervisor.Backend, error) {
		return nil, backendErr
	}); !errors.Is(err, backendErr) || effects != nil {
		t.Fatalf("backend failure = %+v, %v", effects, err)
	}
}

func TestSupervisedBuildTestWiresRunnerBesideBinaryAndPrivateEnvironment(t *testing.T) {
	cfg := supervisedConfig(t)
	cfg.EffectRunnerBinary = ""
	t.Setenv("HOME", "/home/norn")
	t.Setenv("NORN_DATABASE_URL", "postgres://secret@db/norn")
	// pgxpool.New is lazy; nothing in this test connects to a database.
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var gotRunner, gotRoot string
	effects, err := configureBuildTestEffects(cfg, &store.DB{Pool: pool}, func(root, runner, _ string, _ []byte) (supervisor.Backend, error) {
		gotRoot, gotRunner = root, runner
		return startupBackend{}, nil
	})
	if err != nil || effects == nil || effects.Executor == nil || effects.Store == nil || effects.Descriptor == nil {
		t.Fatalf("supervised assembly = %+v, %v", effects, err)
	}
	if gotRoot != cfg.EffectCgroupRoot || filepath.Base(gotRunner) != effectRunnerName || !filepath.IsAbs(gotRunner) {
		t.Fatalf("backend root=%q runner=%q", gotRoot, gotRunner)
	}
	if strings.Join(effects.Environment, "\n") != "PATH=/usr/bin:/bin\nHOME=/home/norn" || effects.Timeout != cfg.BuildTestTimeout || effects.Supervisor != effectRunnerName {
		t.Fatalf("command environment/timeout = %v %s", effects.Environment, effects.Timeout)
	}
	if runtime.GOOS != "linux" {
		if _, err := supervisor.NewCgroupBackend(cfg.EffectCgroupRoot, gotRunner, "", []byte(cfg.EffectSigningKey)); err == nil {
			t.Fatal("cgroup backend constructed on a platform without qualified containment")
		}
	}
}

func TestConfigureSnapshotEffectsReconcilesPublishedArtifactAfterRestart(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_reconcile")
	db, err := store.Connect(server.URL("norn_snapshot_reconcile"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	key := strings.Repeat("k", 32)
	manager, err := supervisor.NewManager(filepath.Join(root, "snapshots"), []byte(key), startupSnapshotBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	operation := &model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: "demo", Status: model.OperationQueued, Source: "test", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1}
	if err := db.InsertOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "snapshot-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil {
		t.Fatalf("claim snapshot operation = %+v, %v", claimed, err)
	}
	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	material := supervisor.SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/demo/snapshot", OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: "app.snapshot", Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-restart", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	effects, err := store.NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("reserve = %+v, %v", reserved, err)
	}
	identity, err := manager.LaunchSnapshot(ctx, reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	if err := effects.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: reservation.InputDigest, ResultDigest: effect.DigestInput([]byte("manifest")), ResultReference: "result/" + reservation.SupervisorExecutionID, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID, EvidenceSource: "test", EvidenceReference: "test/" + identity.RuntimeInstanceID, ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "published", nil); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(reservation.SupervisorExecutionID))
	directory := filepath.Join(root, "snapshots", hex.EncodeToString(digest[:]))
	private := filepath.Join(directory, ".snapshot-published")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(private, "archive.dump")
	if err := os.WriteFile(archive, []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Replica B shares the control database but owns a different node-local
	// supervisor root. Its completed record and private artifact must not be
	// inspected or block replica A's startup reconciliation.
	foreignRoot := filepath.Join(root, "foreign-snapshots")
	foreignManager, err := supervisor.NewManager(foreignRoot, []byte(key), startupSnapshotBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if err := foreignManager.SetSnapshotArtifactBudget(supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	foreignOperation := &model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: "foreign", Status: model.OperationQueued, Source: "test", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1}
	if err := db.InsertOperation(ctx, foreignOperation); err != nil {
		t.Fatal(err)
	}
	foreignClaimed, foreignClaim, err := db.ClaimNextOperation(ctx, "foreign-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || foreignClaimed == nil || foreignClaimed.ID != foreignOperation.ID {
		t.Fatalf("claim foreign snapshot operation = %+v, %v", foreignClaimed, err)
	}
	foreignPayload, err := foreignManager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	foreignReservation := effect.Reservation{Authority: authority, Resource: "app/foreign/snapshot", OperationClaim: effect.OperationClaim{OperationID: foreignClaim.OperationID(), OwnerID: foreignClaim.OwnerID(), Generation: foreignClaim.Generation()}, Stage: "app.snapshot", Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-foreign", LaunchPayload: foreignPayload}
	foreignReservation.InputDigest, err = effect.ComputeInputDigest(foreignReservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreignManager.Prepare(ctx, foreignReservation); err != nil {
		t.Fatal(err)
	}
	foreignReserved, err := effects.Reserve(ctx, foreignReservation)
	if err != nil || !foreignReserved.Created {
		t.Fatalf("reserve foreign snapshot = %+v, %v", foreignReserved, err)
	}
	foreignIdentity, err := foreignManager.LaunchSnapshot(ctx, foreignReservation, material)
	if err != nil {
		t.Fatal(err)
	}
	if err := effects.MarkLaunched(ctx, foreignReserved.Record.Token, foreignIdentity); err != nil {
		t.Fatal(err)
	}
	foreignVerification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: foreignReservation.InputDigest, ResultDigest: effect.DigestInput([]byte("manifest")), ResultReference: "result/" + foreignReservation.SupervisorExecutionID, SupervisorExecutionID: foreignReservation.SupervisorExecutionID, RuntimeInstanceID: foreignIdentity.RuntimeInstanceID, EvidenceSource: "test", EvidenceReference: "test/" + foreignIdentity.RuntimeInstanceID, ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, foreignReserved.Record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: foreignVerification}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishClaimedOperation(ctx, foreignClaim, model.OperationSucceeded, "published", nil); err != nil {
		t.Fatal(err)
	}
	foreignDigest := sha256.Sum256([]byte(foreignReservation.SupervisorExecutionID))
	foreignPrivate := filepath.Join(foreignRoot, hex.EncodeToString(foreignDigest[:]), ".snapshot-published")
	if err := os.MkdirAll(foreignPrivate, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignArchive := filepath.Join(foreignPrivate, "archive.dump")
	if err := os.WriteFile(foreignArchive, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SnapshotExecution: "supervised", EffectSupervisorDir: root, EffectSigningKey: key, EffectCgroupRoot: "/test/cgroup", EffectRunnerBinary: "/test/runner", SnapshotPGDumpPath: "/usr/bin/pg_dump", SnapshotPGDumpSHA256: strings.Repeat("a", 64), SnapshotTimeout: time.Minute, SnapshotArtifactBudgetBytes: supervisor.MaxSnapshotArtifactBytes}
	if _, err := configureSnapshotEffects(cfg, db, func(string, string, string, []byte) (supervisor.Backend, error) {
		return startupRebootedSnapshotBackend{}, nil
	}); err != nil {
		t.Fatalf("startup snapshot reconciliation = %v", err)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("published private archive survived startup reconciliation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); !os.IsNotExist(err) {
		t.Fatalf("snapshot admission survived startup reconciliation: %v", err)
	}
	if _, err := os.Lstat(foreignArchive); err != nil {
		t.Fatalf("replica A touched replica B private archive: %v", err)
	}
}

func TestConfigureSnapshotEffectsReleasesVerifiedFailedAdmissionAfterRestart(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_failed_reconcile")
	db, err := store.Connect(server.URL("norn_snapshot_failed_reconcile"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, key := t.TempDir(), strings.Repeat("k", 32)
	manager, err := supervisor.NewManager(filepath.Join(root, "snapshots"), []byte(key), startupFailedSnapshotBackend{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(supervisor.MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: "demo", Status: model.OperationQueued, Source: "test", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: time.Now().UTC(), MaxAttempts: 1}
	if err := db.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "snapshot-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil {
		t.Fatalf("claim snapshot operation = %+v, %v", claimed, err)
	}
	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	material := supervisor.SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/demo/snapshot", OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: "app.snapshot", Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-failed-restart", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	effects, err := store.NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("reserve = %+v, %v", reserved, err)
	}
	identity, err := manager.LaunchSnapshot(ctx, reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	if err := effects.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationFailed, InputDigest: reservation.InputDigest, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID, EvidenceSource: "test", EvidenceReference: "failed/" + identity.RuntimeInstanceID, ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(reservation.SupervisorExecutionID))
	directory := filepath.Join(root, "snapshots", hex.EncodeToString(digest[:]))
	if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); err != nil {
		t.Fatalf("failed snapshot did not reserve admission: %v", err)
	}
	private := filepath.Join(directory, ".snapshot-failed")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"archive.dump", "service.conf", "passfile"} {
		if err := os.WriteFile(filepath.Join(private, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{SnapshotExecution: "supervised", EffectSupervisorDir: root, EffectSigningKey: key, EffectCgroupRoot: "/test/cgroup", EffectRunnerBinary: "/test/runner", SnapshotPGDumpPath: "/usr/bin/pg_dump", SnapshotPGDumpSHA256: strings.Repeat("a", 64), SnapshotTimeout: time.Minute, SnapshotArtifactBudgetBytes: supervisor.MaxSnapshotArtifactBytes}
	if _, err := configureSnapshotEffects(cfg, db, func(string, string, string, []byte) (supervisor.Backend, error) {
		return startupRebootedSnapshotBackend{}, nil
	}); err != nil {
		t.Fatalf("failed snapshot startup reconciliation = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); !os.IsNotExist(err) {
		t.Fatalf("verified failed snapshot admission survived restart: %v", err)
	}
	for _, name := range []string{"archive.dump", "service.conf", "passfile"} {
		if _, err := os.Lstat(filepath.Join(private, name)); !os.IsNotExist(err) {
			t.Fatalf("failed snapshot private material %s survived restart cleanup: %v", name, err)
		}
	}
}
