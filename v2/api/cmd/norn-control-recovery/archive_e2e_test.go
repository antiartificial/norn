package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/archive"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/internal/s3emulator"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

const archiveSigningKey = "archive-recovery-test-signing-key-0000000000"

func archiveControlDB(t *testing.T, server *pgtest.Server, name string) *store.DB {
	t.Helper()
	server.CreateDatabase(t, name)
	config, err := pgxpool.ParseConfig(server.URL(name))
	if err != nil {
		t.Fatal(err)
	}
	store.DeclareReaderContract(config, "recovery-test")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// archivedSaga accepts a signed operation, records saga events, finishes it
// and archives and prunes its evidence, leaving it only in the archive.
func archivedSaga(t *testing.T, db *store.DB, objects archive.Store) string {
	t.Helper()
	ctx := context.Background()
	signer, err := store.NewHMACAcceptanceSigner(archiveSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operations.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hot := saga.NewPostgresStore(db.Pool)
	pipe := &pipeline.Pipeline{DB: db, SagaStore: hot, OperationStore: operations, AppsDir: t.TempDir()}
	request := pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "archive-" + uuid.NewString(),
		Audit: store.AcceptanceAuditContext{Source: "recovery-test"}}
	accepted, err := pipe.QueueOperation(ctx, model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: "recovered", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Source: "control-api", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1}, request)
	if err != nil {
		t.Fatal(err)
	}
	log := saga.NewWithID(hot, accepted.Operation.SagaID, "recovered", "pipeline", "snapshot")
	for _, message := range []string{"one", "two", "three"} {
		if err := log.Log(ctx, "step.progress", message, nil); err != nil {
			t.Fatal(err)
		}
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "recovery-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "done", nil); err != nil {
		t.Fatal(err)
	}
	archiver := &retention.Archiver{DB: db, Archive: objects, Signer: signer, Mode: retention.ModePrune, BatchSize: 10}
	if report, err := archiver.RunOnce(ctx); err != nil || report.Published != 1 || report.Pruned != 3 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	return accepted.Operation.SagaID
}

func recoveryKeyFiles(t *testing.T, directory string, hmacKey string) (identityPath, keysPath string) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	document, _ := json.Marshal(map[string][]string{"hmacKeys": {base64.RawStdEncoding.EncodeToString([]byte(hmacKey))}})
	identityPath = writeFile(t, filepath.Join(directory, "identity-"+uuid.NewString()), []byte(identity.String()+"\n"), 0o600)
	keysPath = writeFile(t, filepath.Join(directory, "keys-"+uuid.NewString()+".age"), encryptTo(t, identity.Recipient(), document), 0o600)
	return identityPath, keysPath
}

