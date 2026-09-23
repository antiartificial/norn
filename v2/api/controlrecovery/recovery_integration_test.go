package controlrecovery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

const recoveryCanary = "NORN_RECOVERY_CANARY_SECRET_61c3"

func TestCreateVerifyRestorePassiveRoundTrip(t *testing.T) {
	targetURL := os.Getenv("NORN_TEST_RECOVERY_TARGET_DATABASE_URL")
	if targetURL == "" {
		t.Skip("NORN_TEST_RECOVERY_TARGET_DATABASE_URL is not set")
	}
	source, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	hmacKey := []byte("recovery-roundtrip-hmac-key-32-bytes-" + recoveryCanary)
	seedAuthenticRecoveryEvidence(t, ctx, source, hmacKey)

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewManifestSigner([]byte(base64.RawStdEncoding.EncodeToString(private)))
	if err != nil {
		t.Fatal(err)
	}
	failedOutput := filepath.Join(t.TempDir(), "pg-dump-failed.age")
	if _, err := CreateBundle(ctx, CreateBundleOptions{
		Pool: source, DatabaseURL: os.Getenv("NORN_TEST_DATABASE_URL"), Schema: schema, OutputPath: failedOutput,
		Recipients: []age.Recipient{identity.Recipient()}, Signer: signer, PGDumpPath: "/usr/bin/false",
	}); err == nil {
		t.Fatal("failing pg_dump command produced a bundle")
	}
	if _, err := os.Lstat(failedOutput); !os.IsNotExist(err) {
		t.Fatal("failing pg_dump command published an output")
	}
	// A real pg_dump that streams part of the archive and then fails must not
	// leave a published bundle or a staging file behind.
	midstreamDirectory := t.TempDir()
	midstreamOutput := filepath.Join(midstreamDirectory, "pg-dump-midstream.age")
	if _, err := CreateBundle(ctx, CreateBundleOptions{
		Pool: source, DatabaseURL: os.Getenv("NORN_TEST_DATABASE_URL"), Schema: schema, OutputPath: midstreamOutput,
		Recipients: []age.Recipient{identity.Recipient()}, Signer: signer, PGDumpPath: midstreamPGDump(t, 4096),
	}); err == nil {
		t.Fatal("midstream pg_dump failure produced a bundle")
	}
	if entries, err := os.ReadDir(midstreamDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("midstream pg_dump failure left files: %v %v", entries, err)
	}
	bundlePath := filepath.Join(t.TempDir(), "control-recovery.age")
	manifest, err := CreateBundle(ctx, CreateBundleOptions{
		Pool: source, DatabaseURL: os.Getenv("NORN_TEST_DATABASE_URL"), Schema: schema, OutputPath: bundlePath,
		Recipients: []age.Recipient{identity.Recipient()}, Signer: signer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RequiredSigningKeyIDs[0] != hmacKeyID(hmacKey) {
		t.Fatalf("required keys = %v", manifest.RequiredSigningKeyIDs)
	}
	verified, err := VerifyBundle(bundlePath, []age.Identity{identity}, []ed25519.PublicKey{public}, []string{hmacKeyID(hmacKey)})
	if err != nil {
		t.Fatal(err)
	}
	dump, err := verified.OpenDump()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bytes.NewBuffer(nil).ReadFrom(dump); err != nil {
		t.Fatal(err)
	}
	_ = dump.Close()
	_ = verified.Close()
	ciphertext, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(recoveryCanary)) {
		t.Fatal("encrypted bundle exposed plaintext canary")
	}
	if _, err := CreateBundle(ctx, CreateBundleOptions{
		Pool: source, DatabaseURL: os.Getenv("NORN_TEST_DATABASE_URL"), Schema: schema, OutputPath: bundlePath,
		Recipients: []age.Recipient{identity.Recipient()}, Signer: signer,
	}); err == nil {
		t.Fatal("bundle creation replaced an existing destination")
	}
	unchanged, err := os.ReadFile(bundlePath)
	if err != nil || !bytes.Equal(ciphertext, unchanged) {
		t.Fatal("duplicate bundle creation changed the existing destination")
	}

	target := recoveryTargetPool(t, targetURL, schema)
	material := &RecoveryKeyMaterial{HMACKeys: [][]byte{hmacKey}}
	corruptPath := filepath.Join(t.TempDir(), "truncated.age")
	if err := os.WriteFile(corruptPath, ciphertext[:len(ciphertext)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	baseRestore := RestoreOptions{
		BundlePath: corruptPath, Identities: []age.Identity{identity}, TrustedKeys: []ed25519.PublicKey{public}, AvailableKeyIDs: material.KeyIDs(),
		TargetPool: target, TargetDatabaseURL: targetURL, IsolatedTarget: true, EvidenceVerifier: material,
	}
	if _, err := RestorePassive(ctx, baseRestore); err == nil {
		t.Fatal("truncated bundle reached restore")
	}
	assertSchemaAbsent(t, ctx, target, schema)
	baseRestore.BundlePath = bundlePath
	baseRestore.PGRestorePath = "/usr/bin/false"
	if _, err := RestorePassive(ctx, baseRestore); err == nil {
		t.Fatal("failing pg_restore command reported success")
	}
	assertSchemaAbsent(t, ctx, target, schema)
	// A real pg_restore that has already created objects and started loading
	// data inside its single transaction, then hits a truncated archive, must
	// roll every write back.
	restoreLog := filepath.Join(t.TempDir(), "pg-restore.log")
	baseRestore.PGRestorePath = midstreamPGRestore(t, manifest.Sections[0].Size-64, restoreLog)
	if _, err := RestorePassive(ctx, baseRestore); err == nil {
		t.Fatal("midstream pg_restore failure reported success")
	}
	restoreOutput, err := os.ReadFile(restoreLog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(restoreOutput, []byte("creating TABLE")) || !bytes.Contains(restoreOutput, []byte("processing data for table")) || !bytes.Contains(restoreOutput, []byte("error")) {
		t.Fatalf("pg_restore did not fail after starting writes:\n%s", restoreOutput)
	}
	assertSchemaAbsent(t, ctx, target, schema)
	report, err := RestorePassive(ctx, RestoreOptions{
		BundlePath: bundlePath, Identities: []age.Identity{identity}, TrustedKeys: []ed25519.PublicKey{public}, AvailableKeyIDs: material.KeyIDs(),
		TargetPool: target, TargetDatabaseURL: targetURL, IsolatedTarget: true, EvidenceVerifier: material,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passive || report.ActivationReady || report.BundleID != manifest.BundleID {
		t.Fatalf("restore report = %#v", report)
	}
	var operationCount, acceptanceCount, auditCount int
	if err := target.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM `+pgx.Identifier{schema, "operations"}.Sanitize()+`),
		(SELECT count(*) FROM `+pgx.Identifier{schema, "operation_acceptance_intents"}.Sanitize()+`),
		(SELECT count(*) FROM `+pgx.Identifier{schema, "mutation_audit_events"}.Sanitize()+`)`).Scan(&operationCount, &acceptanceCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 || acceptanceCount != 1 || auditCount != 1 {
		t.Fatalf("restored counts operation=%d acceptance=%d audit=%d", operationCount, acceptanceCount, auditCount)
	}
	if _, err := target.Exec(ctx, `UPDATE `+pgx.Identifier{schema, "operations"}.Sanitize()+` SET payload=jsonb_set(payload,'{sequence}','9007199254740992'::jsonb) WHERE id='operation-roundtrip'`); err != nil {
		t.Fatal(err)
	}
	tx, err := target.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if err := material.VerifyRestoredEvidence(ctx, tx, schema, material.KeyIDs()); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("altered integer above 2^53 remained bound to signed acceptance")
	}
	_ = tx.Rollback(ctx)
}

// midstreamPGDump wraps the real pg_dump so that it emits limit bytes of a
// genuine archive and then fails.
func midstreamPGDump(t *testing.T, limit int64) string {
	t.Helper()
	real, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Fatal(err)
	}
	return writeWrapper(t, "pg_dump", fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exec %q --version; fi\n%q \"$@\" | head -c %d\nexit 1\n", real, real, limit))
}

// midstreamPGRestore wraps the real pg_restore so that it receives only the
// first limit bytes of the verified dump and logs its progress verbosely.
func midstreamPGRestore(t *testing.T, limit int64, logPath string) string {
	t.Helper()
	real, err := exec.LookPath("pg_restore")
	if err != nil {
		t.Fatal(err)
	}
	return writeWrapper(t, "pg_restore", fmt.Sprintf("#!/bin/sh\nhead -c %d | %q \"$@\" --verbose 2>%q\n", limit, real, logPath))
}

func writeWrapper(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertSchemaAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed recovery attempt wrote target schema")
	}
}

func seedAuthenticRecoveryEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key []byte) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := auditRecord{
		ID: "audit-roundtrip", RequestID: "request-roundtrip", PrincipalSubject: "actor-roundtrip", Scopes: []string{"write:operations"},
		Method: "POST", Path: "/api/v1/recovery-roundtrip", Status: 202, Outcome: "accepted", StartedAt: now, DurationMs: 7, KeyID: hmacKeyID(key),
	}
	finished := now.Add(7 * time.Millisecond)
	record.FinishedAt = &finished
	record.Digest = signAudit(key, record)
	scopes := `["write:operations"]`
	if _, err := pool.Exec(ctx, `INSERT INTO mutation_audit_events
		(id,request_id,principal_subject,scopes,method,path,status,outcome,started_at,finished_at,duration_ms,record_digest,key_id)
		VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		record.ID, record.RequestID, record.PrincipalSubject, scopes, record.Method, record.Path, record.Status, record.Outcome, record.StartedAt, record.FinishedAt, record.DurationMs, record.Digest, record.KeyID); err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(string(key))
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operationStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	acceptance := store.OperationAcceptance{
		Identity:   store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "actor-roundtrip"}, Kind: "app.deploy", Resource: "app/recovery-roundtrip", Key: "request-" + recoveryCanary},
		Operation:  model.Operation{ID: "operation-roundtrip", Kind: "app.deploy", App: "recovery-roundtrip", SagaID: "saga-roundtrip", Status: model.OperationQueued, Source: "recovery-integration", Payload: map[string]interface{}{"deploymentId": "deployment-roundtrip", "sequence": json.Number("9007199254740993")}, Metadata: map[string]interface{}{}, MaxAttempts: 1, StartedAt: now, NextAttemptAt: now},
		Deployment: &model.Deployment{ID: "deployment-roundtrip", App: "recovery-roundtrip", CommitSHA: "0123456789abcdef0123456789abcdef01234567", ImageTag: "registry.invalid/recovery@sha256:" + strings.Repeat("a", 64), Environment: "test", SagaID: "saga-roundtrip", Status: model.StatusQueued, SourceKind: "git", SourceRef: "refs/heads/recovery", StartedAt: now},
		Regions:    []model.ResolvedRegion{{Name: "test-region", NomadRegion: "test-nomad", Datacenters: []string{"test-dc"}, TrafficWeight: 100}},
		Audit:      store.AcceptanceAuditContext{RequestReceiptID: record.ID, RequestID: record.RequestID, Source: "recovery-integration", Scopes: record.Scopes},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
}

func recoveryTargetPool(t *testing.T, databaseURL, schema string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	identifier := pgx.Identifier{schema}.Sanitize()
	var schemaExists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&schemaExists); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if schemaExists {
		admin.Close()
		t.Fatal("recovery target schema already exists")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+identifier+` CASCADE`); err != nil && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("drop restored schema: %v", err)
		}
		admin.Close()
	})
	return pool
}
