package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/nomad"
)

type sourceAccountLockerFunc func(context.Context, database.ResolvedBinding, database.MySQLMaintenanceCredentials, database.SecretSource) error

func (f sourceAccountLockerFunc) FenceMySQLRuntimeAccount(ctx context.Context, resolved database.ResolvedBinding, maintenance database.MySQLMaintenanceCredentials, secrets database.SecretSource) error {
	return f(ctx, resolved, maintenance, secrets)
}

type sourceSecretSource struct{}

func (sourceSecretSource) Resolve(context.Context, string) ([]byte, error) {
	return nil, errors.New("unused by fake locker")
}

type sourceStopperFunc func(context.Context, nomad.CASStopJobRequest) error

func (f sourceStopperFunc) StopJobCAS(ctx context.Context, request nomad.CASStopJobRequest) error {
	return f(ctx, request)
}

type sourceStagerFunc func(context.Context, database.ResolvedBinding, database.TargetIdentity, database.SecretSource, string, string, string) (string, database.MySQLSQLArtifact, error)

func (f sourceStagerFunc) Stage(ctx context.Context, binding database.ResolvedBinding, target database.TargetIdentity, secrets database.SecretSource, tool, digest, directory string) (string, database.MySQLSQLArtifact, error) {
	return f(ctx, binding, target, secrets, tool, digest, directory)
}

type sourceRetentionStore struct {
	objects        map[string]artifactstore.Descriptor
	publishErr     error
	persistOnError bool
	publishes      int
	verifies       int
}

func (s *sourceRetentionStore) Publish(_ context.Context, expected artifactstore.Descriptor, source io.Reader) (artifactstore.Descriptor, error) {
	s.publishes++
	if _, err := io.Copy(io.Discard, source); err != nil {
		return artifactstore.Descriptor{}, err
	}
	if s.objects == nil {
		s.objects = map[string]artifactstore.Descriptor{}
	}
	if s.publishErr == nil || s.persistOnError {
		s.objects[expected.Key] = expected
	}
	if s.publishErr != nil {
		return artifactstore.Descriptor{}, s.publishErr
	}
	return expected, nil
}

func (s *sourceRetentionStore) Open(context.Context, artifactstore.Descriptor) (io.ReadCloser, error) {
	return nil, artifactstore.ErrArtifactNotFound
}

func (s *sourceRetentionStore) Materialize(context.Context, artifactstore.Descriptor, io.Writer) error {
	return artifactstore.ErrArtifactNotFound
}

func (s *sourceRetentionStore) Verify(_ context.Context, expected artifactstore.Descriptor) error {
	s.verifies++
	if got, ok := s.objects[expected.Key]; !ok || got != expected {
		return artifactstore.ErrArtifactNotFound
	}
	return nil
}

