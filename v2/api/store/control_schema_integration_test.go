package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestControlSchemaAdoptsLegacyRowsWithoutReplacingEvidence(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	migrations := ControlSchemaMigrations()
	if len(migrations) != 43 || migrations[16].Version != 17 {
		t.Fatalf("control migration catalog has %d migrations, want immutable baseline through 17 and appended migrations through 43", len(migrations))
	}
	for index, migration := range migrations {
		if migration.Version != int64(index+1) {
			t.Fatalf("control migration position %d has version %d", index, migration.Version)
		}
	}
	// Simulate the unversioned v2 database before the migration ledger existed.
	if _, err := pool.Exec(ctx, migrations[0].SQL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO access_devices(id,name,public_key,created_at) VALUES ('device-stable','operator','public-key-bytes',$1::timestamptz)`, now)
	batch.Queue(`INSERT INTO access_tokens(jti,device_id,subject,scopes,issued_at,expires_at) VALUES ('token-stable','device-stable','operator','["admin"]',$1::timestamptz,$1::timestamptz + interval '1 hour')`, now)
	batch.Queue(`INSERT INTO step_up_challenges(id,device_id,token_jti,purpose,resource,nonce_hash,status,created_at,expires_at) VALUES ('challenge-stable','device-stable','token-stable','exec','app/demo','nonce-digest','consumed',$1::timestamptz,$1::timestamptz + interval '1 hour')`, now)
	batch.Queue(`INSERT INTO exec_sessions(id,device_id,token_jti,challenge_id,app_id,allocation_id,task,status,created_at,expires_at,owner_id,owner_token,owner_lease_until) VALUES ('exec-stable','device-stable','token-stable','challenge-stable','demo','alloc-stable','web','running',$1::timestamptz,$1::timestamptz + interval '1 hour','api-owner','owner-secret',$1::timestamptz + interval '1 minute')`, now)
	batch.Queue(`INSERT INTO operations(id,kind,app,saga_id,status,payload,metadata,attempts,max_attempts,locked_by,lock_generation,locked_until,next_attempt_at,started_at,updated_at) VALUES ('operation-stable','app.deploy','demo','saga-stable','running','{"deploymentId":"deployment-stable"}','{"receipt":"signed-receipt-bytes"}',1,3,'api-owner',7,$1::timestamptz + interval '1 minute',$1::timestamptz,$1::timestamptz,$1::timestamptz)`, now)
	batch.Queue(`INSERT INTO mutation_audit_events(id,request_id,principal_subject,method,path,status,outcome,started_at,finished_at,record_digest,key_id) VALUES ('audit-stable','request-stable','operator','POST','/api/apps/demo/deploy',202,'success',$1::timestamptz,$1::timestamptz,'signed-audit-digest-bytes','audit-key-v1')`, now)
	batch.Queue(`INSERT INTO mutation_audit_incidents(id,audit_event_id,reason_code,explanation,acknowledged_by,acknowledged_at,key_id,record_digest) VALUES ('incident-stable','audit-stable','reviewed','fixture','operator',$1::timestamptz,'audit-key-v1','signed-incident-digest-bytes')`, now)
	batch.Queue(`INSERT INTO fleet_runner_attempts(id,plan_id,attempt,runner_attempt_id,commit_sha,plan_sha256,workflow_url,status,current_phase,root_attempt_id,heartbeat_timeout_seconds,heartbeat_at,heartbeat_expires_at,started_at,updated_at) VALUES ('attempt-root','plan-stable',1,'runner-1','commit-stable','signed-plan-sha-bytes','https://example.test/run/1','succeeded','complete','attempt-root',60,$1::timestamptz,$1::timestamptz + interval '1 minute',$1::timestamptz,$1::timestamptz)`, now)
	batch.Queue(`INSERT INTO fleet_runner_attempts(id,plan_id,attempt,runner_attempt_id,commit_sha,plan_sha256,workflow_url,status,current_phase,root_attempt_id,heartbeat_timeout_seconds,heartbeat_at,heartbeat_expires_at,started_at,updated_at) VALUES ('attempt-retry','plan-stable',2,'runner-2','commit-stable','signed-plan-sha-bytes','https://example.test/run/2','failed','apply','',60,$1::timestamptz,$1::timestamptz + interval '1 minute',$1::timestamptz,$1::timestamptz)`, now)
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

	db := &DB{Pool: pool}
	migrator, err := NewControlSchemaMigrator(db)
	if err != nil {
		t.Fatal(err)
	}
	status, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentMigrationVersion != 43 || len(status.AppliedVersions) != 43 || status.AppliedVersions[42] != 43 || status.MinimumReaderVersion != MySQLRetainedArtifactReaderVersion || status.MinimumWriterVersion != SnapshotExportIntentWriterVersion {
		t.Fatalf("migration status = %#v", status)
	}

	assertText := func(query, want string, args ...any) {
		t.Helper()
		var got string
		if err := pool.QueryRow(ctx, query, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("query %q = %q, want %q", query, got, want)
		}
	}
	assertText(`SELECT public_key FROM access_devices WHERE id='device-stable'`, "public-key-bytes")
	assertText(`SELECT jti FROM access_tokens WHERE device_id='device-stable'`, "token-stable")
	assertText(`SELECT owner_token FROM exec_sessions WHERE id='exec-stable' AND status='running'`, "owner-secret")
	assertText(`SELECT metadata->>'receipt' FROM operations WHERE id='operation-stable' AND status='running' AND lock_generation=7`, "signed-receipt-bytes")
	assertText(`SELECT record_digest FROM mutation_audit_events WHERE id='audit-stable'`, "signed-audit-digest-bytes")
	assertText(`SELECT record_digest FROM mutation_audit_incidents WHERE id='incident-stable'`, "signed-incident-digest-bytes")
	assertText(`SELECT plan_sha256 FROM fleet_runner_attempts WHERE id='attempt-root'`, "signed-plan-sha-bytes")
	assertText(`SELECT root_attempt_id FROM fleet_runner_attempts WHERE id='attempt-root'`, "attempt-root")
	// The sole intentional legacy data repair fills only blank lineage from the
	// first attempt; already populated roots and evidence bytes remain intact.
	assertText(`SELECT root_attempt_id FROM fleet_runner_attempts WHERE id='attempt-retry'`, "attempt-root")
	var authority string
	if err := pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	if authority == "" {
		t.Fatal("migration 2 did not establish a durable control authority")
	}
	var identities, intents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_request_identities`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_acceptance_intents`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || intents != 0 {
		t.Fatalf("legacy adoption fabricated acceptance evidence: identities=%d intents=%d", identities, intents)
	}
	var acceptanceRequired bool
	if err := pool.QueryRow(ctx, `SELECT acceptance_required FROM operations WHERE id='operation-stable'`).Scan(&acceptanceRequired); err != nil {
		t.Fatal(err)
	}
	if acceptanceRequired {
		t.Fatal("legacy operation was incorrectly marked as atomically accepted")
	}
	var prunedThrough int64
	if err := pool.QueryRow(ctx, `SELECT pruned_through_cursor FROM control_event_retention WHERE id = true`).Scan(&prunedThrough); err != nil || prunedThrough != 0 {
		t.Fatalf("event replay retention watermark = %d, %v", prunedThrough, err)
	}
	var effects int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("legacy adoption fabricated external-effect evidence: effects=%d", effects)
	}

	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	repeated, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if len(repeated.AppliedVersions) != 0 || !after.Equal(before) {
		t.Fatalf("repeated migration mutated metadata: status=%#v before=%s after=%s", repeated, before, after)
	}
}

func TestSignedAcceptanceByteReserveMigrationBackfillsExistingPayloads(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	migrations := ControlSchemaMigrations()
	oldMigrator, err := NewSchemaMigrator(pool, migrations[:13], BinarySchemaCompatibility{
		ReaderVersion: EvidenceArchiveReaderVersion, WriterVersion: RestartEffectSourceWriterVersion,
	}, SchemaMigratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldMigrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var authority string
	if err := pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,acceptance_required) VALUES ('backfill-operation','app.preflight','queued',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_request_identities
			(id,authority,actor_issuer,actor_subject,kind,resource,request_key,fingerprint_version,fingerprint_digest,operation_id,created_at)
		VALUES ('backfill-identity',$1::uuid,'issuer','actor','app.preflight','app','key',$2,$3,'backfill-operation',now())`,
		authority, OperationRequestFingerprintVersion, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_acceptance_intents
			(id,schema_version,request_identity_id,operation_id,accepted_at,source,scopes,fingerprint_version,fingerprint_digest,
			 request_canonical_bytes,canonical_bytes,canonical_digest,signing_algorithm,signing_key_id,signature)
		VALUES ('backfill-intent',$1,'backfill-identity','backfill-operation',now(),'migration-test','[]',$2,$3,$4,$5,$3,'hmac-sha256','key','signature-bytes')`,
		OperationAcceptanceEnvelopeSchema, OperationRequestFingerprintVersion, fingerprint, []byte("request-bytes"), []byte("envelope-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	db := &DB{Pool: pool}
	newMigrator, err := NewControlSchemaMigrator(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newMigrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var reserved, expected, configured int64
	if err := pool.QueryRow(ctx, `SELECT r.reserved_bytes,
		octet_length(i.request_canonical_bytes) + octet_length(i.canonical_bytes) + octet_length(convert_to(i.signature, 'UTF8')),
		e.max_signed_acceptance_bytes
		FROM signed_acceptance_byte_reservations r
		JOIN operation_acceptance_intents i ON i.id=r.acceptance_intent_id
		CROSS JOIN evidence_reserve e
		WHERE r.operation_id='backfill-operation'`).Scan(&reserved, &expected, &configured); err != nil {
		t.Fatal(err)
	}
	if reserved != expected || configured != 0 {
		t.Fatalf("backfilled reservation=%d expected=%d configured limit=%d", reserved, expected, configured)
	}
}
