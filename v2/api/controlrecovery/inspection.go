package controlrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const inspectionFormat = "norn.control-inspection/v1"

var inspectionExclusions = []string{
	"arbitrary payloads, metadata, free-form diagnostic text, network addresses, and user agents",
	"request idempotency keys and raw operation acceptance request keys",
	"acceptance canonical bytes, request canonical bytes, and signatures",
	"dispatch nonces, notification URLs and credentials, enrollment verifiers, and step-up material",
	"exec commands and ownership tokens",
	"recovery evidence and all secret key material",
}

type knownMigration struct {
	version       int64
	name          string
	checksum      string
	minimumReader int64
	minimumWriter int64
}

// Keep this catalog beside the table registry: inspection must fail closed
// until it is deliberately updated for a newly classified migration.
var inspectionCatalog = []knownMigration{
	{version: 1, name: "legacy-control-schema-baseline", checksum: "6124f0d3fd1e339daea3563f55648d8c0bd4dabbe8ce5f972fccd912ba80d390", minimumReader: 0, minimumWriter: 0},
	{version: 2, name: "atomic-operation-acceptance", checksum: "b4894477aa098df50c0afc3d373310342ccf6b2ccb6e6d46c8b01b43f1f250a1", minimumReader: 1, minimumWriter: 2},
	{version: 3, name: "external-effect-recovery", checksum: "8fc9df8b9732cab071e0aad5ce53efcd402c98c1507d6ceff041cbef17bff557", minimumReader: 1, minimumWriter: 3},
	{version: 4, name: "operation-execution-checkpoints", checksum: "c14847e31286b4679308dd4b7d9fa1375570b5a96fa9e56c64bc17efc2af3542", minimumReader: 1, minimumWriter: 4},
	{version: 5, name: "database-catalog-revisions", checksum: "37ed36618266d27000e16b44dc401a9e0f2018781c4a16a64cf83bd15e8ab283", minimumReader: 1, minimumWriter: 5},
	{version: 6, name: "evidence-archive-outbox", checksum: "068010b34109e8be94a98de60ed09d3afabff108e588acdbe93460ac55f91e10", minimumReader: 1, minimumWriter: 5},
	{version: 7, name: "evidence-archive-reader-contract", checksum: "7ed927881b952cf366b5415fa78cc6de74be6ef6cb354a79d899d6422c9d343c", minimumReader: 2, minimumWriter: 5},
	{version: 8, name: "evidence-reserve-admission", checksum: "f47fac7e5b4da8ea703025a83237dfc11183669273c6e75fe213bd17e2115de9", minimumReader: 2, minimumWriter: 6},
	{version: 9, name: "control-event-replay-retention", checksum: "83f86234aa3fef131423978488be125b07933d264663a15b178cd05adebde7fb", minimumReader: 2, minimumWriter: 6},
	{version: 10, name: "non-saga-signed-receipt-evidence", checksum: "698e04711a17727f46cd788bb33e55508f8f49ab99b09a62d8c07fbfd26e709a", minimumReader: 2, minimumWriter: 7},
	{version: 11, name: "durable-app-desired-replicas", checksum: "c3ce6b77e9428e83d8282e06c65c17a13e29c2a85bf374bdb81fc7438dd3cb07", minimumReader: 2, minimumWriter: 8},
	{version: 12, name: "regional-durable-app-desired-replicas", checksum: "379253085218d1161710dbc72afb9c227bd6b464da94244ec0d8edf7e2a09ae4", minimumReader: 2, minimumWriter: 9},
	{version: 13, name: "durable-restart-effect-sources", checksum: "6bc653a27de50c14482fd840ea4af06f78ffbf4443fd568da2a8b960c1bcd00a", minimumReader: 2, minimumWriter: 10},
	{version: 14, name: "signed-acceptance-byte-reserve", checksum: "56cb305a3d4cb2a3d8c0e2b89dd75584dee58cfb0e5c307912b45908456b0e1d", minimumReader: 2, minimumWriter: 11},
	{version: 15, name: "operation-replay-expiry", checksum: "43066af7ce262edd8fd2f578f024bb05c6bcd5623721c6028546781337ecb287", minimumReader: 2, minimumWriter: 12},
}

