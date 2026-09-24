package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestControlSchemaAdoptsPinnedMiniPilotSchema(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	createMiniPilotFixture(t, pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind) VALUES ('plan-1','fleet.plan')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fleet_runner_attempts(id,plan_id,attempt,status,current_phase,commit_sha,plan_sha256,heartbeat_at,last_error) VALUES ('attempt-1','plan-1',1,'failed','apply','commit','plan-digest',$1,'preserved failure')`, now); err != nil {
		t.Fatal(err)
	}
	migrations := ControlSchemaMigrations()
	frozen := MigrationChecksum(migrations[0])
	migrator := miniFixtureMigrator(t, pool)
	status, err := migrator.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentMigrationVersion != migrations[len(migrations)-1].Version || len(status.AppliedVersions) != len(migrations) {
		t.Fatalf("status = %#v", status)
	}
	var expiry time.Time
	var message, checksum string
	if err := pool.QueryRow(ctx, `SELECT heartbeat_expires_at,message FROM fleet_runner_attempts WHERE id='attempt-1'`).Scan(&expiry, &message); err != nil {
		t.Fatal(err)
	}
	if !expiry.Equal(now.Add(120*time.Second)) || message != "preserved failure" {
		t.Fatalf("repair expiry=%s message=%q", expiry, message)
	}
	if err := pool.QueryRow(ctx, `SELECT checksum FROM norn_schema_migrations WHERE version=1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != frozen || checksum != "6124f0d3fd1e339daea3563f55648d8c0bd4dabbe8ce5f972fccd912ba80d390" {
		t.Fatalf("baseline checksum = %q", checksum)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, err := (&DB{Pool: pool}).GetFleetRunnerAttempt(ctx, "plan-1", "attempt-1"); err != nil {
		t.Fatalf("runtime read: %v", err)
	}
}

func TestControlSchemaMiniAdoptionFailsClosed(t *testing.T) {
	t.Run("partial signature", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		if _, err := pool.Exec(context.Background(), `CREATE TABLE external_deployment_nonces(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		migrator := miniFixtureMigrator(t, pool)
		_, err := migrator.Migrate(context.Background())
		var adoptionErr *MiniSchemaAdoptionError
		if !errors.As(err, &adoptionErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		assertNoMigrationMetadata(t, pool)
	})
	t.Run("unknown signature", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		createMiniPilotFixture(t, pool)
		migrator := miniFixtureMigrator(t, pool)
		if _, err := pool.Exec(context.Background(), `ALTER TABLE fleet_runner_attempts ADD COLUMN unknown_generation BIGINT`); err != nil {
			t.Fatal(err)
		}
		_, err := migrator.Migrate(context.Background())
		var adoptionErr *MiniSchemaAdoptionError
		if !errors.As(err, &adoptionErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		assertNoMigrationMetadata(t, pool)
	})
	t.Run("unrelated baseline table drift", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		createMiniPilotFixture(t, pool)
		if _, err := pool.Exec(context.Background(), `CREATE TABLE access_tokens(id TEXT PRIMARY KEY, subject TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := miniSchemaStructuralFingerprint(context.Background(), tx)
		if rollbackErr := tx.Rollback(context.Background()); err == nil && rollbackErr != nil {
			err = rollbackErr
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `ALTER TABLE access_tokens DROP COLUMN subject`); err != nil {
			t.Fatal(err)
		}
		migrator, err := NewControlSchemaMigrator(&DB{Pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		migrator.adoptUnversioned = func(ctx context.Context, tx pgx.Tx) error {
			return adoptMiniControlSchemaFingerprint(ctx, tx, fingerprint, []string{"access_tokens", "external_deployment_admission_checkpoints", "external_deployment_admissions", "external_deployment_nonces", "fleet_github_dispatches", "fleet_runner_attempts", "fleet_runner_checkpoint_refs", "operations"})
		}
		_, err = migrator.Migrate(context.Background())
		var adoptionErr *MiniSchemaAdoptionError
		if !errors.As(err, &adoptionErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		assertNoMigrationMetadata(t, pool)
	})
	t.Run("dispatch rows", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		createMiniPilotFixture(t, pool)
		if _, err := pool.Exec(context.Background(), `INSERT INTO operations(id,kind) VALUES ('plan-1','fleet.plan')`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `INSERT INTO fleet_github_dispatches(plan_id,plan_run_id,plan_sha256,approved_head_sha,fleet_environment,allow_destructive,dispatch_nonce_sha256) VALUES ('plan-1',1,'plan','head','pilot',false,'digest')`); err != nil {
			t.Fatal(err)
		}
		migrator := miniFixtureMigrator(t, pool)
		_, err := migrator.Migrate(context.Background())
		var adoptionErr *MiniSchemaAdoptionError
		if !errors.As(err, &adoptionErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		assertNoMigrationMetadata(t, pool)
	})
}

func TestMiniDispatchLockBlocksLegacyInsertUntilAdoptionTransactionEnds(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	createMiniPilotFixture(t, pools[0])
	if _, err := pools[0].Exec(context.Background(), `INSERT INTO operations(id,kind) VALUES ('plan-1','fleet.plan')`); err != nil {
		t.Fatal(err)
	}
	tx, err := pools[0].Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lockMiniDispatchTable(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	blocked, err := pools[1].Begin(context.Background())
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if _, err := blocked.Exec(context.Background(), `SET LOCAL lock_timeout = '150ms'`); err != nil {
		_ = blocked.Rollback(context.Background())
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	_, err = blocked.Exec(context.Background(), `INSERT INTO fleet_github_dispatches(plan_id,plan_run_id,plan_sha256,approved_head_sha,fleet_environment,allow_destructive,dispatch_nonce_sha256) VALUES ('plan-1',1,'plan','head','pilot',false,'digest')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		_ = blocked.Rollback(context.Background())
		_ = tx.Rollback(context.Background())
		t.Fatalf("concurrent insert error = %T %v, want server lock timeout", err, err)
	}
	if err := blocked.Rollback(context.Background()); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := pools[1].Exec(context.Background(), `INSERT INTO fleet_github_dispatches(plan_id,plan_run_id,plan_sha256,approved_head_sha,fleet_environment,allow_destructive,dispatch_nonce_sha256) VALUES ('plan-1',1,'plan','head','pilot',false,'digest')`); err != nil {
		t.Fatalf("insert after adoption transaction: %v", err)
	}
}

func TestMiniAdoptionRejectsMissingPinnedSequence(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	createMiniPilotFixture(t, pool)
	migrator := miniFixtureMigrator(t, pool)
	if _, err := pool.Exec(context.Background(), `DROP SEQUENCE mini_fixture_sequence`); err != nil {
		t.Fatal(err)
	}
	_, err := migrator.Migrate(context.Background())
	var adoptionErr *MiniSchemaAdoptionError
	if !errors.As(err, &adoptionErr) {
		t.Fatalf("error = %T %v, want structural fingerprint refusal", err, err)
	}
	assertNoMigrationMetadata(t, pool)
}

func assertNoMigrationMetadata(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var present bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('norn_schema_migrations') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("failed adoption left migration metadata")
	}
}

const miniPilotFixtureSQL = `
CREATE SEQUENCE mini_fixture_sequence AS bigint START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
CREATE TABLE operations (id text PRIMARY KEY, kind text NOT NULL, app text NOT NULL DEFAULT '', saga_id text NOT NULL DEFAULT '', ref text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'running', risk text NOT NULL DEFAULT '', source text NOT NULL DEFAULT '', message text NOT NULL DEFAULT '', metadata jsonb NOT NULL DEFAULT '{}', started_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, payload jsonb NOT NULL DEFAULT '{}', attempts integer NOT NULL DEFAULT 0, max_attempts integer NOT NULL DEFAULT 1, locked_by text NOT NULL DEFAULT '', locked_until timestamptz, next_attempt_at timestamptz NOT NULL DEFAULT now(), last_error text NOT NULL DEFAULT '');
CREATE TABLE fleet_runner_attempts (id text PRIMARY KEY, plan_id text NOT NULL REFERENCES operations(id) ON DELETE CASCADE, attempt integer NOT NULL, runner_attempt_id text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'queued', current_phase text NOT NULL DEFAULT '', commit_sha text NOT NULL DEFAULT '', plan_sha256 text NOT NULL DEFAULT '', workflow_url text NOT NULL DEFAULT '', principal_subject text NOT NULL DEFAULT '', retry_of text NOT NULL DEFAULT '', heartbeat_sequence bigint NOT NULL DEFAULT 0, heartbeat_timeout_seconds integer NOT NULL DEFAULT 120, revision bigint NOT NULL DEFAULT 1, started_at timestamptz NOT NULL DEFAULT now(), heartbeat_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, last_error text NOT NULL DEFAULT '', metadata jsonb NOT NULL DEFAULT '{}', root_attempt_id text NOT NULL DEFAULT '', source_dispatch_run_id bigint NOT NULL DEFAULT 0, pilot_run_id text NOT NULL DEFAULT '', recovery boolean NOT NULL DEFAULT false, phase_started_at timestamptz NOT NULL DEFAULT now(), UNIQUE(plan_id,attempt));
CREATE TABLE fleet_github_dispatches (plan_id text PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE, plan_run_id bigint NOT NULL, plan_sha256 text NOT NULL, approved_head_sha text NOT NULL, pilot_run_id text NOT NULL DEFAULT '', fleet_environment text NOT NULL, allow_destructive boolean NOT NULL, dispatch_nonce_sha256 text NOT NULL, dispatch_state text NOT NULL DEFAULT 'prepared', submission_started_at timestamptz, run_id bigint NOT NULL DEFAULT 0, workflow_url text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), approval_envelope_sha256 text NOT NULL DEFAULT '', run_attempt integer NOT NULL DEFAULT 0, rerun_started_at timestamptz);
CREATE TABLE external_deployment_nonces (id text PRIMARY KEY, app text NOT NULL, ci_repository text NOT NULL, ci_run_attempt text NOT NULL, ci_run_id text NOT NULL, claimed_at timestamptz, consumed_at timestamptz, environment text NOT NULL, expires_at timestamptz NOT NULL, issued_at timestamptz NOT NULL DEFAULT now(), issuer_subject text NOT NULL DEFAULT '', issuer_token_id text NOT NULL DEFAULT '', nonce_sha256 text NOT NULL, registered_at timestamptz, registration_generation bigint NOT NULL DEFAULT 0, registration_metadata jsonb NOT NULL DEFAULT '{}', registration_ref text NOT NULL DEFAULT '', revision bigint NOT NULL DEFAULT 1, state text NOT NULL DEFAULT 'ready', superseded_at timestamptz, admission_id text NOT NULL DEFAULT '');
CREATE TABLE external_deployment_admissions (id text PRIMARY KEY, idempotency_key text NOT NULL UNIQUE, app text NOT NULL, environment text NOT NULL, ci_repository text NOT NULL, request_digest text NOT NULL, state text NOT NULL DEFAULT 'initiated', nonce_id text REFERENCES external_deployment_nonces(id) ON DELETE SET NULL, nonce_generation bigint NOT NULL DEFAULT 0, operation_id text REFERENCES operations(id) ON DELETE SET NULL, claimed_receipt jsonb NOT NULL DEFAULT '{}', registration_ref text NOT NULL DEFAULT '', service_snapshot_id text NOT NULL DEFAULT '', service_snapshot_ref text NOT NULL DEFAULT '', service_snapshot_sha256 text NOT NULL DEFAULT '', service_receipt_sha256 text NOT NULL DEFAULT '', service_proof_sha256 text NOT NULL DEFAULT '', service_retry_lineage jsonb NOT NULL DEFAULT '[]', service_claim_revision bigint NOT NULL DEFAULT 0, service_commit_revision bigint NOT NULL DEFAULT 0, service_cleanup_revision bigint NOT NULL DEFAULT 0, cleanup_intent_sha256 text NOT NULL DEFAULT '', absence_proof_sha256 text NOT NULL DEFAULT '', failure_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz);
CREATE TABLE external_deployment_admission_checkpoints (admission_id text NOT NULL REFERENCES external_deployment_admissions(id) ON DELETE CASCADE, phase text NOT NULL, attempt_id text NOT NULL, checkpoint_id text NOT NULL, evidence_ref text NOT NULL, evidence_sha256 text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(admission_id,phase), UNIQUE(admission_id,checkpoint_id));
CREATE TABLE fleet_runner_checkpoint_refs (admission_id text NOT NULL REFERENCES external_deployment_admissions(id) ON DELETE CASCADE, phase text NOT NULL, attempt_id text NOT NULL, checkpoint_id text NOT NULL, evidence_ref text NOT NULL, evidence_sha256 text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(admission_id,phase), UNIQUE(admission_id,checkpoint_id));`

func createMiniPilotFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), miniPilotFixtureSQL); err != nil {
		t.Fatal(err)
	}
}

func miniFixtureMigrator(t *testing.T, pool *pgxpool.Pool) *SchemaMigrator {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := miniSchemaStructuralFingerprint(context.Background(), tx)
	if rollbackErr := tx.Rollback(context.Background()); err == nil && rollbackErr != nil {
		err = rollbackErr
	}
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := NewControlSchemaMigrator(&DB{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	migrator.adoptUnversioned = func(ctx context.Context, tx pgx.Tx) error {
		return adoptMiniControlSchemaFingerprint(ctx, tx, fingerprint, []string{"external_deployment_admission_checkpoints", "external_deployment_admissions", "external_deployment_nonces", "fleet_github_dispatches", "fleet_runner_attempts", "fleet_runner_checkpoint_refs", "operations"})
	}
	return migrator
}
