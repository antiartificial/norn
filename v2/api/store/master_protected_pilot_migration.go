package store

// This migration reconciles protected Fleet and external deployment schema
// added on master after the V3 baseline forked. Existing migration checksums
// remain immutable. The raw GitHub dispatch nonce is removed after its hash
// and owner approval fields have been added.
const masterProtectedPilotMigrationSQL = `
    ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS principal_subject TEXT NOT NULL DEFAULT '';
    ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
    ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS root_attempt_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS source_dispatch_run_id BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS pilot_run_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS recovery BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE fleet_runner_attempts ADD COLUMN IF NOT EXISTS phase_started_at TIMESTAMPTZ;
		-- Legacy records predate phase timing. Their original attempt start is the
		-- only honest lower bound; new attempts set this server-owned field exactly.
		UPDATE fleet_runner_attempts SET phase_started_at = started_at WHERE phase_started_at IS NULL;
		ALTER TABLE fleet_runner_attempts ALTER COLUMN phase_started_at SET NOT NULL;
		ALTER TABLE fleet_runner_attempts ALTER COLUMN phase_started_at SET DEFAULT now();
		-- Existing durable histories predate root_attempt_id. Backfill every
		-- member of each plan lineage from its immutable first attempt.
		UPDATE fleet_runner_attempts target SET root_attempt_id = first_attempt.id
		FROM (SELECT DISTINCT ON (plan_id) plan_id, id FROM fleet_runner_attempts ORDER BY plan_id, attempt ASC) first_attempt
		WHERE target.plan_id = first_attempt.plan_id AND target.root_attempt_id = '';

		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS dispatch_state TEXT NOT NULL DEFAULT 'prepared';
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS submission_started_at TIMESTAMPTZ;
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS pilot_run_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS approval_envelope_sha256 TEXT NOT NULL DEFAULT '';
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS run_attempt INT NOT NULL DEFAULT 0;
		ALTER TABLE fleet_github_dispatches ADD COLUMN IF NOT EXISTS rerun_started_at TIMESTAMPTZ;
		ALTER TABLE fleet_github_dispatches DROP COLUMN IF EXISTS dispatch_nonce;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_fleet_github_dispatch_nonce ON fleet_github_dispatches(dispatch_nonce_sha256);

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
		-- A redacted, canonical claim envelope is stored before the remote claim.
		-- It has no raw nonce and lets the protected owner reconcile a crash
		-- without asking Actions to resubmit a one-use secret.
		ALTER TABLE external_deployment_admissions ADD COLUMN IF NOT EXISTS claimed_receipt JSONB NOT NULL DEFAULT '{}';
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

`

func masterProtectedPilotMigration() SchemaMigration {
	return SchemaMigration{
		Version:              44,
		Name:                 "master-protected-pilot-reconciliation",
		SQL:                  masterProtectedPilotMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion,
		MinimumWriterVersion: SnapshotExportIntentWriterVersion,
	}
}
