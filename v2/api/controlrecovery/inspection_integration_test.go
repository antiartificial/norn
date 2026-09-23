package controlrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/store"
)

const inspectionCanary = "NORN_INSPECTION_CANARY_SECRET_72f9"

func TestInspectionUsesConsistentSnapshotPreservesIntegersAndRedactsSecrets(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	seedAcceptedOperation(t, ctx, pool, "before", inspectionCanary)
	seedSensitiveInspectionRows(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO fleet_runner_attempts
			(id,plan_id,attempt,runner_attempt_id,commit_sha,plan_sha256,workflow_url,status,current_phase,root_attempt_id,
			 heartbeat_sequence,heartbeat_timeout_seconds,revision,heartbeat_at,heartbeat_expires_at,message)
		VALUES
			('attempt-large','plan-large',1,'runner-large','commit-large','digest-large',$1,'running','apply','attempt-large',
			 9007199254740993,60,9007199254740995,now(),now()+interval '1 minute',$1)`, inspectionCanary); err != nil {
		t.Fatal(err)
	}

	snapshotReady := make(chan struct{})
	resume := make(chan struct{})
	releaseSnapshot := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(releaseSnapshot)
	result := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		var output bytes.Buffer
		err := exportInspection(ctx, pool, schema, &output, exportOptions{
			registry: InspectionRegistry(),
			afterSnapshot: func(context.Context) error {
				close(snapshotReady)
				<-resume
				return nil
			},
		})
		result <- struct {
			data []byte
			err  error
		}{data: output.Bytes(), err: err}
	}()
	select {
	case <-snapshotReady:
	case early := <-result:
		t.Fatalf("inspection failed before snapshot barrier: %v", early.err)
	case <-time.After(10 * time.Second):
		releaseSnapshot()
		t.Fatal("timed out waiting for inspection snapshot barrier")
	}
	seedAcceptedOperation(t, ctx, pool, "concurrent", inspectionCanary)
	releaseSnapshot()
	exported := <-result
	if exported.err != nil {
		t.Fatal(exported.err)
	}
	if bytes.Contains(exported.data, []byte(inspectionCanary)) {
		t.Fatal("inspection output contains canary secret")
	}
	if !bytes.Contains(exported.data, []byte("9007199254740993")) || !bytes.Contains(exported.data, []byte("9007199254740995")) {
		t.Fatal("inspection output did not preserve BIGINT lexemes above 2^53")
	}

	document := decodeInspection(t, exported.data)
	if document.Restorable {
		t.Fatal("inspection document incorrectly claims to be restorable")
	}
	if document.Format != inspectionFormat || len(document.Excludes) == 0 {
		t.Fatalf("inspection metadata = %#v", document)
	}
	assertAcceptedSet(t, document, "before", true)
	assertAcceptedSet(t, document, "concurrent", false)

	var first, second bytes.Buffer
	if err := ExportInspection(ctx, pool, schema, &first); err != nil {
		t.Fatal(err)
	}
	if err := ExportInspection(ctx, pool, schema, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("unchanged control state produced non-deterministic inspection output")
	}
	secondDocument := decodeInspection(t, second.Bytes())
	assertAcceptedSet(t, secondDocument, "before", true)
	assertAcceptedSet(t, secondDocument, "concurrent", true)
}

func TestInspectionFailsClosedOnUnknownTableWithoutPartialOrSecretOutput(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE `+pgx.Identifier{schema, "future_control_state"}.Sanitize()+` ()`); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := ExportInspection(ctx, pool, schema, &output)
	var classification *SchemaClassificationError
	if !errors.As(err, &classification) || classification.UnknownTables != 1 {
		t.Fatalf("error = %T %v, want one unknown table", err, err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed inspection wrote %d bytes", output.Len())
	}
	if strings.Contains(err.Error(), inspectionCanary) || strings.Contains(err.Error(), "future_control_state") {
		t.Fatalf("classification error leaked identifier or data: %v", err)
	}
}

func TestInspectionFailsClosedOnUnknownColumn(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE `+pgx.Identifier{schema, "operations"}.Sanitize()+` ADD COLUMN future_secret TEXT NOT NULL DEFAULT '`+inspectionCanary+`'`); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := ExportInspection(ctx, pool, schema, &output)
	var classification *SchemaClassificationError
	if !errors.As(err, &classification) || classification.UnknownColumns != 1 {
		t.Fatalf("error = %T %v, want one unknown column", err, err)
	}
	if output.Len() != 0 || strings.Contains(err.Error(), inspectionCanary) || strings.Contains(err.Error(), "future_secret") {
		t.Fatalf("failed inspection leaked or emitted data: bytes=%d error=%v", output.Len(), err)
	}
}

func TestInspectionFailsClosedOnUnsupportedMigrationCatalog(t *testing.T) {
	t.Run("future ledger version", func(t *testing.T) {
		pool, schema := inspectionTestDatabase(t)
		ctx := context.Background()
		futureVersion := inspectionCatalog[len(inspectionCatalog)-1].version + 1
		if _, err := pool.Exec(ctx, `
			INSERT INTO norn_schema_migrations(version,name,checksum,minimum_reader_version,minimum_writer_version)
			VALUES ($1,'future-control-state',$2,1,$1)`, futureVersion, strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE norn_schema_compatibility SET current_migration_version=$1,minimum_writer_version=$1`, futureVersion); err != nil {
			t.Fatal(err)
		}
		assertCatalogRefusal(t, pool, schema)
	})

	t.Run("known checksum changed", func(t *testing.T) {
		pool, schema := inspectionTestDatabase(t)
		if _, err := pool.Exec(context.Background(), `UPDATE norn_schema_migrations SET checksum=$1 WHERE version=2`, strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
		assertCatalogRefusal(t, pool, schema)
	})

	t.Run("extra compatibility row", func(t *testing.T) {
		pool, schema := inspectionTestDatabase(t)
		if _, err := pool.Exec(context.Background(), `ALTER TABLE norn_schema_compatibility DROP CONSTRAINT norn_schema_compatibility_singleton_check`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO norn_schema_compatibility
				(singleton,format_version,current_migration_version,minimum_reader_version,minimum_writer_version)
			VALUES (false,1,2,1,2)`); err != nil {
			t.Fatal(err)
		}
		assertCatalogRefusal(t, pool, schema)
	})
}

func assertCatalogRefusal(t *testing.T, pool *pgxpool.Pool, schema string) {
	t.Helper()
	var output bytes.Buffer
	err := ExportInspection(context.Background(), pool, schema, &output)
	var catalog *CatalogValidationError
	if !errors.As(err, &catalog) {
		t.Fatalf("error = %T %v, want CatalogValidationError", err, err)
	}
	if output.Len() != 0 {
		t.Fatalf("unsupported catalog emitted %d bytes", output.Len())
	}
}

func inspectionTestDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "norn_control_inspection_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	migrator, err := store.NewControlSchemaMigrator(&store.DB{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	return pool, schema
}

func seedAcceptedOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix, canary string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	operationID := "operation-" + suffix
	deploymentID := "deployment-" + suffix
	identityID := "identity-" + suffix
	intentID := "intent-" + suffix
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployments(id,app,commit_sha,image_tag,saga_id,status)
		VALUES ($1,'app-inspected','commit-inspected','image-inspected',$2,'queued')`, deploymentID, "saga-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO operations(id,kind,app,saga_id,status,message,payload,metadata,last_error,acceptance_required)
		VALUES ($1,'app.deploy','app-inspected',$2,'queued',$3::text,jsonb_build_object('secret',$3::text),jsonb_build_object('secret',$3::text),$3::text,true)`, operationID, "saga-"+suffix, canary); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO operation_request_identities
			(id,authority,actor_issuer,actor_subject,kind,resource,request_key,fingerprint_version,fingerprint_digest,operation_id,created_at)
		SELECT $1,authority,'issuer','actor','app.deploy','app/app-inspected',$2,'v1',$3,$4,now()
		FROM control_plane_identity WHERE singleton`, identityID, canary+"-"+suffix, strings.Repeat("a", 64), operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO operation_acceptance_intents
			(id,schema_version,request_identity_id,operation_id,deployment_id,accepted_at,source,fingerprint_version,
			 fingerprint_digest,request_canonical_bytes,canonical_bytes,canonical_digest,signing_algorithm,signing_key_id,signature)
		VALUES ($1,'norn.acceptance/v1',$2,$3,$4,now(),'integration','v1',$5,$6,$6,$7,'hmac-sha256','key-reference',$8)`,
		intentID, identityID, operationID, deploymentID, strings.Repeat("a", 64), []byte(canary), strings.Repeat("b", 64), canary); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedSensitiveInspectionRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO notification_channels(id,provider,name,url,token,user_key) VALUES ('notification-canary','webhook','redacted',$1,$1,$1)`, inspectionCanary)
	batch.Queue(`INSERT INTO access_devices(id,name) VALUES ('device-canary','device')`)
	batch.Queue(`INSERT INTO access_enrollments(id,code_hash,verifier_hash,device_name,source_hash,status,expires_at) VALUES ('enrollment-canary',$1,$1,'device',$1,'pending',now()+interval '1 hour')`, inspectionCanary)
	batch.Queue(`INSERT INTO step_up_challenges(id,device_id,token_jti,purpose,resource,nonce_hash,status,expires_at) VALUES ('challenge-canary','device-canary','token-safe','exec',$1,$1,'pending',now()+interval '1 hour')`, inspectionCanary)
	batch.Queue(`INSERT INTO exec_sessions(id,device_id,token_jti,challenge_id,app_id,allocation_id,task,command,status,expires_at,remote_addr,user_agent,owner_token) VALUES ('exec-canary','device-canary','token-safe','challenge-canary','app','alloc','task',jsonb_build_array($1::text),'pending',now()+interval '1 hour',$1::text,$1::text,$1::text)`, inspectionCanary)
	batch.Queue(`INSERT INTO webhook_deliveries(id,provider,reason,remote_addr,user_agent,payload,metadata) VALUES ('webhook-canary','github',$1::text,$1::text,$1::text,jsonb_build_object('secret',$1::text),jsonb_build_object('secret',$1::text))`, inspectionCanary)
	batch.Queue(`INSERT INTO recovery_drills(id,kind,evidence) VALUES ('drill-canary','database.restore',jsonb_build_object('secret',$1::text))`, inspectionCanary)
	results := pool.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			t.Fatal(err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
}

func decodeInspection(t *testing.T, data []byte) Inspection {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document Inspection
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertAcceptedSet(t *testing.T, document Inspection, suffix string, want bool) {
	t.Helper()
	wants := map[string]string{
		"deployments":                  "deployment-" + suffix,
		"operations":                   "operation-" + suffix,
		"operation_request_identities": "identity-" + suffix,
		"operation_acceptance_intents": "intent-" + suffix,
	}
	for tableName, id := range wants {
		found := false
		for _, table := range document.Tables {
			if table.Name != tableName {
				continue
			}
			for _, raw := range table.Rows {
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				var row map[string]any
				if err := decoder.Decode(&row); err != nil {
					t.Fatal(err)
				}
				if row["id"] == id {
					found = true
				}
			}
		}
		if found != want {
			t.Errorf("%s id %q presence = %t, want %t", tableName, id, found, want)
		}
	}
}