func TestMySQLSourceSnapshotPayloadPreservesLargeIntegers(t *testing.T) {
	request := MySQLSourceSnapshotRequest{
		CatalogRevision: 9007199254740993,
		Source:          database.TargetIdentity{ServiceGeneration: 9007199254740993},
		JobIdentity:     MySQLSourceSnapshotJobIdentity{JobModifyIndex: "9007199254740993"},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodeExactJSONObject(encoded)
	if err != nil || !sameMySQLSourceSnapshotPayload(payload, request) {
		t.Fatalf("large signed integers did not compare exactly: %v", err)
	}
	request.CatalogRevision--
	if sameMySQLSourceSnapshotPayload(payload, request) {
		t.Fatal("changed large catalog revision compared equal")
	}
}

func TestMySQLSourceSnapshotIntentReservesSignedPhysicalSource(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	db, acceptedStore := dbs[0], stores[0]
	ctx := context.Background()
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{
		APIVersion: database.APIVersion, ID: "snapshot-mysql", Generation: 1,
		Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:snapshot-mysql",
		Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot}},
	})
	aliasService := catalog.Services[len(catalog.Services)-1]
	aliasService.ID = "snapshot-mysql-alias"
	catalog.Services = append(catalog.Services, aliasService)
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "snapshot_reader", SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:snapshot/reader", RestoreRole: "snapshot_restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:snapshot/restore", FenceRole: "snapshot_fence", FenceCredentialRef: "secret:snapshot/fence", FenceAccountHost: "%"}
	catalog.Bindings = append(catalog.Bindings, database.DatabaseBinding{APIVersion: database.APIVersion, ID: "snapshot-source", ServiceID: "snapshot-mysql", Database: "wordpress", Role: "snapshot_runtime", Generation: 1, CredentialRef: "secret:snapshot/runtime", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["snapshot-source"] = "snapshot-source"
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "snapshot-source"})
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLSourceSnapshotRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "snapshot-source", Source: source.Target, Maintenance: *source.MySQLMaintenance,
		JobIdentity: validSourceSnapshotJobIdentity("wordpress", active.Revision, "7", "alloc-1"), DumpToolSHA256: strings.Repeat("a", 64)}
	accept := func(key string, body MySQLSourceSnapshotRequest) OperationClaim {
		t.Helper()
		input := newAcceptance(t, acceptedStore, key, "operator", "wordpress", false)
		input.Identity.Kind, input.Identity.Resource = MySQLSourceSnapshotOperationKind, "mysql/wordpress"
		input.Operation.Kind, input.Operation.MaxAttempts = MySQLSourceSnapshotOperationKind, 1
		encoded, _ := json.Marshal(body)
		input.Operation.Payload = nil
		if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
			t.Fatal(err)
		}
		input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := acceptedStore.Accept(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := NewOperationClaim(accepted.Operation.ID, "snapshot-worker", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1, locked_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, claim.OperationID(), claim.OwnerID()); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	claim := accept("snapshot-"+uuid.NewString(), request)
	wrongJob := request
	wrongJob.JobIdentity.App, wrongJob.JobIdentity.JobID = "another-app", "another-app"
	wrongClaim := accept("snapshot-"+uuid.NewString(), wrongJob)
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, wrongClaim, wrongJob); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("source snapshot stopped a job outside the accepted app: %v", err)
	}
	const priorLaunch = "source-snapshot-prior-launch"
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, priorLaunch, []database.TargetIdentity{source.Target}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("source intent crossed existing runtime launch: %v", err)
	}
	if err := db.ReleaseMySQLRuntimeLaunchNeverStarted(ctx, priorLaunch, MySQLRuntimeLaunchNoStartProof{ObservedAt: time.Now().UTC(), Method: "disposable test inspected absent supervisor instance", EvidenceSHA256: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	prepared, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request)
	if err != nil || prepared.State != "quiesce-intended" || prepared.Replayed {
		t.Fatalf("first signed source intent=%+v err=%v", prepared, err)
	}
	if replay, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request); err != nil || !replay.Replayed {
		t.Fatalf("exact replay=%+v err=%v", replay, err)
	}
	changed := request
	changed.JobIdentity.JobModifyIndex = "8"
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, changed); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("changed job revision accepted: %v", err)
	}
	second := accept("snapshot-"+uuid.NewString(), request)
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, second, request); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("second operation reused physical source: %v", err)
	}
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "snapshot-race", []database.TargetIdentity{source.Target}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("runtime launch passed source fence: %v", err)
	}
	aliasSource := source.Target
	aliasSource.ServiceID, aliasSource.BindingID, aliasSource.Role = "snapshot-mysql-alias", "snapshot-source-alias", "snapshot_runtime_alias"
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "snapshot-alias-race", []database.TargetIdentity{aliasSource}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("catalog service alias passed source fence: %v", err)
	}
	rotatedSource := source.Target
	rotatedSource.ServiceGeneration++
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "snapshot-generation-race", []database.TargetIdentity{rotatedSource}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("service generation rotation passed source fence: %v", err)
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, active.Revision, catalog, "operator"); !errors.Is(err, ErrMySQLRestoreMaintenanceFence) {
		t.Fatalf("catalog activation passed source fence: %v", err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "quiesce-intended" {
		t.Fatalf("source fence after competing operations = %q, %v", state, err)
	}
	called := 0
	stopper := sourceStopperFunc(func(ctx context.Context, got nomad.CASStopJobRequest) error {
		called++
		if got.JobID != "wordpress" || got.Region != "global" || got.JobVersion != 1 || got.JobModifyIndex != 7 || len(got.AllocationIDs) != 1 || got.AllocationIDs[0] != "alloc-1" ||
			got.DeploymentID != request.JobIdentity.DeploymentID || got.SpecDigest != request.JobIdentity.SpecDigest || got.DatabaseBindingSchema != request.JobIdentity.DatabaseBindingSchema ||
			got.DatabaseBindingSHA256 != request.JobIdentity.DatabaseBindingSHA256 || got.DatabaseCatalogRevision != request.JobIdentity.DatabaseCatalogRevision {
			t.Fatalf("Nomad stop request was not signed identity: %+v", got)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "stop-intended" {
			t.Fatalf("external stop preceded durable checkpoint: state=%q err=%v", state, err)
		}
		return nil
	})
	if err := db.StopClaimedMySQLSourceJob(ctx, acceptedStore, claim, request, stopper); err != nil {
		t.Fatal(err)
	}
	var intended, proved time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT state,stop_intended_at,stop_proved_at FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state, &intended, &proved); err != nil || state != "stop-proved" || proved.Before(intended) {
		t.Fatalf("stop proof not durable: state=%q intended=%v proved=%v err=%v", state, intended, proved, err)
	}
	var fenceEpoch int64
	var fenceOwner string
	var fenceActive bool
	if err := db.Pool.QueryRow(ctx, `SELECT s.runtime_fence_epoch,s.runtime_fence_owner,f.active FROM mysql_source_snapshot_intents s CROSS JOIN runtime_mutation_fence f WHERE s.operation_id=$1`, claim.OperationID()).Scan(&fenceEpoch, &fenceOwner, &fenceActive); err != nil || fenceEpoch <= 0 || fenceOwner != "mysql-source-snapshot:"+claim.OperationID() || !fenceActive {
		t.Fatalf("source stop lacked durable runtime fence: epoch=%d owner=%q active=%v err=%v", fenceEpoch, fenceOwner, fenceActive, err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, RuntimeMutationFence{Epoch: fenceEpoch, Owner: fenceOwner}); !errors.Is(err, ErrRuntimeMutationFenceOwnershipLost) {
		t.Fatalf("generic fence release bypassed reserved source: %v", err)
	}
	lockCalls := 0
	locker := sourceAccountLockerFunc(func(_ context.Context, got database.ResolvedBinding, gotMaintenance database.MySQLMaintenanceCredentials, _ database.SecretSource) error {
		lockCalls++
		if got.Target != source.Target || gotMaintenance != request.Maintenance {
			t.Fatalf("locker received substituted identity: %+v %+v", got.Target, gotMaintenance)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "lock-intended" {
			t.Fatalf("MySQL effect preceded durable lock intent: %q %v", state, err)
		}
		return nil
	})
	if err := db.LockClaimedMySQLSourceAccount(ctx, acceptedStore, claim, request, sourceSecretSource{}, locker); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "lock-proved" {
		t.Fatalf("lock proof not durable: %q %v", state, err)
	}
	if err := db.LockClaimedMySQLSourceAccount(ctx, acceptedStore, claim, request, sourceSecretSource{}, locker); !errors.Is(err, ErrMySQLSourceAccountLockIndeterminate) || lockCalls != 1 {
		t.Fatalf("replay repeated MySQL lock: err=%v calls=%d", err, lockCalls)
	}
	if err := db.StopClaimedMySQLSourceJob(ctx, acceptedStore, claim, request, stopper); err == nil || called != 1 {
		t.Fatalf("replay repeated external stop: err=%v calls=%d", err, called)
	}
	stageDirectory := t.TempDir()
	bytes := []byte("-- bounded disposable SQL artifact\n")
	checksum := sha256.Sum256(bytes)
	artifact := database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2, Source: source.Target, Bytes: int64(len(bytes)), SHA256: hex.EncodeToString(checksum[:]),
		Expectation: database.MySQLRestoreExpectation{SchemaSHA256: strings.Repeat("a", 64), DataSHA256: strings.Repeat("b", 64), TableCount: 0}}
	stageCalls := 0
	stager := sourceStagerFunc(func(_ context.Context, got database.ResolvedBinding, target database.TargetIdentity, _ database.SecretSource, _, digest, directory string) (string, database.MySQLSQLArtifact, error) {
		stageCalls++
		if got.Target != source.Target || got.MySQLMaintenance == nil || *got.MySQLMaintenance != request.Maintenance || target != source.Target || digest != request.DumpToolSHA256 || directory != stageDirectory {
			t.Fatalf("stager received substituted source: %+v %+v %s", got, target, digest)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "stage-intended" {
			t.Fatalf("dump ran before durable stage intent: %q %v", state, err)
		}
		path := filepath.Join(directory, "dump.sql")
		if err := os.WriteFile(path, bytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return path, artifact, nil
	})
	signed, err := db.StageClaimedMySQLSourceArtifact(ctx, acceptedStore, claim, request, sourceSecretSource{}, "/usr/bin/true", stageDirectory, stager)
	if err != nil || signed.Receipt.Artifact != artifact || signed.Receipt.AcceptanceIntentID != prepared.AcceptanceIntentID || stageCalls != 1 {
		t.Fatalf("signed artifact receipt=%+v err=%v calls=%d", signed, err, stageCalls)
	}
	loaded, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptedStore, claim.OperationID())
	if err != nil || loaded.SHA256 != signed.SHA256 || loaded.Signature != signed.Signature {
		t.Fatalf("persisted signed receipt=%+v err=%v", loaded, err)
	}
	restore := MySQLRestoreRequest{CatalogRevision: active.Revision, Artifact: artifact, ArtifactPath: signed.Receipt.ArtifactPath,
		SourceArtifact: MySQLRestoreSourceArtifact{OperationID: claim.OperationID(), ReceiptSHA256: signed.SHA256}}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptedStore, restore); err != nil {
		t.Fatalf("valid signed source receipt was rejected: %v", err)
	}
	_ = tx.Rollback(ctx)
	forged := restore
	forged.SourceArtifact.ReceiptSHA256 = strings.Repeat("f", 64)
	tx, err = db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptedStore, forged); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("forged receipt digest qualified restore: %v", err)
	}
	_ = tx.Rollback(ctx)
	if err := os.WriteFile(signed.Receipt.ArtifactPath, []byte("-- changed after receipt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptedStore, restore); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("changed retained artifact bytes qualified restore: %v", err)
	}
	_ = tx.Rollback(ctx)
	if err := os.WriteFile(signed.Receipt.ArtifactPath, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// A lost upload response is ambiguous. The retention path verifies the
	// exact content-addressed object before it signs retained-proved, and it
	// never asks the stager to run the dump again.
	objects := &sourceRetentionStore{publishErr: errors.New("upload response lost")}
	if _, err := db.RetainClaimedMySQLSourceArtifact(ctx, acceptedStore, claim, objects); !errors.Is(err, ErrMySQLSourceArtifactRetentionIndeterminate) || objects.publishes != 1 || stageCalls != 1 {
		t.Fatalf("missing ambiguous upload proof: err=%v publishes=%d stageCalls=%d", err, objects.publishes, stageCalls)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "publish-intended" {
		t.Fatalf("ambiguous upload did not remain publish-intended: state=%q err=%v", state, err)
	}
	descriptor := artifactstore.Descriptor{Key: artifactstore.KeyForSHA256(artifact.SHA256), SHA256: artifact.SHA256, Size: artifact.Bytes}
	objects.objects = map[string]artifactstore.Descriptor{descriptor.Key: descriptor}
	retained, err := db.RetainClaimedMySQLSourceArtifact(ctx, acceptedStore, claim, objects)
	if err != nil || retained.Receipt.StagingReceiptSHA256 != signed.SHA256 || retained.Receipt.Artifact.Key != descriptor.Key || objects.publishes != 1 || objects.verifies < 2 || stageCalls != 1 {
		t.Fatalf("retention after ambiguous upload=%+v err=%v publishes=%d verifies=%d stageCalls=%d", retained, err, objects.publishes, objects.verifies, stageCalls)
	}
	loadedRetention, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptedStore, claim.OperationID())
	if err != nil || loadedRetention.SHA256 != retained.SHA256 || loadedRetention.Signature != retained.Signature {
		t.Fatalf("persisted v2 retention receipt=%+v err=%v", loadedRetention, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "retained-proved" {
		t.Fatalf("retention proof state=%q err=%v", state, err)
	}
	if err := os.Remove(signed.Receipt.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	retainedObjects, err := artifactstore.OpenLocal(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer retainedObjects.Close()
	if _, err := retainedObjects.Publish(ctx, descriptor, strings.NewReader(string(bytes))); err != nil {
		t.Fatal(err)
	}
	restoreInput := newAcceptance(t, acceptedStore, "retained-restore-"+uuid.NewString(), "operator", "target", false)
	restoreInput.Identity.Kind, restoreInput.Identity.Resource = MySQLRestoreOperationKind, "mysql/target"
	restoreInput.Operation.Kind, restoreInput.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encodedRestore, _ := json.Marshal(restore)
	restoreInput.Operation.Payload = nil
	if err := json.Unmarshal(encodedRestore, &restoreInput.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	restoreInput.Fingerprint, err = CanonicalOperationRequestFingerprint(restoreInput)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRestore, err := acceptedStore.Accept(ctx, restoreInput)
	if err != nil {
		t.Fatal(err)
	}
	restoreClaim, err := NewOperationClaim(acceptedRestore.Operation.ID, "restore-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running',attempts=1,locked_by=$2,lock_generation=1,
		locked_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, restoreClaim.OperationID(), restoreClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	materializeDirectory := filepath.Join(t.TempDir(), "materialized")
	if err := os.Mkdir(materializeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	materialized, err := db.MaterializeClaimedMySQLRestoreArtifact(ctx, acceptedStore, restoreClaim, retainedObjects, materializeDirectory)
	if err != nil {
		t.Fatalf("retained materialization after stage removal: %v", err)
	}
	if content, err := os.ReadFile(materialized); err != nil || string(content) != string(bytes) {
		t.Fatalf("materialized retained bytes=%q err=%v", content, err)
	}
	_ = os.Remove(materialized)
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET retention_receipt_canonical=retention_receipt_canonical || decode('20','hex') WHERE operation_id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MaterializeClaimedMySQLRestoreArtifact(ctx, acceptedStore, restoreClaim, retainedObjects, materializeDirectory); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("tampered retained receipt qualified restore: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET retention_receipt_canonical=$2 WHERE operation_id=$1`, claim.OperationID(), loadedRetention.CanonicalBytes); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptedStore, restore); err != nil {
		t.Fatalf("retained receipt did not qualify after staged file removal: %v", err)
	}
	_ = tx.Rollback(ctx)
	if _, err := db.StageClaimedMySQLSourceArtifact(ctx, acceptedStore, claim, request, sourceSecretSource{}, "/usr/bin/true", stageDirectory, stager); err == nil || stageCalls != 1 {
		t.Fatalf("replay repeated dump: err=%v calls=%d", err, stageCalls)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET artifact_receipt_canonical=artifact_receipt_canonical || decode('20','hex') WHERE operation_id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptedStore, claim.OperationID()); !errors.Is(err, ErrMySQLSourceArtifactIndeterminate) {
		t.Fatalf("tampered persisted receipt verified: %v", err)
	}
}
