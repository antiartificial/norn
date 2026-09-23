package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	schemaMetadataFormatVersion          = 1
	defaultSchemaMigrationLockKey  int64 = 0x4e4f524e5f53334d // "NORN_S3M"
	defaultSchemaMigrationLockWait       = 10 * time.Second
)

const schemaMetadataDDL = `
CREATE TABLE IF NOT EXISTS norn_schema_migrations (
	version BIGINT PRIMARY KEY CHECK (version > 0),
	name TEXT NOT NULL CHECK (name <> ''),
	checksum CHAR(64) NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
	minimum_reader_version BIGINT NOT NULL CHECK (minimum_reader_version >= 0),
	minimum_writer_version BIGINT NOT NULL CHECK (minimum_writer_version >= 0),
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS norn_schema_compatibility (
	singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
	format_version INTEGER NOT NULL,
	current_migration_version BIGINT NOT NULL CHECK (current_migration_version > 0),
	minimum_reader_version BIGINT NOT NULL CHECK (minimum_reader_version >= 0),
	minimum_writer_version BIGINT NOT NULL CHECK (minimum_writer_version >= 0),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// SchemaMigration is one ordered, forward-only schema change. SQL must be
// idempotent when the first migration adopts an existing unversioned schema.
// The compatibility versions are independent from both Version and any Norn
// product version.
type SchemaMigration struct {
	Version              int64
	Name                 string
	SQL                  string
	MinimumReaderVersion int64
	MinimumWriterVersion int64
}

// BinarySchemaCompatibility declares the schema contract understood by a
// binary. A read-only process needs only ReaderVersion; a writer needs both.
type BinarySchemaCompatibility struct {
	ReaderVersion int64
	WriterVersion int64
}

// SchemaMigratorOptions controls serialization. Zero values select stable,
// bounded defaults. LockKey should normally be left at zero so every Norn
// process contends for the same database-scoped migration owner.
type SchemaMigratorOptions struct {
	LockKey     int64
	LockTimeout time.Duration
}

// SchemaAccess selects the compatibility promises a read-only check enforces.
type SchemaAccess uint8

const (
	SchemaAccessReadOnly SchemaAccess = iota + 1
	SchemaAccessReadWrite
)

// SchemaStatus is safe to expose through passive status/diagnostic surfaces.
// AppliedVersions reports only migrations applied by the current Migrate call.
type SchemaStatus struct {
	CurrentMigrationVersion int64   `json:"currentMigrationVersion"`
	MinimumReaderVersion    int64   `json:"minimumReaderVersion"`
	MinimumWriterVersion    int64   `json:"minimumWriterVersion"`
	AppliedVersions         []int64 `json:"appliedVersions,omitempty"`
}

type SchemaMetadataErrorKind string

const (
	SchemaMetadataAbsent  SchemaMetadataErrorKind = "absent"
	SchemaMetadataCorrupt SchemaMetadataErrorKind = "corrupt"
)

// MigrationDefinitionError reports an invalid in-process migration catalog.
type MigrationDefinitionError struct {
	Version int64
	Reason  string
}

func (e *MigrationDefinitionError) Error() string {
	if e.Version > 0 {
		return fmt.Sprintf("schema migration %d definition is invalid: %s", e.Version, e.Reason)
	}
	return "schema migration definition is invalid: " + e.Reason
}

// SchemaMetadataError reports missing or structurally invalid ledger metadata.
type SchemaMetadataError struct {
	Kind   SchemaMetadataErrorKind
	Reason string
	Err    error
}

func (e *SchemaMetadataError) Error() string {
	message := fmt.Sprintf("schema metadata is %s", e.Kind)
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

func (e *SchemaMetadataError) Unwrap() error { return e.Err }

// MigrationChecksumError means an applied, known migration was changed after
// it was recorded. The runner never overwrites the stored checksum.
type MigrationChecksumError struct {
	Version  int64
	Expected string
	Actual   string
}

func (e *MigrationChecksumError) Error() string {
	return fmt.Sprintf("schema migration %d checksum mismatch: expected %s, found %s", e.Version, e.Expected, e.Actual)
}

// MigrationHistoryError reports a gap, invalid ordering, or inconsistent
// compatibility state in the durable ledger.
type MigrationHistoryError struct {
	Version int64
	Reason  string
}

func (e *MigrationHistoryError) Error() string {
	if e.Version > 0 {
		return fmt.Sprintf("schema migration history is invalid at version %d: %s", e.Version, e.Reason)
	}
	return "schema migration history is invalid: " + e.Reason
}

// SchemaMigrationRequiredError is returned by Check when this binary knows a
// migration which has not yet been applied. Check never applies it.
type SchemaMigrationRequiredError struct {
	Current  int64
	Required int64
}

func (e *SchemaMigrationRequiredError) Error() string {
	return fmt.Sprintf("schema migration required: database is at %d, binary requires at least %d", e.Current, e.Required)
}

// SchemaCompatibilityError reports that the database contract has retired the
// supplied binary reader or writer contract.
type SchemaCompatibilityError struct {
	Access   SchemaAccess
	Contract string
	Provided int64
	Required int64
}

func (e *SchemaCompatibilityError) Error() string {
	return fmt.Sprintf("schema %s compatibility version %d is below required version %d", e.Contract, e.Provided, e.Required)
}

// MigrationLockTimeoutError reports failure to become the single migration
// owner within the configured bounded wait.
type MigrationLockTimeoutError struct {
	Waited time.Duration
	Err    error
}

func (e *MigrationLockTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s waiting for schema migration ownership", e.Waited)
}

func (e *MigrationLockTimeoutError) Unwrap() error { return e.Err }

// MigrationApplyError identifies the migration and phase whose transaction
// failed. DDL and its ledger record are rolled back together.
type MigrationApplyError struct {
	Version int64
	Name    string
	Phase   string
	Err     error
}

func (e *MigrationApplyError) Error() string {
	if e.Version > 0 {
		return fmt.Sprintf("schema migration %d (%s) failed during %s: %v", e.Version, e.Name, e.Phase, e.Err)
	}
	return fmt.Sprintf("schema migration failed during %s: %v", e.Phase, e.Err)
}

func (e *MigrationApplyError) Unwrap() error { return e.Err }

// SchemaMigrator owns an immutable migration catalog for one PostgreSQL pool.
type SchemaMigrator struct {
	pool          *pgxpool.Pool
	migrations    []SchemaMigration
	compatibility BinarySchemaCompatibility
	lockKey       int64
	lockTimeout   time.Duration
}

// NewSchemaMigrator validates and copies definitions before any database work.
func NewSchemaMigrator(pool *pgxpool.Pool, migrations []SchemaMigration, compatibility BinarySchemaCompatibility, options SchemaMigratorOptions) (*SchemaMigrator, error) {
	if pool == nil {
		return nil, &MigrationDefinitionError{Reason: "PostgreSQL pool is nil"}
	}
	if err := validateMigrationDefinitions(migrations); err != nil {
		return nil, err
	}
	if compatibility.ReaderVersion < 0 || compatibility.WriterVersion < 0 {
		return nil, &MigrationDefinitionError{Reason: "binary compatibility versions cannot be negative"}
	}
	if options.LockTimeout < 0 {
		return nil, &MigrationDefinitionError{Reason: "migration lock timeout cannot be negative"}
	}
	if options.LockKey == 0 {
		options.LockKey = defaultSchemaMigrationLockKey
	}
	if options.LockTimeout == 0 {
		options.LockTimeout = defaultSchemaMigrationLockWait
	}
	catalog := append([]SchemaMigration(nil), migrations...)
	return &SchemaMigrator{
		pool:          pool,
		migrations:    catalog,
		compatibility: compatibility,
		lockKey:       options.LockKey,
		lockTimeout:   options.LockTimeout,
	}, nil
}

// MigrationChecksum returns the durable checksum for a definition. It covers
// SQL, identity, order, and compatibility promises rather than SQL alone.
func MigrationChecksum(migration SchemaMigration) string {
	hash := sha256.New()
	hash.Write([]byte("norn-schema-migration-definition-v1\x00"))
	writeChecksumInt64(hash, migration.Version)
	writeChecksumString(hash, migration.Name)
	writeChecksumString(hash, migration.SQL)
	writeChecksumInt64(hash, migration.MinimumReaderVersion)
	writeChecksumInt64(hash, migration.MinimumWriterVersion)
	return hex.EncodeToString(hash.Sum(nil))
}

// Check reads and verifies metadata without creating tables, taking locks, or
// issuing any mutation. Unknown newer migrations are accepted only when their
// ledger is contiguous and their explicit compatibility contract admits this
// binary; known migration checksums are always verified.
func (m *SchemaMigrator) Check(ctx context.Context, access SchemaAccess) (SchemaStatus, error) {
	if err := validateSchemaAccess(access); err != nil {
		return SchemaStatus{}, err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot begin read-only metadata check", Err: err}
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	present, err := schemaMetadataPresence(ctx, tx)
	if err != nil {
		return SchemaStatus{}, err
	}
	if !present {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataAbsent, Reason: "migration ledger and compatibility row do not exist"}
	}
	status, err := loadAndValidateSchemaMetadata(ctx, tx, m.migrations)
	if err != nil {
		return SchemaStatus{}, err
	}
	if err := m.checkCurrent(status, access); err != nil {
		return SchemaStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot finish read-only metadata check", Err: err}
	}
	return status, nil
}

// Migrate serializes all pending forward migrations with a transaction-scoped
// advisory lock. Metadata setup, supplied DDL, ledger inserts, and compatibility
// state commit atomically in that same transaction. It never runs a down path.
func (m *SchemaMigrator) Migrate(ctx context.Context) (SchemaStatus, error) {
	target := m.migrations[len(m.migrations)-1]
	targetStatus := SchemaStatus{
		CurrentMigrationVersion: target.Version,
		MinimumReaderVersion:    target.MinimumReaderVersion,
		MinimumWriterVersion:    target.MinimumWriterVersion,
	}
	if err := m.checkCompatibility(targetStatus, SchemaAccessReadWrite); err != nil {
		return SchemaStatus{}, err
	}

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SchemaStatus{}, &MigrationApplyError{Phase: "begin", Err: err}
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	lockCtx, cancel := context.WithTimeout(ctx, m.lockTimeout)
	_, lockErr := tx.Exec(lockCtx, `SELECT pg_advisory_xact_lock($1)`, m.lockKey)
	lockContextErr := lockCtx.Err()
	cancel()
	if lockErr != nil {
		if errors.Is(lockContextErr, context.DeadlineExceeded) || errors.Is(lockErr, context.DeadlineExceeded) {
			return SchemaStatus{}, &MigrationLockTimeoutError{Waited: m.lockTimeout, Err: lockErr}
		}
		return SchemaStatus{}, &MigrationApplyError{Phase: "migration ownership", Err: lockErr}
	}

	hasMetadata, err := schemaMetadataPresence(ctx, tx)
	if err != nil {
		return SchemaStatus{}, err
	}
	var status SchemaStatus
	if hasMetadata {
		status, err = loadAndValidateSchemaMetadata(ctx, tx, m.migrations)
		if err != nil {
			return SchemaStatus{}, err
		}
	} else if _, err := tx.Exec(ctx, schemaMetadataDDL); err != nil {
		return SchemaStatus{}, &MigrationApplyError{Phase: "metadata setup", Err: err}
	}
	if hasMetadata && status.CurrentMigrationVersion >= target.Version {
		if err := m.checkCurrent(status, SchemaAccessReadWrite); err != nil {
			return SchemaStatus{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SchemaStatus{}, &MigrationApplyError{Phase: "commit", Err: err}
		}
		return status, nil
	}

	current := status.CurrentMigrationVersion
	applied := make([]int64, 0, int(target.Version-current))
	for _, migration := range m.migrations {
		if migration.Version <= current {
			continue
		}
		if _, err := tx.Exec(ctx, migration.SQL); err != nil {
			return SchemaStatus{}, &MigrationApplyError{Version: migration.Version, Name: migration.Name, Phase: "DDL", Err: err}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO norn_schema_migrations
				(version, name, checksum, minimum_reader_version, minimum_writer_version)
			VALUES ($1, $2, $3, $4, $5)`,
			migration.Version, migration.Name, MigrationChecksum(migration),
			migration.MinimumReaderVersion, migration.MinimumWriterVersion,
		); err != nil {
			return SchemaStatus{}, &MigrationApplyError{Version: migration.Version, Name: migration.Name, Phase: "ledger insert", Err: err}
		}
		status = SchemaStatus{
			CurrentMigrationVersion: migration.Version,
			MinimumReaderVersion:    migration.MinimumReaderVersion,
			MinimumWriterVersion:    migration.MinimumWriterVersion,
		}
		applied = append(applied, migration.Version)
	}

	if status.CurrentMigrationVersion == 0 {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "migration transaction produced an empty ledger"}
	}
	if hasMetadata {
		command, err := tx.Exec(ctx, `
			UPDATE norn_schema_compatibility
			SET format_version=$1, current_migration_version=$2,
				minimum_reader_version=$3, minimum_writer_version=$4, updated_at=now()
			WHERE singleton=TRUE`,
			schemaMetadataFormatVersion, status.CurrentMigrationVersion,
			status.MinimumReaderVersion, status.MinimumWriterVersion,
		)
		if err != nil {
			return SchemaStatus{}, &MigrationApplyError{Phase: "compatibility metadata update", Err: err}
		}
		if command.RowsAffected() != 1 {
			return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "compatibility metadata singleton is missing"}
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO norn_schema_compatibility
				(singleton, format_version, current_migration_version, minimum_reader_version, minimum_writer_version)
			VALUES (TRUE, $1, $2, $3, $4)`,
			schemaMetadataFormatVersion, status.CurrentMigrationVersion,
			status.MinimumReaderVersion, status.MinimumWriterVersion,
		); err != nil {
			return SchemaStatus{}, &MigrationApplyError{Phase: "compatibility metadata insert", Err: err}
		}
	}
	if err := m.checkCompatibility(status, SchemaAccessReadWrite); err != nil {
		return SchemaStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SchemaStatus{}, &MigrationApplyError{Phase: "commit", Err: err}
	}
	status.AppliedVersions = applied
	return status, nil
}

func validateMigrationDefinitions(migrations []SchemaMigration) error {
	if len(migrations) == 0 {
		return &MigrationDefinitionError{Reason: "at least one forward migration is required"}
	}
	seenNames := make(map[string]int64, len(migrations))
	var previousReader, previousWriter int64
	for index, migration := range migrations {
		expectedVersion := int64(index + 1)
		if migration.Version != expectedVersion {
			return &MigrationDefinitionError{Version: migration.Version, Reason: fmt.Sprintf("definitions must be ordered and contiguous; position %d requires version %d", index, expectedVersion)}
		}
		name := strings.TrimSpace(migration.Name)
		if name == "" {
			return &MigrationDefinitionError{Version: migration.Version, Reason: "name is empty"}
		}
		if earlier, duplicate := seenNames[name]; duplicate {
			return &MigrationDefinitionError{Version: migration.Version, Reason: fmt.Sprintf("name %q duplicates version %d", name, earlier)}
		}
		seenNames[name] = migration.Version
		if strings.TrimSpace(migration.SQL) == "" {
			return &MigrationDefinitionError{Version: migration.Version, Reason: "SQL is empty"}
		}
		if migration.MinimumReaderVersion < 0 || migration.MinimumWriterVersion < 0 {
			return &MigrationDefinitionError{Version: migration.Version, Reason: "minimum compatibility versions cannot be negative"}
		}
		if index > 0 && migration.MinimumReaderVersion < previousReader {
			return &MigrationDefinitionError{Version: migration.Version, Reason: "minimum reader version cannot decrease"}
		}
		if index > 0 && migration.MinimumWriterVersion < previousWriter {
			return &MigrationDefinitionError{Version: migration.Version, Reason: "minimum writer version cannot decrease"}
		}
		previousReader = migration.MinimumReaderVersion
		previousWriter = migration.MinimumWriterVersion
	}
	return nil
}

func validateSchemaAccess(access SchemaAccess) error {
	if access != SchemaAccessReadOnly && access != SchemaAccessReadWrite {
		return &MigrationDefinitionError{Reason: fmt.Sprintf("unknown schema access mode %d", access)}
	}
	return nil
}

func (m *SchemaMigrator) checkCurrent(status SchemaStatus, access SchemaAccess) error {
	requiredMigration := m.migrations[len(m.migrations)-1].Version
	if status.CurrentMigrationVersion < requiredMigration {
		return &SchemaMigrationRequiredError{Current: status.CurrentMigrationVersion, Required: requiredMigration}
	}
	return m.checkCompatibility(status, access)
}

func (m *SchemaMigrator) checkCompatibility(status SchemaStatus, access SchemaAccess) error {
	if m.compatibility.ReaderVersion < status.MinimumReaderVersion {
		return &SchemaCompatibilityError{
			Access: access, Contract: "reader", Provided: m.compatibility.ReaderVersion, Required: status.MinimumReaderVersion,
		}
	}
	if access == SchemaAccessReadWrite && m.compatibility.WriterVersion < status.MinimumWriterVersion {
		return &SchemaCompatibilityError{
			Access: access, Contract: "writer", Provided: m.compatibility.WriterVersion, Required: status.MinimumWriterVersion,
		}
	}
	return nil
}

type schemaLedgerRow struct {
	version       int64
	name          string
	checksum      string
	minimumReader int64
	minimumWriter int64
}

type schemaQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func schemaMetadataPresence(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var ledgerPresent, compatibilityPresent bool
	if err := queryer.QueryRow(ctx, `
		SELECT to_regclass('norn_schema_migrations') IS NOT NULL,
		       to_regclass('norn_schema_compatibility') IS NOT NULL`).Scan(&ledgerPresent, &compatibilityPresent); err != nil {
		return false, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot inspect metadata tables", Err: err}
	}
	if ledgerPresent != compatibilityPresent {
		return false, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "migration ledger and compatibility table are not both present"}
	}
	return ledgerPresent, nil
}

func loadAndValidateSchemaMetadata(ctx context.Context, queryer schemaQueryer, definitions []SchemaMigration) (SchemaStatus, error) {
	rows, err := queryer.Query(ctx, `
		SELECT version, name, checksum, minimum_reader_version, minimum_writer_version
		FROM norn_schema_migrations ORDER BY version`)
	if err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot read migration ledger", Err: err}
	}
	defer rows.Close()
	ledger := make([]schemaLedgerRow, 0, len(definitions))
	for rows.Next() {
		var item schemaLedgerRow
		if err := rows.Scan(&item.version, &item.name, &item.checksum, &item.minimumReader, &item.minimumWriter); err != nil {
			return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot decode migration ledger", Err: err}
		}
		ledger = append(ledger, item)
	}
	if err := rows.Err(); err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot finish reading migration ledger", Err: err}
	}

	stateRows, err := queryer.Query(ctx, `
		SELECT singleton, format_version, current_migration_version,
		       minimum_reader_version, minimum_writer_version
		FROM norn_schema_compatibility`)
	if err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot read compatibility metadata", Err: err}
	}
	defer stateRows.Close()
	type stateRow struct {
		singleton     bool
		formatVersion int
		current       int64
		minimumReader int64
		minimumWriter int64
	}
	states := make([]stateRow, 0, 1)
	for stateRows.Next() {
		var state stateRow
		if err := stateRows.Scan(&state.singleton, &state.formatVersion, &state.current, &state.minimumReader, &state.minimumWriter); err != nil {
			return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot decode compatibility metadata", Err: err}
		}
		states = append(states, state)
	}
	if err := stateRows.Err(); err != nil {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "cannot finish reading compatibility metadata", Err: err}
	}
	if len(ledger) == 0 || len(states) != 1 {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: "metadata must contain a non-empty ledger and exactly one compatibility row"}
	}
	state := states[0]
	if !state.singleton || state.formatVersion != schemaMetadataFormatVersion {
		return SchemaStatus{}, &SchemaMetadataError{Kind: SchemaMetadataCorrupt, Reason: fmt.Sprintf("unsupported compatibility metadata format %d", state.formatVersion)}
	}

	var previousReader, previousWriter int64
	for index, item := range ledger {
		expectedVersion := int64(index + 1)
		if item.version != expectedVersion {
			return SchemaStatus{}, &MigrationHistoryError{Version: item.version, Reason: fmt.Sprintf("expected contiguous version %d", expectedVersion)}
		}
		if item.name == "" || !validChecksum(item.checksum) || item.minimumReader < 0 || item.minimumWriter < 0 {
			return SchemaStatus{}, &MigrationHistoryError{Version: item.version, Reason: "ledger row contains invalid identity, checksum, or compatibility values"}
		}
		if index > 0 && (item.minimumReader < previousReader || item.minimumWriter < previousWriter) {
			return SchemaStatus{}, &MigrationHistoryError{Version: item.version, Reason: "minimum compatibility versions decreased"}
		}
		if index < len(definitions) {
			definition := definitions[index]
			expectedChecksum := MigrationChecksum(definition)
			if item.checksum != expectedChecksum {
				return SchemaStatus{}, &MigrationChecksumError{Version: item.version, Expected: expectedChecksum, Actual: item.checksum}
			}
			if item.name != definition.Name || item.minimumReader != definition.MinimumReaderVersion || item.minimumWriter != definition.MinimumWriterVersion {
				return SchemaStatus{}, &MigrationHistoryError{Version: item.version, Reason: "ledger identity or compatibility does not match the known definition"}
			}
		}
		previousReader, previousWriter = item.minimumReader, item.minimumWriter
	}
	last := ledger[len(ledger)-1]
	if state.current != last.version || state.minimumReader != last.minimumReader || state.minimumWriter != last.minimumWriter {
		return SchemaStatus{}, &MigrationHistoryError{Version: last.version, Reason: "compatibility metadata does not match the ledger head"}
	}
	return SchemaStatus{
		CurrentMigrationVersion: state.current,
		MinimumReaderVersion:    state.minimumReader,
		MinimumWriterVersion:    state.minimumWriter,
	}, nil
}

func validChecksum(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type checksumWriter interface {
	Write([]byte) (int, error)
}

func writeChecksumInt64(writer checksumWriter, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = writer.Write(encoded[:])
}

func writeChecksumString(writer checksumWriter, value string) {
	writeChecksumInt64(writer, int64(len(value)))
	_, _ = writer.Write([]byte(value))
}
