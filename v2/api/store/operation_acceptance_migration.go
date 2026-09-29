package store

const operationAcceptanceMigrationSQL = `
	CREATE TABLE control_plane_identity (
		singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
		authority UUID NOT NULL UNIQUE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	INSERT INTO control_plane_identity(singleton, authority)
	VALUES (true, gen_random_uuid())
	ON CONFLICT (singleton) DO NOTHING;

	ALTER TABLE operations
		ADD COLUMN acceptance_required BOOLEAN NOT NULL DEFAULT false;

	CREATE TABLE operation_request_identities (
		id TEXT PRIMARY KEY,
		authority UUID NOT NULL REFERENCES control_plane_identity(authority) ON DELETE RESTRICT,
		actor_issuer TEXT NOT NULL,
		actor_subject TEXT NOT NULL,
		kind TEXT NOT NULL,
		resource TEXT NOT NULL,
		request_key TEXT NOT NULL,
		fingerprint_version TEXT NOT NULL,
		fingerprint_digest TEXT NOT NULL CHECK (fingerprint_digest ~ '^[0-9a-f]{64}$'),
		operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
		created_at TIMESTAMPTZ NOT NULL,
		UNIQUE (authority, actor_issuer, actor_subject, kind, resource, request_key)
	);
	CREATE INDEX idx_operation_request_identity_operation
		ON operation_request_identities(operation_id);

	ALTER TABLE deployment_regions
		ADD COLUMN datacenters JSONB NOT NULL DEFAULT '[]';

	CREATE TABLE operation_acceptance_intents (
		id TEXT PRIMARY KEY,
		schema_version TEXT NOT NULL,
		request_identity_id TEXT NOT NULL UNIQUE REFERENCES operation_request_identities(id) ON DELETE RESTRICT,
		operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
		deployment_id TEXT REFERENCES deployments(id) ON DELETE RESTRICT,
		accepted_at TIMESTAMPTZ NOT NULL,
		request_receipt_id TEXT REFERENCES mutation_audit_events(id) ON DELETE RESTRICT,
		request_id TEXT NOT NULL DEFAULT '',
		credential_id TEXT NOT NULL DEFAULT '',
		device_id TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL,
		scopes JSONB NOT NULL DEFAULT '[]',
		fingerprint_version TEXT NOT NULL,
		fingerprint_digest TEXT NOT NULL CHECK (fingerprint_digest ~ '^[0-9a-f]{64}$'),
		request_canonical_bytes BYTEA NOT NULL,
		canonical_bytes BYTEA NOT NULL,
		canonical_digest TEXT NOT NULL CHECK (canonical_digest ~ '^[0-9a-f]{64}$'),
		signing_algorithm TEXT NOT NULL,
		signing_key_id TEXT NOT NULL,
		signature TEXT NOT NULL,
		CHECK (octet_length(canonical_bytes) > 0),
		CHECK (octet_length(request_canonical_bytes) > 0),
		CHECK (length(signature) > 0)
	);
	CREATE INDEX idx_operation_acceptance_receipt
		ON operation_acceptance_intents(request_receipt_id)
		WHERE request_receipt_id IS NOT NULL;
`

func operationAcceptanceMigration() SchemaMigration {
	return SchemaMigration{
		Version:              2,
		Name:                 "atomic-operation-acceptance",
		SQL:                  operationAcceptanceMigrationSQL,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 2,
	}
}
