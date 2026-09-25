package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
)

type DB struct {
	Pool *pgxpool.Pool
}

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
	DeclareReaderContract(config, "control")
	return config, nil
}

// readerApplicationPrefix is how a control-store session declares the saga
// history reader contract of the binary that opened it. Pruning refuses
// while any session of the control role declares less than
// EvidenceArchiveReaderVersion (or nothing: pre-archive binaries), because a
// startup schema floor cannot retire processes that are already running.
const readerApplicationPrefix = "norn/reader="

// DeclareReaderContract sets application_name to this binary's reader
// contract and component, overriding any value in the connection string.
func DeclareReaderContract(config *pgxpool.Config, component string) {
	config.ConnConfig.RuntimeParams["application_name"] = ReaderApplicationName(component)
}

// ReaderApplicationName is the application_name declaring this binary's
// reader contract, bounded to PostgreSQL's 63-byte limit.
func ReaderApplicationName(component string) string {
	name := fmt.Sprintf("%s%d/%s", readerApplicationPrefix, ControlSchemaReaderVersion, component)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func (db *DB) Close() {
	db.Pool.Close()
}

// controlSchemaBaselineSQL is immutable migration 1. It intentionally retains
// every legacy idempotent DDL statement and the historical fleet attempt
// lineage repair so an existing unversioned v2 database can be adopted without
// replacing IDs, receipts, or populated lineage.
const controlSchemaBaselineSQL = `
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
			lock_generation BIGINT NOT NULL DEFAULT 0,
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
		CREATE UNIQUE INDEX IF NOT EXISTS idx_operations_promotion_qualification
			ON operations ((metadata->'promotionQualification'->>'id'))
			WHERE kind = 'app.deploy' AND metadata->'promotionQualification'->>'id' <> '';

		ALTER TABLE operations ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS max_attempts INT NOT NULL DEFAULT 1;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS locked_by TEXT NOT NULL DEFAULT '';
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS lock_generation BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();
		ALTER TABLE operations ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_operations_queue ON operations(status, next_attempt_at, kind);

		CREATE TABLE IF NOT EXISTS fleet_runner_attempts (
			id TEXT PRIMARY KEY,
			plan_id TEXT NOT NULL,
			attempt INT NOT NULL,
			runner_attempt_id TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			plan_sha256 TEXT NOT NULL,
			workflow_url TEXT NOT NULL,
			status TEXT NOT NULL,
			current_phase TEXT NOT NULL,
			root_attempt_id TEXT NOT NULL DEFAULT '',
			retry_of TEXT NOT NULL DEFAULT '',
			heartbeat_sequence BIGINT NOT NULL DEFAULT 0,
			heartbeat_timeout_seconds INT NOT NULL,
			revision BIGINT NOT NULL DEFAULT 1,
			heartbeat_at TIMESTAMPTZ NOT NULL,
			heartbeat_expires_at TIMESTAMPTZ NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ
		);
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS root_attempt_id TEXT NOT NULL DEFAULT '';
		UPDATE fleet_runner_attempts target SET root_attempt_id = first_attempt.id
		FROM (SELECT DISTINCT ON (plan_id) plan_id, id FROM fleet_runner_attempts ORDER BY plan_id, attempt ASC) first_attempt
		WHERE target.plan_id = first_attempt.plan_id AND target.root_attempt_id = '';
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_runner_attempt_identity ON fleet_runner_attempts(plan_id, runner_attempt_id);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_runner_attempt_number ON fleet_runner_attempts(plan_id, attempt);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_runner_attempt_live_plan ON fleet_runner_attempts(plan_id) WHERE status IN ('queued', 'running');
		CREATE INDEX IF NOT EXISTS idx_fleet_runner_attempt_plan ON fleet_runner_attempts(plan_id, attempt DESC);
		CREATE INDEX IF NOT EXISTS idx_fleet_runner_attempt_expiry ON fleet_runner_attempts(status, heartbeat_expires_at);

		CREATE TABLE IF NOT EXISTS fleet_github_dispatches (
			plan_id TEXT PRIMARY KEY,
			plan_run_id BIGINT NOT NULL,
			plan_sha256 TEXT NOT NULL,
			approved_head_sha TEXT NOT NULL,
			fleet_environment TEXT NOT NULL,
			allow_destructive BOOLEAN NOT NULL,
			dispatch_nonce TEXT NOT NULL,
			dispatch_nonce_sha256 TEXT NOT NULL,
			run_id BIGINT NOT NULL DEFAULT 0,
			workflow_url TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_github_dispatch_nonce ON fleet_github_dispatches(dispatch_nonce_sha256);

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
		CREATE TABLE IF NOT EXISTS github_actions_assertion_uses (
			issuer     TEXT NOT NULL,
			jti        TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			used_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (issuer, jti)
		);
		CREATE INDEX IF NOT EXISTS idx_github_actions_assertion_uses_expiry ON github_actions_assertion_uses(expires_at);
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
		ALTER TABLE exec_sessions ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE exec_sessions ADD COLUMN IF NOT EXISTS owner_token TEXT NOT NULL DEFAULT '';
		ALTER TABLE exec_sessions ADD COLUMN IF NOT EXISTS owner_lease_until TIMESTAMPTZ;
		CREATE INDEX IF NOT EXISTS idx_exec_sessions_device ON exec_sessions(device_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_exec_sessions_status ON exec_sessions(status, expires_at);
		CREATE INDEX IF NOT EXISTS idx_exec_sessions_running_lease
			ON exec_sessions(owner_lease_until)
			WHERE status='running' AND owner_id<>'';

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
	`

// Reader contract 2 (EvidenceArchiveReaderVersion): saga history reads are
// archive-aware and never serve pruned history as complete.
const (
	EvidenceArchiveReaderVersion int64 = 2
	ControlSchemaReaderVersion   int64 = MySQLRetainedArtifactReaderVersion
	ControlSchemaWriterVersion   int64 = MySQLRestoreRecoveryWriterVersion
)

// ControlSchemaMigrations returns a copy of the ordered, forward-only control
// schema catalog. Never edit an applied definition; append a new version.
func ControlSchemaMigrations() []SchemaMigration {
	return []SchemaMigration{{
		Version:              1,
		Name:                 "legacy-control-schema-baseline",
		SQL:                  controlSchemaBaselineSQL,
		MinimumReaderVersion: 0,
		MinimumWriterVersion: 0,
	}, operationAcceptanceMigration(), operationEffectsMigration(), operationCheckpointsMigration(), databaseCatalogMigration(), evidenceArchiveMigration(), evidenceArchiveReaderMigration(), evidenceReserveMigration(), eventReplayRetentionMigration(), nonSagaEvidenceMigration(), desiredReplicasMigration(), regionalDesiredReplicasMigration(), restartEffectSourcesMigration(), signedAcceptanceByteReserveMigration(), operationReplayExpiryMigration(), snapshotPublicationMigration(), operationAcceptanceRetirementMigration(), privateInvocationMigration(), functionInvocationEffectAttemptsMigration(), functionInvocationCleanupMigration(), functionInvocationArchiveMigration(), functionDeploymentProvenanceMigration(), functionInvocationReaderContractMigration(), mysqlRestoreIntentMigration(), mysqlRestoreMaintenanceFenceMigration(), mysqlRuntimeLaunchReservationMigration(), mysqlRestoreRuntimeLockMigration(), runtimeMutationFenceMigration(), mysqlSourceSnapshotIntentMigration(), mysqlSourceSnapshotStopMigration(), mysqlSourceSnapshotAccountLockMigration(), mysqlSourceSnapshotArtifactMigration(), mysqlRestoreReceiptMigration(), mysqlRestoreFenceTransferMigration(), mysqlSourceSnapshotRetentionMigration(), mysqlRestoreRecoveryMigration()}
}

func NewControlSchemaMigrator(db *DB) (*SchemaMigrator, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("control schema database is unavailable")
	}
	migrator, err := NewSchemaMigrator(db.Pool, ControlSchemaMigrations(), BinarySchemaCompatibility{
		ReaderVersion: ControlSchemaReaderVersion,
		WriterVersion: ControlSchemaWriterVersion,
	}, SchemaMigratorOptions{})
	if err != nil {
		return nil, err
	}
	migrator.adoptUnversioned = adoptMiniControlSchema
	return migrator, nil
}

