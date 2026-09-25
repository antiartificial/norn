package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MySQLRestoreExpectation is source-derived proof that must be reproduced by
// the destination before a private restore may receive a success receipt.
// SchemaSHA256 covers table/view definitions plus routines, triggers and
// events. DataSHA256 covers every base table name, exact row count and a
// deterministic SHA-256 digest of its typed row stream. TableCount makes an
// empty or truncated data manifest explicit.
type MySQLRestoreExpectation struct {
	SchemaSHA256 string `json:"schemaSha256"`
	DataSHA256   string `json:"dataSha256"`
	TableCount   int64  `json:"tableCount"`
}

// InspectMySQLRestoreExpectation independently reads a resolved MySQL target.
// It is used both around source staging and after target import.
func InspectMySQLRestoreExpectation(ctx context.Context, resolved ResolvedBinding, secrets SecretSource) (MySQLRestoreExpectation, error) {
	if resolved.Target.Engine != EngineMySQL || !validMySQLArtifactIdentity(resolved.Target) {
		return MySQLRestoreExpectation{}, fmt.Errorf("MySQL restore expectation target is invalid")
	}
	session, err := OpenSession(ctx, resolved, secrets)
	if err != nil {
		return MySQLRestoreExpectation{}, err
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		return MySQLRestoreExpectation{}, err
	}
	if session.mysqlConnector == nil {
		return MySQLRestoreExpectation{}, fmt.Errorf("MySQL restore expectation connector is unavailable")
	}
	db := sql.OpenDB(session.mysqlConnector)
	defer db.Close()
	return inspectMySQLRestoreExpectationDB(ctx, db, resolved.Target.Database)
}

func inspectMySQLRestoreExpectationDB(ctx context.Context, db *sql.DB, database string) (MySQLRestoreExpectation, error) {
	hash := sha256.New()
	tables, err := mysqlSchemaObjects(ctx, db, database)
	if err != nil {
		return MySQLRestoreExpectation{}, err
	}
	for _, table := range tables {
		writeMySQLExpectationHash(hash, "object", table.kind, table.name)
	}
	for _, query := range mysqlAuxiliarySchemaQueries {
		rows, err := db.QueryContext(ctx, query.sql)
		if err != nil {
			return MySQLRestoreExpectation{}, fmt.Errorf("MySQL restore schema inspection failed for %s", query.label)
		}
		for rows.Next() {
			var encoded string
			if err := rows.Scan(&encoded); err != nil {
				rows.Close()
				return MySQLRestoreExpectation{}, fmt.Errorf("MySQL restore schema inspection failed for %s", query.label)
			}
			writeMySQLExpectationHash(hash, query.label, normalizeMySQLSchemaDefinition(encoded, database))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return MySQLRestoreExpectation{}, fmt.Errorf("MySQL restore schema inspection failed for %s", query.label)
		}
		rows.Close()
	}

	expectation := MySQLRestoreExpectation{SchemaSHA256: hex.EncodeToString(hash.Sum(nil))}
	dataHash := sha256.New()
	for _, table := range tables {
		if table.kind != "BASE TABLE" {
			continue
		}
		count, digest, err := mysqlTableDataDigest(ctx, db, database, table.name)
		if err != nil {
			return MySQLRestoreExpectation{}, err
		}
		writeMySQLExpectationHash(dataHash, table.name, strconv.FormatInt(count, 10), digest)
		expectation.TableCount++
	}
	expectation.DataSHA256 = hex.EncodeToString(dataHash.Sum(nil))
	return expectation, nil
}

// VerifyMySQLRestoreTarget fails closed unless the target independently
// reproduces the signed source expectation.
func VerifyMySQLRestoreTarget(ctx context.Context, resolved ResolvedBinding, secrets SecretSource, expected MySQLRestoreExpectation) error {
	if !validMySQLRestoreExpectation(expected) {
		return fmt.Errorf("MySQL restore expectation is invalid")
	}
	actual, err := InspectMySQLRestoreExpectation(ctx, resolved, secrets)
	if err != nil {
		return err
	}
	if actual.SchemaSHA256 != expected.SchemaSHA256 {
		return fmt.Errorf("MySQL restored target schema does not match the signed source expectation")
	}
	if actual.DataSHA256 != expected.DataSHA256 || actual.TableCount != expected.TableCount {
		return fmt.Errorf("MySQL restored target data does not match the signed source expectation")
	}
	return nil
}