// Inspection is a redacted, non-restorable view of one repeatable-read control
// snapshot. It intentionally contains no database address or connection data.
type Inspection struct {
	Format     string            `json:"format"`
	Restorable bool              `json:"restorable"`
	Excludes   []string          `json:"excludes"`
	Tables     []InspectionTable `json:"tables"`
}

// InspectionTable contains every row's allowlisted projection in primary-key
// order. Count is an int64 so encoding/json retains its exact integer lexeme.
type InspectionTable struct {
	Name    string            `json:"name"`
	Count   int64             `json:"count"`
	Columns []string          `json:"columns"`
	Rows    []json.RawMessage `json:"rows"`
}

// SchemaClassificationError means the physical schema and explicit registry
// differ. Names are deliberately not included in the error to avoid turning a
// crafted identifier into a logging exfiltration path.
type SchemaClassificationError struct {
	UnknownTables        int
	MissingTables        int
	UnknownColumns       int
	MissingColumns       int
	PrimaryKeyMismatches int
}

func (e *SchemaClassificationError) Error() string {
	return fmt.Sprintf("control inspection refused unclassified schema (unknown tables=%d, missing tables=%d, unknown columns=%d, missing columns=%d, primary key mismatches=%d)", e.UnknownTables, e.MissingTables, e.UnknownColumns, e.MissingColumns, e.PrimaryKeyMismatches)
}

// CatalogValidationError means the migration ledger or compatibility singleton
// is not the exact catalog understood by this inspection binary. Values are not
// printed because catalog fields may have been maliciously replaced.
type CatalogValidationError struct{}

func (*CatalogValidationError) Error() string {
	return "control inspection refused unsupported schema catalog"
}

// ExportError identifies a safe inspection phase without embedding PostgreSQL
// errors, row data, payloads, or a database URL in the printable message.
type ExportError struct {
	Phase string
	cause error
}

func (e *ExportError) Error() string { return "control inspection failed during " + e.Phase }
func (e *ExportError) Unwrap() error { return e.cause }

type exportOptions struct {
	registry           []Table
	afterSnapshot      func(context.Context) error
	allowMiniExtension bool
}

// ExportInspection writes a deterministic, redacted inspection document for
// the exact named schema. It never changes search_path and never falls back to
// an unqualified relation. Validation and database-read failures happen before
// the first destination write; a failing io.Writer can still accept a prefix.
func ExportInspection(ctx context.Context, pool *pgxpool.Pool, schema string, destination io.Writer) error {
	return exportInspection(ctx, pool, schema, destination, exportOptions{registry: InspectionRegistry(), allowMiniExtension: true})
}

func exportInspection(ctx context.Context, pool *pgxpool.Pool, schema string, destination io.Writer, options exportOptions) error {
	if pool == nil {
		return &ExportError{Phase: "input validation", cause: fmt.Errorf("nil PostgreSQL pool")}
	}
	if destination == nil {
		return &ExportError{Phase: "input validation", cause: fmt.Errorf("nil destination")}
	}
	if strings.TrimSpace(schema) == "" {
		return &ExportError{Phase: "input validation", cause: fmt.Errorf("empty schema")}
	}
	if err := validateRegistry(options.registry); err != nil {
		return &ExportError{Phase: "registry validation", cause: err}
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return &ExportError{Phase: "read-only snapshot start", cause: err}
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if options.allowMiniExtension {
		options.registry, err = registryWithMiniExtension(ctx, tx, schema, options.registry)
		if err != nil {
			return &ExportError{Phase: "schema classification", cause: err}
		}
		if err := validateRegistry(options.registry); err != nil {
			return &ExportError{Phase: "registry validation", cause: err}
		}
	}

	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return &ExportError{Phase: "snapshot normalization", cause: err}
	}
	if err := classifySchema(ctx, tx, schema, options.registry); err != nil {
		return err
	}
	if err := validateCatalog(ctx, tx, schema); err != nil {
		return err
	}
	if options.afterSnapshot != nil {
		if err := options.afterSnapshot(ctx); err != nil {
			return &ExportError{Phase: "snapshot barrier", cause: err}
		}
	}

	document := Inspection{
		Format:     inspectionFormat,
		Restorable: false,
		Excludes:   append([]string(nil), inspectionExclusions...),
		Tables:     make([]InspectionTable, 0, len(options.registry)),
	}
	for _, table := range options.registry {
		exported, err := inspectTable(ctx, tx, schema, table)
		if err != nil {
			return &ExportError{Phase: "registered table read", cause: err}
		}
		document.Tables = append(document.Tables, exported)
	}
	if err := tx.Commit(ctx); err != nil {
		return &ExportError{Phase: "read-only snapshot commit", cause: err}
	}

	var buffered bytes.Buffer
	encoder := json.NewEncoder(&buffered)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return &ExportError{Phase: "document encoding", cause: err}
	}
	if _, err := io.Copy(destination, &buffered); err != nil {
		return &ExportError{Phase: "document write", cause: err}
	}
	return nil
}