// Migrate retains the compatibility entry point for existing tests and tools
// while delegating all schema ownership and history validation to the
// versioned runner.
func Migrate(db *DB) error {
	migrator, err := NewControlSchemaMigrator(db)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(context.Background())
	return err
}

func (db *DB) InsertDeployment(ctx context.Context, d *model.Deployment) error {
	changes, _ := json.Marshal(d.SourceChanges)
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO deployments (id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		d.ID, d.App, d.CommitSHA, d.ImageTag, d.SpecDigest, d.Environment, d.SagaID, d.Status, d.SourceKind, d.SourceRef, d.SourceDirty, changes, d.StartedAt,
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
		 SET status = $1, commit_sha = $2, image_tag = $3, environment = $4, source_kind = $5, source_ref = $6, source_dirty = $7, source_changes = $8, finished_at = $9, spec_digest = $10
		 WHERE id = $11`,
		d.Status, d.CommitSHA, d.ImageTag, d.Environment, d.SourceKind, d.SourceRef, d.SourceDirty, changes, finished, d.SpecDigest, d.ID,
	)
	return err
}

// FunctionDeploymentBinding returns the active environment's last successful
// image and the spec digest recorded with that deployment's result. Failed
// attempts do not displace it. Newer active attempts and unproven rollbacks
// fail closed because Nomad may already be running a different image.
func (db *DB) FunctionDeploymentBinding(ctx context.Context, app, environment string) (image, specDigest string, err error) {
	err = db.Pool.QueryRow(ctx, `SELECT d.image_tag, d.spec_digest FROM deployments d
		WHERE d.app=$1 AND d.environment=$2 AND d.status='deployed'
		AND NOT EXISTS (
			SELECT 1 FROM deployments newer WHERE newer.app=d.app AND newer.environment=d.environment
			AND (newer.started_at, newer.id) > (d.started_at, d.id)
			AND newer.status NOT IN ('deployed','failed')
		)
		ORDER BY d.started_at DESC, d.id DESC LIMIT 1`, app, environment).Scan(&image, &specDigest)
	if err != nil {
		return "", "", err
	}
	if image == "" || specDigest == "" {
		return "", "", fmt.Errorf("function deployment binding unavailable")
	}
	return image, specDigest, nil
}

func (db *DB) ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments`
	args := []interface{}{}
	if app != "" {
		query += " WHERE app = $1 ORDER BY started_at DESC LIMIT $2"
		args = append(args, app, limit)
	} else {
		query += " ORDER BY started_at DESC LIMIT $1"
		args = append(args, limit)
	}

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	var deployments []model.Deployment
	for rows.Next() {
		var d model.Deployment
		var changes []byte
		if err := rows.Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(changes, &d.SourceChanges)
		deployments = append(deployments, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Release the row cursor's connection before fetching regions. Concurrent
	// readers can otherwise occupy every pool connection with open deployment
	// cursors and deadlock while each waits for a nested region query.
	rows.Close()
	for i := range deployments {
		deployments[i].Regions, _ = db.DeploymentRegions(ctx, deployments[i].ID)
	}
	return deployments, nil
}

func (db *DB) GetDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx,
		`SELECT id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments
		 WHERE id = $1`,
		id,
	).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

func (db *DB) LastSuccessfulDeployment(ctx context.Context, app, environment, excludeID string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx,
		`SELECT id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments
		 WHERE app = $1 AND environment = $2 AND status = 'deployed' AND id != $3
		 ORDER BY started_at DESC LIMIT 1`,
		app, environment, excludeID,
	).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

// LatestSuccessfulDeployment is the authoritative active deployment for a
// control-plane environment. Failed or queued rows must never displace it.
func (db *DB) LatestSuccessfulDeployment(ctx context.Context, app, environment string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx, `SELECT id, app, commit_sha, image_tag, spec_digest, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at
		 FROM deployments WHERE app=$1 AND environment=$2 AND status='deployed' ORDER BY started_at DESC LIMIT 1`, app, environment).
		Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
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

// FinishCronPauseClaimedOperation atomically makes a verified pause visible
// with its terminal receipt. The operation lease is checked in the same
// transaction so a superseded worker cannot publish stale cron state.
func (db *DB) FinishCronPauseClaimedOperation(ctx context.Context, claim OperationClaim, app, process, schedule, message string, metadata map[string]interface{}) error {
	return db.finishCronClaimedOperation(ctx, claim, app, process, schedule, true, message, metadata)
}

// FinishCronResumeClaimedOperation commits the verified Nomad resume and the
// durable unpaused state under one operation claim fence.
func (db *DB) FinishCronResumeClaimedOperation(ctx context.Context, claim OperationClaim, app, process, schedule, message string, metadata map[string]interface{}) error {
	return db.finishCronClaimedOperation(ctx, claim, app, process, schedule, false, message, metadata)
}

// FinishCronScheduleClaimedOperation commits a verified periodic replacement,
// its effective schedule, and its terminal evidence intent in one claim-fenced
// transaction. The previous state remains intact until this point.
func (db *DB) FinishCronScheduleClaimedOperation(ctx context.Context, claim OperationClaim, app, process string, paused bool, schedule, message string, metadata map[string]interface{}) error {
	return db.finishCronClaimedOperation(ctx, claim, app, process, schedule, paused, message, metadata)
}

func (db *DB) finishCronClaimedOperation(ctx context.Context, claim OperationClaim, app, process, schedule string, paused bool, message string, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if app == "" || process == "" || schedule == "" {
		return fmt.Errorf("cron pause intent is invalid")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, owner string
	var generation int64
	var lockedUntil *time.Time
	if err = tx.QueryRow(ctx, `SELECT status, locked_by, lock_generation, locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).Scan(&status, &owner, &generation, &lockedUntil); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if status != "running" || owner != claim.OwnerID() || generation != claim.Generation() || lockedUntil == nil || !lockedUntil.After(now) {
		return ownershipLost(claim)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cron_states (app, process, paused, schedule, updated_at) VALUES ($1,$2,$3,$4,now()) ON CONFLICT (app,process) DO UPDATE SET paused=EXCLUDED.paused,schedule=EXCLUDED.schedule,updated_at=now()`, app, process, paused, schedule); err != nil {
		return err
	}
	var sagaID, operationApp string
	if err = tx.QueryRow(ctx, `UPDATE operations SET status='succeeded', message=$1, metadata=metadata || $2::jsonb, locked_by='', locked_until=NULL, updated_at=now(), finished_at=now() WHERE id=$3 RETURNING saga_id,app`, message, data, claim.OperationID()).Scan(&sagaID, &operationApp); err != nil {
		return err
	}
	if sagaID != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state) VALUES ('ei-' || gen_random_uuid()::text,'saga',$1,$2,$3,1,'pending') ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING`, sagaID, operationApp, claim.OperationID()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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
