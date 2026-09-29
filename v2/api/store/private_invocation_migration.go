package store

const PrivateInvocationWriterVersion int64 = 15

const privateInvocationMigrationSQL = `
CREATE TABLE private_invocation_material (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 key_id TEXT NOT NULL CHECK (key_id <> ''),
 ciphertext_digest TEXT NOT NULL CHECK (ciphertext_digest ~ '^sha256:[0-9a-f]{64}$'),
 envelope BYTEA NOT NULL CHECK (octet_length(envelope) > 0 AND octet_length(envelope) <= 131072),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX private_invocation_material_key_id ON private_invocation_material(key_id);
`

func privateInvocationMigration() SchemaMigration {
	return SchemaMigration{Version: 18, Name: "private-function-invocation-material", SQL: privateInvocationMigrationSQL, MinimumReaderVersion: OperationAcceptanceRetirementReaderVersion, MinimumWriterVersion: PrivateInvocationWriterVersion}
}