func validateRegistry(registry []Table) error {
	if len(registry) == 0 {
		return fmt.Errorf("empty registry")
	}
	tables := make(map[string]struct{}, len(registry))
	for _, table := range registry {
		if table.Name == "" || len(table.OrderBy) == 0 || len(table.Columns) == 0 {
			return fmt.Errorf("incomplete table classification")
		}
		if _, exists := tables[table.Name]; exists {
			return fmt.Errorf("duplicate table classification")
		}
		tables[table.Name] = struct{}{}
		columns := make(map[string]struct{}, len(table.Columns))
		included := 0
		for _, column := range table.Columns {
			if column.Name == "" {
				return fmt.Errorf("empty column classification")
			}
			if _, exists := columns[column.Name]; exists {
				return fmt.Errorf("duplicate column classification")
			}
			columns[column.Name] = struct{}{}
			if column.Include {
				included++
			}
		}
		if included == 0 {
			return fmt.Errorf("table has no inspection projection")
		}
		for _, order := range table.OrderBy {
			if _, exists := columns[order]; !exists {
				return fmt.Errorf("ordering column is not classified")
			}
		}
	}
	return nil
}

func classifySchema(ctx context.Context, tx pgx.Tx, schema string, registry []Table) error {
	tableRows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`, schema)
	if err != nil {
		return &ExportError{Phase: "schema classification", cause: err}
	}
	actual := make(map[string]map[string]struct{})
	for tableRows.Next() {
		var table string
		if err := tableRows.Scan(&table); err != nil {
			tableRows.Close()
			return &ExportError{Phase: "schema classification", cause: err}
		}
		actual[table] = make(map[string]struct{})
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return &ExportError{Phase: "schema classification", cause: err}
	}
	tableRows.Close()

	rows, err := tx.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'p')
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`, schema)
	if err != nil {
		return &ExportError{Phase: "schema classification", cause: err}
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return &ExportError{Phase: "schema classification", cause: err}
		}
		if actual[table] == nil {
			actual[table] = make(map[string]struct{})
		}
		actual[table][column] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return &ExportError{Phase: "schema classification", cause: err}
	}

	expected := make(map[string]map[string]struct{}, len(registry))
	expectedPrimaryKeys := make(map[string][]string, len(registry))
	for _, table := range registry {
		expected[table.Name] = make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			expected[table.Name][column.Name] = struct{}{}
		}
		expectedPrimaryKeys[table.Name] = table.OrderBy
	}

	classification := &SchemaClassificationError{}
	for table, columns := range actual {
		expectedColumns, exists := expected[table]
		if !exists {
			classification.UnknownTables++
			continue
		}
		for column := range columns {
			if _, exists := expectedColumns[column]; !exists {
				classification.UnknownColumns++
			}
		}
	}
	for table, columns := range expected {
		actualColumns, exists := actual[table]
		if !exists {
			classification.MissingTables++
			continue
		}
		for column := range columns {
			if _, exists := actualColumns[column]; !exists {
				classification.MissingColumns++
			}
		}
	}
	primaryKeys, err := loadPrimaryKeys(ctx, tx, schema)
	if err != nil {
		return &ExportError{Phase: "schema classification", cause: err}
	}
	for table, expectedKey := range expectedPrimaryKeys {
		if !equalStrings(primaryKeys[table], expectedKey) {
			classification.PrimaryKeyMismatches++
		}
	}
	if classification.UnknownTables+classification.MissingTables+classification.UnknownColumns+classification.MissingColumns+classification.PrimaryKeyMismatches > 0 {
		return classification
	}
	return nil
}

