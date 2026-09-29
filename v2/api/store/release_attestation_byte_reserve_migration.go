package store

// Migration 45 records logical payload and metadata bytes for private release
// attestations. Existing records are backfilled before a limit can be set.
// Heap, index, TOAST and WAL overhead remain outside this counter.
const releaseAttestationByteReserveMigrationSQL = `
	ALTER TABLE evidence_reserve
		ADD COLUMN max_release_attestation_bytes BIGINT NOT NULL DEFAULT 0 CHECK (max_release_attestation_bytes >= 0);
	CREATE TABLE release_attestation_byte_reservations (
		operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
		reserved_bytes BIGINT NOT NULL CHECK (reserved_bytes > 0),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	INSERT INTO release_attestation_byte_reservations (operation_id, reserved_bytes, created_at)
	SELECT id, GREATEST(1, octet_length(payload::text) + octet_length(metadata::text)), started_at
	FROM operations WHERE kind = 'release.attestation';
	CREATE FUNCTION reserve_release_attestation_bytes() RETURNS trigger LANGUAGE plpgsql AS $$
	DECLARE
		reserve_enabled BOOLEAN;
		byte_limit BIGINT;
		reserved BIGINT;
		new_bytes BIGINT;
	BEGIN
		IF NEW.kind <> 'release.attestation' THEN
			RETURN NEW;
		END IF;
		SELECT enabled, max_release_attestation_bytes INTO reserve_enabled, byte_limit
		FROM evidence_reserve WHERE singleton FOR UPDATE;
		IF NOT FOUND THEN
			RAISE EXCEPTION 'evidence reserve state is missing';
		END IF;
		new_bytes := GREATEST(1, octet_length(NEW.payload::text) + octet_length(NEW.metadata::text));
		IF reserve_enabled AND byte_limit > 0 THEN
			SELECT coalesce(sum(reserved_bytes), 0) INTO reserved FROM release_attestation_byte_reservations;
			IF new_bytes > byte_limit OR reserved > byte_limit - new_bytes THEN
				RAISE EXCEPTION USING ERRCODE = 'P0045', MESSAGE = 'release attestation byte reserve exhausted';
			END IF;
		END IF;
		INSERT INTO release_attestation_byte_reservations (operation_id, reserved_bytes)
		VALUES (NEW.id, new_bytes);
		RETURN NEW;
	END
	$$;
	CREATE TRIGGER reserve_release_attestation_bytes_after_insert
		AFTER INSERT ON operations FOR EACH ROW EXECUTE FUNCTION reserve_release_attestation_bytes();
`

func releaseAttestationByteReserveMigration() SchemaMigration {
	return SchemaMigration{Version: 45, Name: "release-attestation-byte-reserve", SQL: releaseAttestationByteReserveMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}