func validMySQLRestoreExpectation(expected MySQLRestoreExpectation) bool {
	if len(expected.SchemaSHA256) != 64 || len(expected.DataSHA256) != 64 || expected.TableCount < 0 ||
		strings.ToLower(expected.SchemaSHA256) != expected.SchemaSHA256 || strings.ToLower(expected.DataSHA256) != expected.DataSHA256 {
		return false
	}
	if _, err := hex.DecodeString(expected.SchemaSHA256); err != nil {
		return false
	}
	if _, err := hex.DecodeString(expected.DataSHA256); err != nil {
		return false
	}
	return true
}

type mysqlSchemaObject struct{ name, kind string }

func mysqlSchemaObjects(ctx context.Context, db *sql.DB, database string) ([]mysqlSchemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME`, database)
	if err != nil {
		return nil, fmt.Errorf("MySQL restore schema object inspection failed")
	}
	defer rows.Close()
	var result []mysqlSchemaObject
	for rows.Next() {
		var item mysqlSchemaObject
		if err := rows.Scan(&item.name, &item.kind); err != nil || strings.ContainsRune(item.name, 0) || item.name == "" || (item.kind != "BASE TABLE" && item.kind != "VIEW") {
			return nil, fmt.Errorf("MySQL restore schema object inspection failed")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

var mysqlAuxiliarySchemaQueries = []struct{ label, sql string }{
	{"table", `SELECT JSON_ARRAY(TABLE_NAME,TABLE_TYPE,ENGINE,ROW_FORMAT,TABLE_COLLATION,CREATE_OPTIONS,TABLE_COMMENT) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME`},
	{"column", `SELECT JSON_ARRAY(TABLE_NAME,COLUMN_NAME,ORDINAL_POSITION,COLUMN_DEFAULT,IS_NULLABLE,DATA_TYPE,CHARACTER_MAXIMUM_LENGTH,NUMERIC_PRECISION,NUMERIC_SCALE,DATETIME_PRECISION,CHARACTER_SET_NAME,COLLATION_NAME,COLUMN_TYPE,COLUMN_KEY,EXTRA,PRIVILEGES,COLUMN_COMMENT,GENERATION_EXPRESSION) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,ORDINAL_POSITION`},
	{"index", `SELECT JSON_ARRAY(TABLE_NAME,NON_UNIQUE,INDEX_NAME,SEQ_IN_INDEX,COLUMN_NAME,COLLATION,SUB_PART,NULLABLE,INDEX_TYPE,COMMENT,INDEX_COMMENT,IS_VISIBLE,EXPRESSION) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX`},
	{"constraint", `SELECT JSON_ARRAY(TABLE_NAME,CONSTRAINT_NAME,CONSTRAINT_TYPE,ENFORCED) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,CONSTRAINT_NAME`},
	{"key", `SELECT JSON_ARRAY(TABLE_NAME,CONSTRAINT_NAME,COLUMN_NAME,ORDINAL_POSITION,POSITION_IN_UNIQUE_CONSTRAINT,REFERENCED_TABLE_NAME,REFERENCED_COLUMN_NAME) FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,CONSTRAINT_NAME,ORDINAL_POSITION`},
	{"check", `SELECT JSON_ARRAY(CONSTRAINT_NAME,CHECK_CLAUSE) FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() ORDER BY CONSTRAINT_NAME`},
	{"view", `SELECT JSON_ARRAY(TABLE_NAME,VIEW_DEFINITION,CHECK_OPTION,IS_UPDATABLE,SECURITY_TYPE,CHARACTER_SET_CLIENT,COLLATION_CONNECTION) FROM information_schema.VIEWS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME`},
	{"routine", `SELECT JSON_ARRAY(ROUTINE_NAME,ROUTINE_TYPE,DATA_TYPE,DTD_IDENTIFIER,ROUTINE_DEFINITION,IS_DETERMINISTIC,SQL_DATA_ACCESS,SECURITY_TYPE,SQL_MODE,CHARACTER_SET_CLIENT,COLLATION_CONNECTION,DATABASE_COLLATION) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=DATABASE() ORDER BY ROUTINE_TYPE,ROUTINE_NAME`},
	{"trigger", `SELECT JSON_ARRAY(TRIGGER_NAME,EVENT_MANIPULATION,EVENT_OBJECT_TABLE,ACTION_ORDER,ACTION_CONDITION,ACTION_STATEMENT,ACTION_ORIENTATION,ACTION_TIMING,ACTION_REFERENCE_OLD_ROW,ACTION_REFERENCE_NEW_ROW,SQL_MODE,CHARACTER_SET_CLIENT,COLLATION_CONNECTION,DATABASE_COLLATION) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE() ORDER BY TRIGGER_NAME`},
	{"event", `SELECT JSON_ARRAY(EVENT_NAME,EVENT_DEFINITION,EVENT_TYPE,EXECUTE_AT,INTERVAL_VALUE,INTERVAL_FIELD,SQL_MODE,STARTS,ENDS,STATUS,ON_COMPLETION,CHARACTER_SET_CLIENT,COLLATION_CONNECTION,DATABASE_COLLATION) FROM information_schema.EVENTS WHERE EVENT_SCHEMA=DATABASE() ORDER BY EVENT_NAME`},
}

var mysqlDefinerPattern = regexp.MustCompile(`(?i)DEFINER=` + "`[^`]*`@`[^`]*`")

func normalizeMySQLSchemaDefinition(definition, database string) string {
	definition = strings.ReplaceAll(definition, mysqlQuotedIdentifier(database)+".", "`<database>`.")
	return mysqlDefinerPattern.ReplaceAllString(definition, "DEFINER=`<definer>`")
}

func mysqlTableDataDigest(ctx context.Context, db *sql.DB, database, table string) (int64, string, error) {
	columnRows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, database, table)
	if err != nil {
		return 0, "", fmt.Errorf("MySQL restore column inspection failed for %s", table)
	}
	var columns []string
	for columnRows.Next() {
		var column string
		if err := columnRows.Scan(&column); err != nil || column == "" || strings.ContainsRune(column, 0) {
			columnRows.Close()
			return 0, "", fmt.Errorf("MySQL restore column inspection failed for %s", table)
		}
		columns = append(columns, column)
	}
	if err := columnRows.Err(); err != nil || len(columns) == 0 {
		columnRows.Close()
		return 0, "", fmt.Errorf("MySQL restore column inspection failed for %s", table)
	}
	columnRows.Close()
	expressions := make([]string, len(columns))
	order := make([]string, len(columns))
	for i, column := range columns {
		expressions[i] = "IF(" + mysqlQuotedIdentifier(column) + " IS NULL,NULL,HEX(" + mysqlQuotedIdentifier(column) + ")) AS c" + strconv.Itoa(i)
		order[i] = "c" + strconv.Itoa(i)
	}
	query := "SELECT " + strings.Join(expressions, ",") + " FROM " + mysqlQuotedIdentifier(database) + "." + mysqlQuotedIdentifier(table) + " ORDER BY " + strings.Join(order, ",")
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, "", fmt.Errorf("MySQL restore data inspection failed for %s", table)
	}
	defer rows.Close()
	hash := sha256.New()
	values := make([]sql.RawBytes, len(columns))
	dest := make([]interface{}, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	var count int64
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return 0, "", fmt.Errorf("MySQL restore data inspection failed for %s", table)
		}
		for _, value := range values {
			if value == nil {
				writeMySQLExpectationHash(hash, "null")
			} else {
				writeMySQLExpectationHash(hash, "value", string(value))
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("MySQL restore data inspection failed for %s", table)
	}
	return count, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeMySQLExpectationHash(hash interface{ Write([]byte) (int, error) }, fields ...string) {
	var size [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
}

func mysqlQuotedIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
