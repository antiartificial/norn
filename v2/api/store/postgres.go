package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
)

type DB struct {
	Pool *pgxpool.Pool
}

// migrationAdvisoryLockKey serializes the whole idempotent schema program
// across API processes and parallel package tests sharing a PostgreSQL 16
// database. It is held by one acquired session, so a crash releases it with
// the connection and cannot leave a durable migration lock behind.
const migrationAdvisoryLockKey int64 = 0x4e4f524e5f4d4947

func Connect(databaseURL string) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	poolConfig, err := operationPoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

func operationPoolConfig(databaseURL string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// One worker can simultaneously hold its per-app advisory-lock connection,
	// execute pipeline SQL, and renew the operation lease. Keep a fourth slot for
	// API reads and shutdown/recovery bookkeeping rather than allowing a
	// pool_max_conns setting to deadlock durable work.
	if config.MaxConns < 4 {
		return nil, fmt.Errorf("postgres pool_max_conns must be at least 4 for durable operation locking")
	}
	return config, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

func Migrate(db *DB) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("postgres migration database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire postgres migration session: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("lock postgres migration: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockKey)
	}()
	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS saga_events (
			id         TEXT PRIMARY KEY,
			saga_id    TEXT NOT NULL,
			timestamp  TIMESTAMPTZ NOT NULL DEFAULT now(),
			source     TEXT NOT NULL DEFAULT '',
			app        TEXT NOT NULL DEFAULT '',
			category   TEXT NOT NULL DEFAULT '',
			action     TEXT NOT NULL DEFAULT '',
			message    TEXT NOT NULL DEFAULT '',
			metadata   JSONB NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_saga_saga_id ON saga_events(saga_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_saga_app ON saga_events(app, timestamp DESC);

		CREATE TABLE IF NOT EXISTS control_events (
			id         BIGSERIAL PRIMARY KEY,
			timestamp  TIMESTAMPTZ NOT NULL DEFAULT now(),
			type       TEXT NOT NULL,
			app_id     TEXT NOT NULL DEFAULT '',
			payload    JSONB NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_control_events_time ON control_events(timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_control_events_app ON control_events(app_id, id DESC);

		CREATE TABLE IF NOT EXISTS deployments (
			id          TEXT PRIMARY KEY,
			app         TEXT NOT NULL,
			commit_sha  TEXT NOT NULL,
			image_tag   TEXT NOT NULL,
			environment TEXT NOT NULL DEFAULT '',
			saga_id     TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'running',
			source_kind TEXT NOT NULL DEFAULT '',
			source_ref  TEXT NOT NULL DEFAULT '',
			source_dirty BOOLEAN NOT NULL DEFAULT false,
			source_changes JSONB NOT NULL DEFAULT '[]',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_deployments_app ON deployments(app, started_at DESC);

		CREATE TABLE IF NOT EXISTS deployment_regions (
			deployment_id  TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
			region         TEXT NOT NULL,
			nomad_region   TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'queued',
			desired_weight INT NOT NULL DEFAULT 0,
			active_weight  INT NOT NULL DEFAULT 0,
			eval_id        TEXT NOT NULL DEFAULT '',
			last_error     TEXT NOT NULL DEFAULT '',
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (deployment_id, region)
		);
		CREATE INDEX IF NOT EXISTS idx_deployment_regions_region ON deployment_regions(region, updated_at DESC);

		ALTER TABLE deployments ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT '';
		ALTER TABLE deployments ADD COLUMN IF NOT EXISTS environment TEXT NOT NULL DEFAULT '';
		ALTER TABLE deployments ADD COLUMN IF NOT EXISTS source_ref TEXT NOT NULL DEFAULT '';
		ALTER TABLE deployments ADD COLUMN IF NOT EXISTS source_dirty BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE deployments ADD COLUMN IF NOT EXISTS source_changes JSONB NOT NULL DEFAULT '[]';

		CREATE TABLE IF NOT EXISTS deployment_steps (
			deployment_id TEXT NOT NULL,
			app           TEXT NOT NULL DEFAULT '',
			saga_id       TEXT NOT NULL DEFAULT '',
			step          TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'running',
			attempt       INT NOT NULL DEFAULT 0,
			started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at   TIMESTAMPTZ,
			duration_ms   BIGINT NOT NULL DEFAULT 0,
			message       TEXT NOT NULL DEFAULT '',
			metadata      JSONB NOT NULL DEFAULT '{}',
			PRIMARY KEY (deployment_id, step)
		);
		ALTER TABLE deployment_steps ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_deployment_steps_saga ON deployment_steps(saga_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_deployment_steps_app ON deployment_steps(app, started_at DESC);

		CREATE TABLE IF NOT EXISTS cron_states (
			app        TEXT NOT NULL,
			process    TEXT NOT NULL,
			paused     BOOLEAN NOT NULL DEFAULT false,
			schedule   TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (app, process)
		);

		CREATE TABLE IF NOT EXISTS func_executions (
			id          TEXT PRIMARY KEY,
			app         TEXT NOT NULL,
			process     TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'running',
			exit_code   INT,
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ,
			duration_ms BIGINT
		);
		CREATE INDEX IF NOT EXISTS idx_func_exec_app ON func_executions(app, started_at DESC);

		CREATE TABLE IF NOT EXISTS beacon_events (
			id          TEXT PRIMARY KEY,
			source      TEXT NOT NULL DEFAULT 'norn',
			app         TEXT NOT NULL DEFAULT '',
			environment TEXT NOT NULL DEFAULT '',
			type        TEXT NOT NULL,
			severity    TEXT NOT NULL DEFAULT 'info',
			title       TEXT NOT NULL,
			body        TEXT NOT NULL DEFAULT '',
			dedupe_key  TEXT NOT NULL DEFAULT '',
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			acknowledged_at TIMESTAMPTZ,
			acknowledged_by TEXT NOT NULL DEFAULT '',
			acknowledgement_note TEXT NOT NULL DEFAULT '',
			snoozed_until TIMESTAMPTZ,
			metadata    JSONB NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_beacon_events_time ON beacon_events(occurred_at DESC);
		CREATE INDEX IF NOT EXISTS idx_beacon_events_app ON beacon_events(app, occurred_at DESC);
		CREATE INDEX IF NOT EXISTS idx_beacon_events_type ON beacon_events(type, occurred_at DESC);
		CREATE INDEX IF NOT EXISTS idx_beacon_events_dedupe ON beacon_events(dedupe_key, occurred_at DESC);
		ALTER TABLE beacon_events ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMPTZ;
		ALTER TABLE beacon_events ADD COLUMN IF NOT EXISTS acknowledged_by TEXT NOT NULL DEFAULT '';
		ALTER TABLE beacon_events ADD COLUMN IF NOT EXISTS acknowledgement_note TEXT NOT NULL DEFAULT '';
		ALTER TABLE beacon_events ADD COLUMN IF NOT EXISTS snoozed_until TIMESTAMPTZ;
		CREATE INDEX IF NOT EXISTS idx_beacon_events_ack ON beacon_events(acknowledged_at, snoozed_until, occurred_at DESC);

		CREATE TABLE IF NOT EXISTS operations (
			id          TEXT PRIMARY KEY,
			kind        TEXT NOT NULL,
			app         TEXT NOT NULL DEFAULT '',
			saga_id     TEXT NOT NULL DEFAULT '',
			ref         TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'running',
			risk        TEXT NOT NULL DEFAULT '',
			source      TEXT NOT NULL DEFAULT '',
			message     TEXT NOT NULL DEFAULT '',
			payload     JSONB NOT NULL DEFAULT '{}',
			metadata    JSONB NOT NULL DEFAULT '{}',
			attempts    INT NOT NULL DEFAULT 0,
			max_attempts INT NOT NULL DEFAULT 1,
			locked_by  TEXT NOT NULL DEFAULT '',
			locked_until TIMESTAMPTZ,
			next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_error TEXT NOT NULL DEFAULT '',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_operations_status ON operations(status, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_operations_app ON operations(app, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_operations_saga ON operations(saga_id);
		CREATE INDEX IF NOT EXISTS idx_operations_kind ON operations(kind, started_at DESC);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_operations_idempotency
			ON operations ((metadata->>'idempotencyKey'))
			WHERE metadata ? 'idempotencyKey' AND metadata->>'idempotencyKey' <> '';

		CREATE TABLE IF NOT EXISTS fleet_runner_attempts (
			id                        TEXT PRIMARY KEY,
			plan_id                   TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
			attempt                   INT NOT NULL,
			root_attempt_id           TEXT NOT NULL DEFAULT '',
			source_dispatch_run_id    BIGINT NOT NULL DEFAULT 0,
			pilot_run_id              TEXT NOT NULL DEFAULT '',
			recovery                  BOOLEAN NOT NULL DEFAULT false,
			runner_attempt_id         TEXT NOT NULL DEFAULT '',
			status                    TEXT NOT NULL DEFAULT 'queued',
			current_phase             TEXT NOT NULL,
			commit_sha                TEXT NOT NULL,
			plan_sha256               TEXT NOT NULL,
			workflow_url              TEXT NOT NULL DEFAULT '',
			principal_subject         TEXT NOT NULL DEFAULT '',
			retry_of                  TEXT NOT NULL DEFAULT '',
			heartbeat_sequence        BIGINT NOT NULL DEFAULT 0,
			heartbeat_timeout_seconds INT NOT NULL DEFAULT 120,
			revision                  BIGINT NOT NULL DEFAULT 1,
			started_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
			phase_started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			heartbeat_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at               TIMESTAMPTZ,
			last_error                TEXT NOT NULL DEFAULT '',
			metadata                  JSONB NOT NULL DEFAULT '{}',
			CHECK (attempt > 0),
			CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled', 'abandoned')),
			CHECK (heartbeat_sequence >= 0),
			CHECK (heartbeat_timeout_seconds BETWEEN 30 AND 900),
			CHECK (revision > 0),
			UNIQUE (plan_id, attempt)
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_runner_external_attempt
			ON fleet_runner_attempts(plan_id, runner_attempt_id)
			WHERE runner_attempt_id <> '';
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_runner_one_live_attempt
			ON fleet_runner_attempts(plan_id)
			WHERE status IN ('queued', 'running');
		CREATE INDEX IF NOT EXISTS idx_fleet_runner_plan
			ON fleet_runner_attempts(plan_id, attempt DESC);
		CREATE INDEX IF NOT EXISTS idx_fleet_runner_liveness
			ON fleet_runner_attempts(status, heartbeat_at);
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS root_attempt_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS source_dispatch_run_id BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS pilot_run_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS recovery BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS phase_started_at TIMESTAMPTZ;
		-- Legacy records predate phase timing. Their original attempt start is the
		-- only honest lower bound; new attempts set this server-owned field exactly.
		UPDATE fleet_runner_attempts SET phase_started_at = started_at WHERE phase_started_at IS NULL;
		ALTER TABLE fleet_runner_attempts ALTER COLUMN phase_started_at SET NOT NULL;
		-- Existing durable histories predate root_attempt_id. Backfill every
		-- member of each plan lineage from its immutable first attempt.
		UPDATE fleet_runner_attempts target SET root_attempt_id = first_attempt.id
		FROM (SELECT DISTINCT ON (plan_id) plan_id, id FROM fleet_runner_attempts ORDER BY plan_id, attempt ASC) first_attempt
		WHERE target.plan_id = first_attempt.plan_id AND target.root_attempt_id = '';

		CREATE TABLE IF NOT EXISTS fleet_github_dispatches (
			plan_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
			plan_run_id BIGINT NOT NULL,
			plan_sha256 TEXT NOT NULL,
			approved_head_sha TEXT NOT NULL,
			pilot_run_id TEXT NOT NULL DEFAULT '',
			fleet_environment TEXT NOT NULL,
			allow_destructive BOOLEAN NOT NULL,
			dispatch_nonce_sha256 TEXT NOT NULL,
			dispatch_state TEXT NOT NULL DEFAULT 'prepared',
			submission_started_at TIMESTAMPTZ,
			run_id BIGINT NOT NULL DEFAULT 0,
			workflow_url TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS dispatch_state TEXT NOT NULL DEFAULT 'prepared';
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS submission_started_at TIMESTAMPTZ;
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS pilot_run_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_github_dispatches DROP COLUMN IF EXISTS dispatch_nonce;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_github_dispatch_nonce ON fleet_github_dispatches(dispatch_nonce_sha256);

		ALTER TABLE operations ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS max_attempts INT NOT NULL DEFAULT 1;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS locked_by TEXT NOT NULL DEFAULT '';
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_operations_queue ON operations(status, next_attempt_at, kind);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_operations_promotion_qualification
			ON operations ((metadata->'promotionQualification'->>'id'))
			WHERE kind = 'app.deploy' AND metadata->'promotionQualification'->>'id' <> '';

		CREATE TABLE IF NOT EXISTS github_actions_assertion_uses (
			issuer TEXT NOT NULL,
			jti TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (issuer, jti)
		);
		CREATE INDEX IF NOT EXISTS idx_github_actions_assertion_uses_expiry ON github_actions_assertion_uses(expires_at);

		-- The raw Norn-issued external-admission nonce is never retained. The
		-- hash is bound to the exact protected Fleet CI run and can be consumed
		-- once only after independent runtime verification succeeds.
		CREATE TABLE IF NOT EXISTS external_deployment_nonces (
			id TEXT PRIMARY KEY,
			nonce_sha256 TEXT NOT NULL UNIQUE,
			app TEXT NOT NULL,
			environment TEXT NOT NULL,
			ci_repository TEXT NOT NULL,
			ci_run_id TEXT NOT NULL,
			ci_run_attempt TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			consumed_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_external_deployment_nonces_expiry ON external_deployment_nonces(expires_at);

		-- The admission row is deliberately separate from operations: it starts
		-- before an external verifier is invoked and preserves nonce issuance and
		-- failure lifecycle without storing an unredacted receipt or nonce.
		CREATE TABLE IF NOT EXISTS external_deployment_admissions (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			app TEXT NOT NULL,
			environment TEXT NOT NULL,
			ci_repository TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'initiated',
			nonce_id TEXT REFERENCES external_deployment_nonces(id) ON DELETE SET NULL,
			nonce_generation BIGINT NOT NULL DEFAULT 0,
			registration_ref TEXT NOT NULL DEFAULT '',
			operation_id TEXT REFERENCES operations(id) ON DELETE SET NULL,
			failure_code TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at TIMESTAMPTZ,
			CHECK (state IN ('initiated','nonce_registering','nonce_ready','evidence_claimed','committed','cleanup_pending','complete','expired')),
			CHECK (nonce_generation >= 0)
		);
		CREATE INDEX IF NOT EXISTS idx_external_deployment_admissions_nonce ON external_deployment_admissions(nonce_id);
		CREATE INDEX IF NOT EXISTS idx_external_deployment_admissions_state ON external_deployment_admissions(state, updated_at DESC);

		-- These ALTERs make the v4 lifecycle additive for an already-running
		-- control plane. Legacy nonce rows retain empty registration bindings.
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS admission_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS registration_generation BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS registration_ref TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS issuer_subject TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS issuer_token_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS registration_metadata JSONB NOT NULL DEFAULT '{}';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'ready';
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ;
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS registered_at TIMESTAMPTZ;
		ALTER TABLE external_deployment_nonces ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_snapshot_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_snapshot_ref TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_snapshot_sha256 TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_retry_lineage JSONB NOT NULL DEFAULT '[]';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_receipt_sha256 TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_proof_sha256 TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_claim_revision BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_commit_revision BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS service_cleanup_revision BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS cleanup_intent_sha256 TEXT NOT NULL DEFAULT '';
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS absence_proof_sha256 TEXT NOT NULL DEFAULT '';
		DO $$ BEGIN
			ALTER TABLE external_deployment_nonces ADD CONSTRAINT external_deployment_nonces_state_check
				CHECK (state IN ('registering','ready','claimed','superseded','expired'));
		EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		DO $$ BEGIN
			ALTER TABLE external_deployment_nonces ADD CONSTRAINT external_deployment_nonces_revision_check CHECK (revision > 0);
		EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_external_deployment_nonces_admission_generation
			ON external_deployment_nonces(admission_id, registration_generation)
			WHERE admission_id <> '';
		CREATE INDEX IF NOT EXISTS idx_external_deployment_nonces_admission ON external_deployment_nonces(admission_id)
			WHERE admission_id <> '';

		CREATE TABLE IF NOT EXISTS external_deployment_admission_checkpoints (
			admission_id TEXT NOT NULL REFERENCES external_deployment_admissions(id) ON DELETE CASCADE,
			phase TEXT NOT NULL,
			checkpoint_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			evidence_ref TEXT NOT NULL,
			evidence_sha256 TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (admission_id, phase),
			UNIQUE (admission_id, checkpoint_id)
		);

		-- Keep the append-only checkpoint pointers in their own durable namespace
		-- as well as the admission-context projection above. The duplicate
		-- projection preserves the v4 context API while this table is the
		-- server-owned runner evidence ledger used for reconciliation.
		CREATE TABLE IF NOT EXISTS fleet_runner_checkpoint_refs (
			admission_id TEXT NOT NULL REFERENCES external_deployment_admissions(id) ON DELETE CASCADE,
			phase TEXT NOT NULL,
			checkpoint_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			evidence_ref TEXT NOT NULL,
			evidence_sha256 TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (admission_id, phase),
			UNIQUE (admission_id, checkpoint_id)
		);

		CREATE TABLE IF NOT EXISTS webhook_deliveries (
			id          TEXT PRIMARY KEY,
			provider    TEXT NOT NULL,
			event       TEXT NOT NULL DEFAULT '',
			delivery_id TEXT NOT NULL DEFAULT '',
			repository  TEXT NOT NULL DEFAULT '',
			ref         TEXT NOT NULL DEFAULT '',
			branch      TEXT NOT NULL DEFAULT '',
			app         TEXT NOT NULL DEFAULT '',
			saga_id     TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'received',
			reason      TEXT NOT NULL DEFAULT '',
			remote_addr TEXT NOT NULL DEFAULT '',
			user_agent  TEXT NOT NULL DEFAULT '',
			payload     JSONB NOT NULL DEFAULT '{}',
			metadata    JSONB NOT NULL DEFAULT '{}',
			received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_received ON webhook_deliveries(received_at DESC);
		CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_provider ON webhook_deliveries(provider, received_at DESC);
		CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_status ON webhook_deliveries(status, received_at DESC);

		ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';

		CREATE TABLE IF NOT EXISTS notification_channels (
			id         TEXT PRIMARY KEY,
			provider   TEXT NOT NULL,
			name       TEXT NOT NULL,
			url        TEXT NOT NULL DEFAULT '',
			token      TEXT NOT NULL DEFAULT '',
			user_key   TEXT NOT NULL DEFAULT '',
			severities JSONB NOT NULL DEFAULT '[]',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);

		CREATE TABLE IF NOT EXISTS access_grants (
			id         TEXT PRIMARY KEY,
			ip         TEXT NOT NULL,
			note       TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_access_grants_ip ON access_grants(ip, expires_at);
		CREATE INDEX IF NOT EXISTS idx_access_grants_expires ON access_grants(expires_at);

		CREATE TABLE IF NOT EXISTS access_devices (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			platform    TEXT NOT NULL DEFAULT '',
			model       TEXT NOT NULL DEFAULT '',
			app_version TEXT NOT NULL DEFAULT '',
			public_key  TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_seen_at TIMESTAMPTZ,
			revoked_at  TIMESTAMPTZ
		);
		ALTER TABLE access_devices ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_access_devices_active ON access_devices(revoked_at, created_at DESC);

		CREATE TABLE IF NOT EXISTS access_tokens (
			jti          TEXT PRIMARY KEY,
			device_id    TEXT REFERENCES access_devices(id) ON DELETE CASCADE,
			subject      TEXT NOT NULL DEFAULT '',
			scopes       JSONB NOT NULL DEFAULT '[]',
			issued_at    TIMESTAMPTZ NOT NULL,
			expires_at   TIMESTAMPTZ NOT NULL,
			revoked_at   TIMESTAMPTZ,
			rotated_from TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_access_tokens_device ON access_tokens(device_id, issued_at DESC);
		CREATE INDEX IF NOT EXISTS idx_access_tokens_active ON access_tokens(revoked_at, expires_at);

		CREATE TABLE IF NOT EXISTS access_enrollments (
			id               TEXT PRIMARY KEY,
			code_hash        TEXT NOT NULL UNIQUE,
			verifier_hash    TEXT NOT NULL,
			device_name      TEXT NOT NULL,
			platform         TEXT NOT NULL DEFAULT '',
			model            TEXT NOT NULL DEFAULT '',
			app_version      TEXT NOT NULL DEFAULT '',
			public_key       TEXT NOT NULL DEFAULT '',
			requested_scopes JSONB NOT NULL DEFAULT '[]',
			approved_scopes  JSONB NOT NULL DEFAULT '[]',
			source_hash      TEXT NOT NULL DEFAULT '',
			verifier_attempts INT NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'pending',
			device_id        TEXT NOT NULL DEFAULT '',
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at       TIMESTAMPTZ NOT NULL,
			approved_at      TIMESTAMPTZ,
			exchanged_at     TIMESTAMPTZ
		);
		ALTER TABLE access_enrollments ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE access_enrollments ADD COLUMN IF NOT EXISTS source_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE access_enrollments ADD COLUMN IF NOT EXISTS verifier_attempts INT NOT NULL DEFAULT 0;
		CREATE INDEX IF NOT EXISTS idx_access_enrollments_status ON access_enrollments(status, expires_at);
		CREATE INDEX IF NOT EXISTS idx_access_enrollments_source ON access_enrollments(source_hash, created_at DESC);

		CREATE TABLE IF NOT EXISTS step_up_challenges (
			id          TEXT PRIMARY KEY,
			device_id   TEXT NOT NULL REFERENCES access_devices(id) ON DELETE CASCADE,
			token_jti   TEXT NOT NULL,
			purpose     TEXT NOT NULL,
			resource    TEXT NOT NULL,
			nonce_hash  TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'pending',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at  TIMESTAMPTZ NOT NULL,
			verified_at TIMESTAMPTZ,
			consumed_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_step_up_challenges_device ON step_up_challenges(device_id, created_at DESC);

		CREATE TABLE IF NOT EXISTS exec_sessions (
			id            TEXT PRIMARY KEY,
			device_id     TEXT NOT NULL REFERENCES access_devices(id),
			token_jti     TEXT NOT NULL,
			challenge_id  TEXT NOT NULL REFERENCES step_up_challenges(id),
			app_id        TEXT NOT NULL,
			allocation_id TEXT NOT NULL,
			task          TEXT NOT NULL,
			command       JSONB NOT NULL DEFAULT '[]',
			command_digest TEXT NOT NULL DEFAULT '',
			terminal      BOOLEAN NOT NULL DEFAULT true,
			columns       INT NOT NULL DEFAULT 80,
			rows          INT NOT NULL DEFAULT 24,
			status        TEXT NOT NULL DEFAULT 'pending',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at    TIMESTAMPTZ NOT NULL,
			connected_at  TIMESTAMPTZ,
			finished_at   TIMESTAMPTZ,
			exit_code     INT,
			error_code    TEXT NOT NULL DEFAULT '',
			remote_addr   TEXT NOT NULL DEFAULT '',
			user_agent    TEXT NOT NULL DEFAULT ''
		);
		ALTER TABLE exec_sessions ADD COLUMN IF NOT EXISTS command_digest TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_exec_sessions_device ON exec_sessions(device_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_exec_sessions_status ON exec_sessions(status, expires_at);

		CREATE TABLE IF NOT EXISTS mutation_audit_events (
			id                TEXT PRIMARY KEY,
			request_id        TEXT NOT NULL DEFAULT '',
			principal_subject TEXT NOT NULL DEFAULT '',
			token_id          TEXT NOT NULL DEFAULT '',
			device_id         TEXT NOT NULL DEFAULT '',
			scopes            JSONB NOT NULL DEFAULT '[]',
			method            TEXT NOT NULL,
			path              TEXT NOT NULL,
			client_ip         TEXT NOT NULL DEFAULT '',
			user_agent        TEXT NOT NULL DEFAULT '',
			status            INT NOT NULL DEFAULT 0,
			outcome           TEXT NOT NULL DEFAULT 'started',
			started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at       TIMESTAMPTZ,
			duration_ms       BIGINT NOT NULL DEFAULT 0,
			record_digest     TEXT NOT NULL DEFAULT ''
		);
		ALTER TABLE mutation_audit_events ADD COLUMN IF NOT EXISTS key_id TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_mutation_audit_started ON mutation_audit_events(started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_mutation_audit_principal ON mutation_audit_events(principal_subject, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_mutation_audit_outcome ON mutation_audit_events(outcome, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_mutation_audit_finished ON mutation_audit_events(finished_at) WHERE finished_at IS NOT NULL;

		CREATE TABLE IF NOT EXISTS mutation_audit_incidents (
			id                TEXT PRIMARY KEY,
			audit_event_id    TEXT NOT NULL UNIQUE,
			reason_code       TEXT NOT NULL,
			explanation       TEXT NOT NULL,
			acknowledged_by   TEXT NOT NULL,
			acknowledged_at   TIMESTAMPTZ NOT NULL,
			key_id            TEXT NOT NULL DEFAULT '',
			record_digest     TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_mutation_audit_incident_time ON mutation_audit_incidents(acknowledged_at DESC);

		CREATE TABLE IF NOT EXISTS recovery_drills (
			id            TEXT PRIMARY KEY,
			kind          TEXT NOT NULL,
			target        TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'running',
			initiated_by  TEXT NOT NULL DEFAULT '',
			evidence      JSONB NOT NULL DEFAULT '{}',
			started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at   TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_recovery_drills_kind ON recovery_drills(kind, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_recovery_drills_status ON recovery_drills(status, started_at DESC);

		CREATE TABLE IF NOT EXISTS access_observation_buckets (
			app            TEXT NOT NULL,
			process        TEXT NOT NULL DEFAULT '',
			endpoint       TEXT NOT NULL DEFAULT '',
			source         TEXT NOT NULL DEFAULT '',
			bucket_start   TIMESTAMPTZ NOT NULL,
			requests       BIGINT NOT NULL DEFAULT 0,
			successes      BIGINT NOT NULL DEFAULT 0,
			client_errors  BIGINT NOT NULL DEFAULT 0,
			server_errors  BIGINT NOT NULL DEFAULT 0,
			first_seen     TIMESTAMPTZ NOT NULL,
			last_seen      TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (app, process, endpoint, source, bucket_start)
		);
		CREATE INDEX IF NOT EXISTS idx_access_observation_app_last ON access_observation_buckets(app, process, last_seen DESC);
		CREATE INDEX IF NOT EXISTS idx_access_observation_bucket ON access_observation_buckets(bucket_start DESC);
	`)
	return err
}

func (db *DB) InsertDeployment(ctx context.Context, d *model.Deployment) error {
	changes, _ := json.Marshal(d.SourceChanges)
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO deployments (id, app, commit_sha, image_tag, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		d.ID, d.App, d.CommitSHA, d.ImageTag, d.SagaID, d.Status, d.SourceKind, d.SourceRef, d.SourceDirty, changes, d.StartedAt,
	)
	return err
}

func (db *DB) InsertDeploymentRegions(ctx context.Context, deploymentID string, regions []model.ResolvedRegion) error {
	for _, region := range regions {
		_, err := db.Pool.Exec(ctx, `INSERT INTO deployment_regions
			(deployment_id, region, nomad_region, status, desired_weight, active_weight)
			VALUES ($1, $2, $3, $4, $5, 0)
			ON CONFLICT (deployment_id, region) DO NOTHING`,
			deploymentID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight)
		if err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) UpdateDeploymentRegion(ctx context.Context, deploymentID, region string, status model.DeployStatus, evalID, lastError string, activeWeight int) error {
	_, err := db.Pool.Exec(ctx, `UPDATE deployment_regions SET status=$1,
		eval_id=CASE WHEN $2 = '' THEN eval_id ELSE $2 END,
		last_error=$3, active_weight=$4, updated_at=now() WHERE deployment_id=$5 AND region=$6`,
		status, evalID, lastError, activeWeight, deploymentID, region)
	return err
}

// FailIncompleteDeploymentRegions closes every region that did not reach a
// terminal state. Keeping active_weight at zero prevents a traffic controller
// from promoting a partially completed deployment.
func (db *DB) FailIncompleteDeploymentRegions(ctx context.Context, deploymentID, lastError string) error {
	_, err := db.Pool.Exec(ctx, `UPDATE deployment_regions
		SET status='failed', active_weight=0,
			last_error=CASE WHEN last_error = '' THEN $2 ELSE last_error END,
			updated_at=now()
		WHERE deployment_id=$1 AND status NOT IN ('deployed', 'failed')`, deploymentID, lastError)
	return err
}

func (db *DB) DeploymentRegions(ctx context.Context, deploymentID string) ([]model.DeploymentRegion, error) {
	rows, err := db.Pool.Query(ctx, `SELECT deployment_id, region, nomad_region, status,
		desired_weight, active_weight, eval_id, last_error, updated_at
		FROM deployment_regions WHERE deployment_id=$1 ORDER BY region`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DeploymentRegion
	for rows.Next() {
		var region model.DeploymentRegion
		if err := rows.Scan(&region.DeploymentID, &region.Region, &region.NomadRegion, &region.Status,
			&region.DesiredWeight, &region.ActiveWeight, &region.EvalID, &region.LastError, &region.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, region)
	}
	return out, rows.Err()
}

func (db *DB) UpdateDeployment(ctx context.Context, id string, status model.DeployStatus) error {
	var finished *time.Time
	if status == model.StatusDeployed || status == model.StatusFailed {
		now := time.Now()
		finished = &now
	}
	_, err := db.Pool.Exec(ctx,
		`UPDATE deployments SET status = $1, finished_at = $2 WHERE id = $3`,
		status, finished, id,
	)
	return err
}

func (db *DB) UpdateDeploymentResult(ctx context.Context, d *model.Deployment) error {
	changes, _ := json.Marshal(d.SourceChanges)
	var finished *time.Time
	if d.Status == model.StatusDeployed || d.Status == model.StatusFailed {
		now := time.Now()
		finished = &now
	}
	_, err := db.Pool.Exec(ctx,
		`UPDATE deployments
		 SET status = $1, commit_sha = $2, image_tag = $3, source_kind = $4, source_ref = $5, source_dirty = $6, source_changes = $7, finished_at = $8
		 WHERE id = $9`,
		d.Status, d.CommitSHA, d.ImageTag, d.SourceKind, d.SourceRef, d.SourceDirty, changes, finished, d.ID,
	)
	return err
}

func (db *DB) ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error) {
	return db.ListDeploymentsPage(ctx, app, "", limit, 0)
}

func (db *DB) ListDeploymentsPage(ctx context.Context, app, status string, limit, offset int) ([]model.Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments`
	args := []interface{}{}
	conditions := []string{}
	if app != "" {
		args = append(args, app)
		conditions = append(conditions, fmt.Sprintf("app = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []model.Deployment
	for rows.Next() {
		var d model.Deployment
		var changes []byte
		if err := rows.Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(changes, &d.SourceChanges)
		d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
		deployments = append(deployments, d)
	}
	return deployments, nil
}

func (db *DB) GetDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx,
		`SELECT id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments
		 WHERE id = $1`,
		id,
	).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

func (db *DB) LastSuccessfulDeployment(ctx context.Context, app, excludeID string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx,
		`SELECT id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments
		 WHERE app = $1 AND status = 'deployed' AND id != $2
		 ORDER BY started_at DESC LIMIT 1`,
		app, excludeID,
	).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

func (db *DB) RecoverInFlightDeployments(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx,
		`WITH recovered AS (
			SELECT id FROM deployments WHERE status NOT IN ('deployed', 'failed')
		), failed_regions AS (
			UPDATE deployment_regions
			SET status='failed', active_weight=0,
				last_error=CASE WHEN last_error = '' THEN 'norn restarted during deployment' ELSE last_error END,
				updated_at=now()
			WHERE deployment_id IN (SELECT id FROM recovered)
		)
		UPDATE deployments SET status = 'failed', finished_at = now()
		WHERE id IN (SELECT id FROM recovered)`,
	)
	return err
}

// Healthy checks the database connection.
func (db *DB) Healthy(ctx context.Context) error {
	var n int
	return db.Pool.QueryRow(ctx, "SELECT 1").Scan(&n)
}

// DailyStats returns basic deployment statistics for today.
type DailyStats struct {
	Total          int    `json:"total"`
	Success        int    `json:"success"`
	Failed         int    `json:"failed"`
	MostPopularApp string `json:"mostPopularApp,omitempty"`
	MostPopularN   int    `json:"mostPopularN,omitempty"`
}

func (db *DB) GetDailyStats(ctx context.Context) (*DailyStats, error) {
	s := &DailyStats{}
	err := db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'deployed'),
			COUNT(*) FILTER (WHERE status = 'failed')
		FROM deployments
		WHERE started_at >= CURRENT_DATE
	`).Scan(&s.Total, &s.Success, &s.Failed)
	if err != nil {
		return nil, fmt.Errorf("daily stats: %w", err)
	}

	// Most popular app today
	var app *string
	var n *int
	_ = db.Pool.QueryRow(ctx, `
		SELECT app, COUNT(*) as n FROM deployments
		WHERE started_at >= CURRENT_DATE
		GROUP BY app ORDER BY n DESC LIMIT 1
	`).Scan(&app, &n)
	if app != nil {
		s.MostPopularApp = *app
		s.MostPopularN = *n
	}

	return s, nil
}

type DeploymentMetric struct {
	App             string             `json:"app"`
	Status          model.DeployStatus `json:"status"`
	Count           int64              `json:"count"`
	DurationSeconds float64            `json:"durationSeconds"`
	LastStartedUnix float64            `json:"lastStartedUnix"`
}

func (db *DB) DeploymentMetrics(ctx context.Context) ([]DeploymentMetric, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT
			app,
			status,
			COUNT(*)::bigint,
			COALESCE(SUM(EXTRACT(EPOCH FROM (finished_at - started_at))) FILTER (WHERE finished_at IS NOT NULL), 0)::float8,
			COALESCE(EXTRACT(EPOCH FROM MAX(started_at)), 0)::float8
		FROM deployments
		GROUP BY app, status
		ORDER BY app, status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []DeploymentMetric
	for rows.Next() {
		var metric DeploymentMetric
		if err := rows.Scan(&metric.App, &metric.Status, &metric.Count, &metric.DurationSeconds, &metric.LastStartedUnix); err != nil {
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	return metrics, rows.Err()
}

// CronState represents the pause/schedule state of a cron process.
type CronState struct {
	App       string    `json:"app"`
	Process   string    `json:"process"`
	Paused    bool      `json:"paused"`
	Schedule  string    `json:"schedule"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (db *DB) GetCronState(ctx context.Context, app, process string) (*CronState, error) {
	var cs CronState
	err := db.Pool.QueryRow(ctx,
		`SELECT app, process, paused, schedule, updated_at FROM cron_states WHERE app = $1 AND process = $2`,
		app, process,
	).Scan(&cs.App, &cs.Process, &cs.Paused, &cs.Schedule, &cs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cs, nil
}

func (db *DB) GetCronStates(ctx context.Context, app string) ([]CronState, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT app, process, paused, schedule, updated_at FROM cron_states WHERE app = $1 ORDER BY process`,
		app,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []CronState
	for rows.Next() {
		var cs CronState
		if err := rows.Scan(&cs.App, &cs.Process, &cs.Paused, &cs.Schedule, &cs.UpdatedAt); err != nil {
			return nil, err
		}
		states = append(states, cs)
	}
	return states, nil
}

func (db *DB) UpsertCronState(ctx context.Context, app, process string, paused bool, schedule string) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO cron_states (app, process, paused, schedule, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (app, process) DO UPDATE SET paused = $3, schedule = $4, updated_at = now()
	`, app, process, paused, schedule)
	return err
}

// FuncExecution represents a function invocation record.
type FuncExecution struct {
	ID         string     `json:"id"`
	App        string     `json:"app"`
	Process    string     `json:"process"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs *int64     `json:"durationMs,omitempty"`
}

func (db *DB) InsertFuncExecution(ctx context.Context, fe *FuncExecution) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO func_executions (id, app, process, status, started_at) VALUES ($1, $2, $3, $4, $5)`,
		fe.ID, fe.App, fe.Process, fe.Status, fe.StartedAt,
	)
	return err
}

func (db *DB) UpdateFuncExecution(ctx context.Context, id, status string, exitCode int, durationMs int64) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE func_executions SET status = $1, exit_code = $2, finished_at = now(), duration_ms = $3 WHERE id = $4`,
		status, exitCode, durationMs, id,
	)
	return err
}

func (db *DB) ListFuncExecutions(ctx context.Context, app string, limit int) ([]FuncExecution, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Pool.Query(ctx,
		`SELECT id, app, process, status, exit_code, started_at, finished_at, duration_ms
		 FROM func_executions WHERE app = $1 ORDER BY started_at DESC LIMIT $2`,
		app, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []FuncExecution
	for rows.Next() {
		var fe FuncExecution
		if err := rows.Scan(&fe.ID, &fe.App, &fe.Process, &fe.Status, &fe.ExitCode, &fe.StartedAt, &fe.FinishedAt, &fe.DurationMs); err != nil {
			return nil, err
		}
		execs = append(execs, fe)
	}
	return execs, nil
}