// The offline tool verifies a local archive (content binding plus
// recovered-key signatures), rebuilds a fresh control schema's index from the
// archive alone so pruned history is readable again, and fails visibly on
// corruption or the wrong keys.
func TestArchiveRecoveryCommandsVerifyAndReindexWithoutHistoricalDatabase(t *testing.T) {
	server := pgtest.Start(t)
	source := archiveControlDB(t, server, "source_control")
	directory := t.TempDir()
	root := filepath.Join(directory, "evidence")
	objects, err := archive.OpenLocal(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	sagaID := archivedSaga(t, source, objects)
	local := []string{"--archive-backend", "local", "--archive-dir", root}

	result := runCLI(t, append([]string{"archive-verify"}, local...)...)
	var report retention.ArchiveVerification
	if result.err != nil || json.Unmarshal(result.stdout, &report) != nil || report.Verified != 1 || report.Events != 3 || report.SignaturesVerified {
		t.Fatalf("verify without keys = %s %s %v", result.stdout, result.stderr, result.err)
	}
	identity, keys := recoveryKeyFiles(t, directory, archiveSigningKey)
	result = runCLI(t, append([]string{"archive-verify", "--identity-file", identity, "--recovery-keys-file", keys}, local...)...)
	if result.err != nil || json.Unmarshal(result.stdout, &report) != nil || report.Verified != 1 || !report.SignaturesVerified {
		t.Fatalf("verify with recovered keys = %s %s %v", result.stdout, result.stderr, result.err)
	}
	wrongIdentity, wrongKeys := recoveryKeyFiles(t, directory, "some-other-acceptance-key-00000000000000")
	if result = runCLI(t, append([]string{"archive-verify", "--identity-file", wrongIdentity, "--recovery-keys-file", wrongKeys}, local...)...); result.err == nil || !strings.Contains(string(result.stdout), "rejected") {
		t.Fatalf("verify with the wrong keys = %s %s %v", result.stdout, result.stderr, result.err)
	}

	// Restore into a fresh control database with no historical rows.
	restored := archiveControlDB(t, server, "restored_control")
	dsn := writeFile(t, filepath.Join(directory, "restored.url"), []byte(server.URL("restored_control")), 0o600)
	result = runCLI(t, append([]string{"archive-reindex", "--database-url-file", dsn, "--identity-file", identity, "--recovery-keys-file", keys}, local...)...)
	var recovery retention.IndexRecovery
	if result.err != nil || json.Unmarshal(result.stdout, &recovery) != nil || recovery.Restored != 1 {
		t.Fatalf("reindex = %s %s %v", result.stdout, result.stderr, result.err)
	}
	history := &retention.HistoryStore{Hot: saga.NewPostgresStore(restored.Pool), DB: restored, Archive: objects}
	if events, err := history.ListBySaga(context.Background(), sagaID); err != nil || len(events) != 3 || events[2].Message != "three" {
		t.Fatalf("restored history = %+v, %v", events, err)
	}
	if again := runCLI(t, append([]string{"archive-reindex", "--database-url-file", dsn}, local...)...); again.err != nil || !strings.Contains(string(again.stdout), `"existing":1`) {
		t.Fatalf("repeated reindex = %s %s %v", again.stdout, again.stderr, again.err)
	}

	// Corruption fails verification visibly (non-zero exit, named object).
	keysOnDisk, _ := objects.List(context.Background(), "evidence/")
	path := filepath.Join(root, filepath.FromSlash(keysOnDisk[0]))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"one"`, `"ONE"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if result = runCLI(t, append([]string{"archive-verify"}, local...)...); result.err == nil || !strings.Contains(string(result.stdout), keysOnDisk[0]) {
		t.Fatalf("verify of a corrupted archive = %s %s %v", result.stdout, result.stderr, result.err)
	}
}

// The same commands open the Fleet object profile (emulated here; this is
// not qualification of any real object service).
func TestArchiveVerifyOpensTheObjectProfile(t *testing.T) {
	server := pgtest.Start(t)
	source := archiveControlDB(t, server, "object_source")
	emulator, objectServer := s3emulator.Start("norn-evidence", "archive-reader")
	defer objectServer.Close()
	endpoint, _ := url.Parse(objectServer.URL)
	objects, err := archive.OpenObjectStore(context.Background(), archive.ObjectStoreConfig{Endpoint: endpoint.Host, Bucket: "norn-evidence", Prefix: "fleet",
		Region: "us-east-1", AccessKey: "archive-reader", SecretKey: "secret", Transport: objectServer.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	archivedSaga(t, source, objects)
	directory := t.TempDir()
	flags := []string{"archive-verify", "--archive-backend", "object", "--archive-endpoint", endpoint.Host, "--archive-bucket", "norn-evidence", "--archive-prefix", "fleet",
		"--archive-region", "us-east-1",
		"--archive-access-key-file", writeFile(t, filepath.Join(directory, "access"), []byte("archive-reader"), 0o600),
		"--archive-secret-key-file", writeFile(t, filepath.Join(directory, "secret"), []byte("secret"), 0o600),
		"--archive-ca-file", writeFile(t, filepath.Join(directory, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: objectServer.Certificate().Raw}), 0o600)}
	var putsBefore int
	emulator.Configure(func(e *s3emulator.Emulator) {
		putsBefore = e.Requests["PUT"]
		e.FailWrites = true
	})
	result := runCLI(t, flags...)
	var report retention.ArchiveVerification
	if result.err != nil || json.Unmarshal(result.stdout, &report) != nil || report.Verified != 1 {
		t.Fatalf("object verify = %s %s %v", result.stdout, result.stderr, result.err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) {
		if e.Requests["PUT"] != putsBefore {
			t.Fatalf("read-only verify issued %d PUTs", e.Requests["PUT"]-putsBefore)
		}
	})
	emulator.Configure(func(e *s3emulator.Emulator) { e.CorruptReads = true })
	if result = runCLI(t, flags...); result.err == nil {
		t.Fatalf("corrupted object reads verified: %s", result.stdout)
	}
}