func loadPrimaryKeys(ctx context.Context, tx pgx.Tx, schema string) (map[string][]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS key(attnum, position) ON true
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = key.attnum
		WHERE n.nspname = $1 AND i.indisprimary
		ORDER BY c.relname, key.position`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]string)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		result[table] = append(result[table], column)
	}
	return result, rows.Err()
}

func validateCatalog(ctx context.Context, tx pgx.Tx, schema string) error {
	definitions := inspectionCatalog
	rows, err := tx.Query(ctx, `SELECT version,name,checksum,minimum_reader_version,minimum_writer_version FROM `+pgx.Identifier{schema, "norn_schema_migrations"}.Sanitize()+` ORDER BY version`)
	if err != nil {
		return &ExportError{Phase: "catalog validation", cause: err}
	}
	index := 0
	valid := true
	for rows.Next() {
		var version, minimumReader, minimumWriter int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum, &minimumReader, &minimumWriter); err != nil {
			rows.Close()
			return &ExportError{Phase: "catalog validation", cause: err}
		}
		if index >= len(definitions) {
			valid = false
			continue
		}
		definition := definitions[index]
		if version != definition.version || name != definition.name || checksum != definition.checksum || minimumReader != definition.minimumReader || minimumWriter != definition.minimumWriter {
			valid = false
		}
		index++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return &ExportError{Phase: "catalog validation", cause: err}
	}
	rows.Close()
	if index != len(definitions) {
		valid = false
	}

	last := definitions[len(definitions)-1]
	var compatibilityRows int64
	var singleton bool
	var formatVersion int32
	var currentMigration, minimumReader, minimumWriter int64
	err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(bool_and(singleton),false),COALESCE(min(format_version),0),COALESCE(min(current_migration_version),0),COALESCE(min(minimum_reader_version),0),COALESCE(min(minimum_writer_version),0) FROM `+pgx.Identifier{schema, "norn_schema_compatibility"}.Sanitize()).Scan(
		&compatibilityRows, &singleton, &formatVersion, &currentMigration, &minimumReader, &minimumWriter,
	)
	if err != nil {
		return &CatalogValidationError{}
	}
	if compatibilityRows != 1 || !singleton || formatVersion != 1 || currentMigration != last.version || minimumReader != last.minimumReader || minimumWriter != last.minimumWriter {
		valid = false
	}
	if !valid {
		return &CatalogValidationError{}
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func inspectTable(ctx context.Context, tx pgx.Tx, schema string, table Table) (InspectionTable, error) {
	columns := make([]string, 0, len(table.Columns))
	arguments := make([]string, 0, len(table.Columns)*2)
	for _, column := range table.Columns {
		if !column.Include {
			continue
		}
		columns = append(columns, column.Name)
		arguments = append(arguments, quoteLiteral(column.Name), pgx.Identifier{column.Name}.Sanitize())
	}
	order := make([]string, 0, len(table.OrderBy))
	for _, column := range table.OrderBy {
		order = append(order, pgx.Identifier{column}.Sanitize())
	}
	query := fmt.Sprintf(
		"SELECT json_build_object(%s)::text FROM %s ORDER BY %s",
		strings.Join(arguments, ", "),
		pgx.Identifier{schema, table.Name}.Sanitize(),
		strings.Join(order, ", "),
	)
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return InspectionTable{}, err
	}
	defer rows.Close()

	exported := InspectionTable{Name: table.Name, Columns: columns, Rows: make([]json.RawMessage, 0)}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return InspectionTable{}, err
		}
		if !json.Valid([]byte(raw)) {
			return InspectionTable{}, fmt.Errorf("PostgreSQL returned invalid JSON")
		}
		exported.Rows = append(exported.Rows, json.RawMessage(raw))
	}
	if err := rows.Err(); err != nil {
		return InspectionTable{}, err
	}
	exported.Count = int64(len(exported.Rows))
	return exported, nil
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
