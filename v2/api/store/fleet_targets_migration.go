package store

// FleetTargetFenceWriterVersion raises the control schema's writer floor
// once the fence tables exist (ADR 0009, H1). Any writer below this version
// — including Mini — can no longer write to the control database once
// migration 48 is applied, whether or not a Fleet target is ever
// registered: refusing writers at registration time instead would need
// writer tracking that does not exist today (H1's rejected alternative).
// etcd has no equivalent mechanism (H2); its runbook must stop every
// pre-fence Fleet runtime before the first target registration.
const FleetTargetFenceWriterVersion int64 = 32

const fleetTargetsMigrationSQL = `
	-- The registry singleton's generation is read FOR SHARE by every
	-- fence-relevant admission and FOR UPDATE only by registration (plan.md
	-- §2.2's lock order, position 2). Generation 0 means "no target has ever
	-- been registered": every admission then behaves exactly as before this
	-- change (Q1).
	CREATE TABLE fleet_target_registry (
		singleton  BOOLEAN PRIMARY KEY CHECK (singleton),
		generation BIGINT NOT NULL DEFAULT 0 CHECK (generation >= 0)
	);
	INSERT INTO fleet_target_registry (singleton, generation) VALUES (true, 0)
	ON CONFLICT (singleton) DO NOTHING;

	CREATE TABLE fleet_targets (
		target_id                 TEXT PRIMARY KEY CHECK (target_id ~ '^tgt_[0-9a-f]{64}$'),
		provider                  TEXT NOT NULL CHECK (provider <> ''),
		provider_account          TEXT NOT NULL CHECK (provider_account <> ''),
		state_backend             TEXT NOT NULL CHECK (state_backend <> ''),
		created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
		registration_operation_id TEXT NOT NULL CHECK (registration_operation_id <> '')
	);

	-- Aliases are immutable in this pass: once a row exists, no path may
	-- repoint it to a different target_id (plan.md §2.2).
	CREATE TABLE fleet_target_aliases (
		alias     TEXT PRIMARY KEY CHECK (alias ~ '^(cluster|environment):.+$'),
		target_id TEXT NOT NULL REFERENCES fleet_targets(target_id) ON DELETE RESTRICT
	);
	CREATE INDEX idx_fleet_target_aliases_target ON fleet_target_aliases(target_id);

	-- The fence is a lock, not a ledger (m18): it never stores an attempt ID.
	-- Holder attempts are always re-derived from fleet_runner_attempts (PG)
	-- or the etcd attempt keys for holder_plan_id. Position 4 in the lock
	-- order: FOR UPDATE for acquire/bind/release/abandon, FOR SHARE otherwise.
	-- Registration inserts the (free) row in the same transaction as the
	-- target, so acquire always has an existing row to lock FOR UPDATE and
	-- never races an INSERT.
	CREATE TABLE fleet_target_fences (
		target_id            TEXT PRIMARY KEY REFERENCES fleet_targets(target_id) ON DELETE RESTRICT,
		generation           BIGINT NOT NULL DEFAULT 0 CHECK (generation >= 0),
		held                 BOOLEAN NOT NULL DEFAULT false,
		holder_plan_id       TEXT NOT NULL DEFAULT '',
		holder_nonce_sha256  TEXT NOT NULL DEFAULT '',
		authority_epoch      BIGINT NOT NULL DEFAULT 0 CHECK (authority_epoch >= 0),
		last_release_plan_id TEXT NOT NULL DEFAULT '',
		last_release_reason  TEXT NOT NULL DEFAULT '' CHECK (last_release_reason IN ('', 'succeeded', 'dispatch_not_submitted', 'released_terminal', 'abandoned')),
		last_release_at      TIMESTAMPTZ,
		revision             BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
		CHECK ((held AND holder_plan_id <> '' AND holder_nonce_sha256 <> '' AND authority_epoch > 0) OR
		       (NOT held AND holder_plan_id = '' AND holder_nonce_sha256 = '')),
		CHECK ((last_release_reason = '') = (last_release_at IS NULL) AND (last_release_reason = '') = (last_release_plan_id = ''))
	);

	-- A permanent record of break-glass abandonment (B1): once a plan and
	-- nonce appear here, every execution path refuses them forever
	-- (fleet_target_holder_abandoned), in the same transaction as the write
	-- that checks it. target_id is NULL for a plan abandoned while its
	-- cluster was unregistered (H7); such a row must record that cluster
	-- name instead, so the record never points at a fabricated target.
	CREATE TABLE fleet_target_abandoned_plans (
		plan_id      TEXT PRIMARY KEY,
		nonce_sha256 TEXT NOT NULL CHECK (nonce_sha256 <> ''),
		target_id    TEXT REFERENCES fleet_targets(target_id) ON DELETE RESTRICT,
		cluster      TEXT NOT NULL DEFAULT '',
		operation_id TEXT NOT NULL,
		abandoned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CHECK (target_id IS NOT NULL OR cluster <> '')
	);

	-- Singleton authority epoch (position 3 in the lock order). Advancing it
	-- touches no fence or attempt history (plan.md §2.2): old-epoch fences
	-- simply become Uncertain/AuthoritySuperseded until re-bind or release.
	CREATE TABLE fleet_authority_epoch (
		singleton    BOOLEAN PRIMARY KEY CHECK (singleton),
		epoch        BIGINT NOT NULL CHECK (epoch > 0),
		activated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		reason       TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO fleet_authority_epoch (singleton, epoch, activated_at, reason) VALUES (true, 1, now(), 'initial')
	ON CONFLICT (singleton) DO NOTHING;
`

func fleetTargetsMigration() SchemaMigration {
	return SchemaMigration{Version: 48, Name: "fleet-targets-and-authority-epoch", SQL: fleetTargetsMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: FleetTargetFenceWriterVersion}
}
