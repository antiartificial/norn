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
	{version: 16, name: "snapshot-publication-intents", checksum: "3286426f2a2e3b48585596c0c831a27fdbc57587814291063b8da253079eada7", minimumReader: 2, minimumWriter: 13},
	{version: 17, name: "archive-backed-operation-acceptance-retirement", checksum: "5e449c3ebaa5f6630bb7c3cdb75fba027581ae83b77ff62551e5951effd59a79", minimumReader: 3, minimumWriter: 14},
	{version: 18, name: "private-function-invocation-material", checksum: "14d89d2a9ad0f5fc4c9873935251fed668cb5c1d61c9c098a663f00862e1e997", minimumReader: 3, minimumWriter: 15},
	{version: 19, name: "function-invocation-effect-attempts", checksum: "e1c5f09cb3d3a45ec6dabebde3ca91ed21dfa8259329e208b121dcde9125632e", minimumReader: 3, minimumWriter: 16},
	{version: 20, name: "function-invocation-variable-cleanup", checksum: "2bfd0a10c48636574e1845a45bfaa418d6d4ae33df903e433cc291bfd546553a", minimumReader: 3, minimumWriter: 17},
	{version: 21, name: "function-invocation-operation-evidence", checksum: "d5697525fd96af9b2923f0e6a7a0ed9c471537d4d15980e6bb5532c5d44b672b", minimumReader: 3, minimumWriter: 18},
	{version: 22, name: "function-deployment-spec-provenance", checksum: "28d981f0686414bdd46e80f93d9bccec9e90f9dba5d0185153a7f2160979df0b", minimumReader: 4, minimumWriter: 19},
	{version: 23, name: "function-invocation-evidence-reader-contract", checksum: "4514b7e37abd4ea7585a917ac1643bf7a8f932017ec026056c58e6b78a6a5317", minimumReader: 4, minimumWriter: 19},
	{version: 24, name: "mysql-restore-durable-intents", checksum: "e8766aad6a11cf6d4ecc051ca7c86d3fc807c01c316573e003457f428eec0d09", minimumReader: 4, minimumWriter: 19},
	{version: 25, name: "mysql-restore-maintenance-fences", checksum: "af1fc371fdad3cc811bfe9506e8c4247928ee7e37ec5697efa5405a7d2d89fe8", minimumReader: 4, minimumWriter: 19},
	{version: 26, name: "mysql-runtime-launch-reservations", checksum: "ce15e12b8e07f0321f788663e860559010b15569a25dce4992ea3176533ddc7b", minimumReader: 4, minimumWriter: 19},
	{version: 27, name: "mysql-restore-runtime-account-locks", checksum: "2def0571daf4c3183e1b572dc6b54ff79ca45ae18454acdab7af9f52c9532db1", minimumReader: 4, minimumWriter: 19},
	{version: 28, name: "runtime-mutation-fence", checksum: "86f4bef45ea1666749ec12eecdbc85e7ab7eeafc9b7f1ea1cb86802432e8b574", minimumReader: 4, minimumWriter: 20},
	{version: 29, name: "mysql-source-snapshot-intents", checksum: "210f680a203ee48641f58978e5d22630f458c730c95dabc5972829a16023e1da", minimumReader: 4, minimumWriter: 21},
	{version: 30, name: "mysql-source-snapshot-stop-checkpoint", checksum: "537c79e3ddfbeba01e1426705ab9bf744536dab36385e0c64b602dfa77ce8b3c", minimumReader: 4, minimumWriter: 22},
	{version: 31, name: "mysql-source-snapshot-account-lock-checkpoint", checksum: "c551d36c1b8be3dbad1c50bfc80c7da4d3037dd62c674972c87e6c2c80838d18", minimumReader: 4, minimumWriter: 23},
	{version: 32, name: "mysql-source-snapshot-artifact-receipt", checksum: "dc5898700f080c48d1bc94b70d0549f0a2bfd57137af67f365ce3ed31a1b5991", minimumReader: 4, minimumWriter: 24},
	{version: 33, name: "mysql-restore-source-artifact-receipt", checksum: "b40428948f15ff910147fc8002d5a6acc4f175cb0d5d4a774fbaace3b8a07abe", minimumReader: 4, minimumWriter: 25},
	{version: 34, name: "mysql-restore-runtime-fence-transfer", checksum: "905b490888349c721fd3443f8bd1d3bb46654d938750abd154642fb1152cdee8", minimumReader: 4, minimumWriter: 26},
	{version: 35, name: "mysql-source-snapshot-retained-artifact", checksum: "d344bff440a4c0bb59e24ab8c7691ff125209ee6fef0ab0ef8d63158396e761a", minimumReader: 5, minimumWriter: 27},
	{version: 36, name: "mysql-restore-recovery-intents", checksum: "5f4006ecf406258051e3d3710daaf11d5346f65976458a120438a915d5c68bd4", minimumReader: 5, minimumWriter: 28},
	{version: 37, name: "mysql-restore-recovery-target-unlock", checksum: "194f4847bafc639d4b29d5eab6b120eca61acb0b3f4b467a21c0c3855d8dd42f", minimumReader: 5, minimumWriter: 29},
	{version: 38, name: "mysql-restore-recovery-runtime-release", checksum: "d06af57118195ad80f36b728124536d297f0e77304857905316e67eb50f70c2c", minimumReader: 5, minimumWriter: 30},
	{version: 39, name: "snapshot-export-intents", checksum: "4f8b627afa2fdc4cc5cbe53d3ed5a852398507a638db3b3986052789614f370b", minimumReader: 5, minimumWriter: 31},
	{version: 40, name: "mysql-source-snapshot-reconciled-predecessor", checksum: "d48d527f07353ed0ae301eb47282808f2497979d2687aae40f064b837f7ddf59", minimumReader: 5, minimumWriter: 31},
	{version: 41, name: "mysql-source-snapshot-proved-successor", checksum: "0ab38134d51f4c96bfaeadb5951c7da27db5650a62d6ed402bd3eae37f2b52b2", minimumReader: 5, minimumWriter: 31},
	{version: 42, name: "mysql-source-snapshot-stage-successor", checksum: "e00a3bebe67cc3ae7f017875228fd26909b562d68cabd206949e6f0bb660adfd", minimumReader: 5, minimumWriter: 31},
	{version: 43, name: "mysql-source-snapshot-publish-successor", checksum: "28a1fa48c7bb25f3ce6dde4a05682f679c9cdeafefe842c0b6365e15b9dccb75", minimumReader: 5, minimumWriter: 31},
	{version: 44, name: "master-protected-pilot-reconciliation", checksum: "ec81c702e6d469938b895407e76cd055bcdabd87bb21c72d454d9e050205055f", minimumReader: 5, minimumWriter: 31},
	{version: 45, name: "release-attestation-byte-reserve", checksum: "431039613471382276b79337a4dca8207964a877cf9e72603cad724aeb26fe20", minimumReader: 5, minimumWriter: 31},
	{version: 46, name: "database-cutover-journal", checksum: "8ccd3c793766370b44f75223311215ba8cd83e32236bfa1898443d08e2a871ae", minimumReader: 5, minimumWriter: 31},
	{version: 47, name: "database-cutover-evidence-references", checksum: "00bb277fbb53eb7aa6ad8291a11f8ac3ab45b70e369d03da93f99b9b5e387d8f", minimumReader: 5, minimumWriter: 31},
	{version: 48, name: "fleet-targets-and-authority-epoch", checksum: "c5d952e95c87a19b6a45736f1c81cc7afc8a2201282264c7bbeeac6a6b9e629f", minimumReader: 5, minimumWriter: 32},
	{version: 49, name: "fleet-resources-and-observations", checksum: "e65825ec6dc64e6ec744a790cd7dd35867ff9a6cffd5769aa2572c4ba9590012", minimumReader: 5, minimumWriter: 32},
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
