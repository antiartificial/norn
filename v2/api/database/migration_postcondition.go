package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/effect/supervisor"
)

var migrationReadQuery = regexp.MustCompile(`(?is)^\s*(SELECT|WITH)\b`)

// MigrationPostconditionSQL is a reviewed scalar assertion on the original
// database. Query must return exactly one non-null text value; ExpectedValue
// is compared byte-for-byte. The query and expectation are bound to the
// durable migration intent by SHA-256 and must come from the pinned source.
type MigrationPostconditionSQL struct {
	Engine        Engine `json:"engine"`
	Query         string `json:"query"`
	ExpectedValue string `json:"expectedValue"`
}

func (s MigrationPostconditionSQL) SHA256() (string, error) {
	if (s.Engine != EnginePostgreSQL && s.Engine != EngineMySQL) ||
		len(s.Query) == 0 || len(s.Query) > 16<<10 ||
		!migrationReadQuery.MatchString(s.Query) || strings.ContainsAny(s.Query, ";\x00") ||
		len(s.ExpectedValue) > 4<<10 || strings.ContainsRune(s.ExpectedValue, '\x00') {
		return "", fmt.Errorf("migration postcondition SQL is invalid")
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode migration postcondition: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// SQLMigrationPostconditionChecker opens the resolved original target afresh.
// A catalog generation change or different target holds the effect for manual
// review, even when the command reported a contained zero exit.
type SQLMigrationPostconditionChecker struct {
	Resolved ResolvedBinding
	Secrets  SecretSource
	Spec     MigrationPostconditionSQL
	Timeout  time.Duration
}

const defaultMigrationPostconditionTimeout = 30 * time.Second

func (c *SQLMigrationPostconditionChecker) CheckMigrationPostcondition(ctx context.Context, intent supervisor.MigrationIntent) (supervisor.MigrationPostconditionResult, error) {
	if c == nil || c.Secrets == nil || c.Resolved.Target.Engine != c.Spec.Engine {
		return supervisor.MigrationPostconditionResult{}, fmt.Errorf("migration original-target checker is unavailable")
	}
	digest, err := c.Spec.SHA256()
	if err != nil || digest != intent.PostconditionSHA256 {
		return supervisor.MigrationPostconditionResult{}, fmt.Errorf("migration postcondition differs from accepted intent")
	}
	targetSHA, err := TargetIdentitySHA256(c.Resolved.Target)
	if err != nil || targetSHA != intent.TargetSHA256 ||
		c.Resolved.Target.BindingID != intent.TargetBindingID ||
		int64(c.Resolved.Target.BindingGeneration) != intent.TargetGeneration {
		return supervisor.MigrationPostconditionResult{}, fmt.Errorf("migration target differs from accepted intent")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultMigrationPostconditionTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	session, err := OpenSession(checkCtx, c.Resolved, c.Secrets)
	if err != nil {
		return supervisor.MigrationPostconditionResult{}, fmt.Errorf("migration original target cannot be opened: %w", err)
	}
	defer session.Close()
	var actual string
	switch c.Spec.Engine {
	case EnginePostgreSQL:
		actual, err = checkPostgreSQLMigrationSQL(checkCtx, session, c.Spec.Query)
	case EngineMySQL:
		actual, err = checkMySQLMigrationSQL(checkCtx, session, c.Spec.Query)
	}
	if err != nil {
		return supervisor.MigrationPostconditionResult{}, err
	}
	return supervisor.MigrationPostconditionResult{TargetSHA256: targetSHA,
		PostconditionSHA256: digest, Satisfied: actual == c.Spec.ExpectedValue}, nil
}

func checkPostgreSQLMigrationSQL(ctx context.Context, session *Session, query string) (string, error) {
	if session.config == nil {
		return "", fmt.Errorf("PostgreSQL migration target is unavailable")
	}
	connection, err := pgx.ConnectConfig(ctx, session.config.Copy())
	if err != nil {
		return "", fmt.Errorf("PostgreSQL migration target connection failed")
	}
	defer connection.Close(context.Background())
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", fmt.Errorf("PostgreSQL migration read-only transaction failed")
	}
	defer tx.Rollback(context.Background())
	var database, role string
	if err := tx.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&database, &role); err != nil ||
		database != session.target.Database || role != session.target.Role {
		return "", fmt.Errorf("PostgreSQL migration target identity differs")
	}
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return "", fmt.Errorf("PostgreSQL migration postcondition query failed")
	}
	defer rows.Close()
	if len(rows.FieldDescriptions()) != 1 || !rows.Next() {
		return "", fmt.Errorf("PostgreSQL migration postcondition is not one scalar")
	}
	var actual *string
	if err := rows.Scan(&actual); err != nil || actual == nil || rows.Next() || rows.Err() != nil {
		return "", fmt.Errorf("PostgreSQL migration postcondition is not one scalar")
	}
	return *actual, nil
}

func checkMySQLMigrationSQL(ctx context.Context, session *Session, query string) (string, error) {
	if session.mysqlConnector == nil {
		return "", fmt.Errorf("MySQL migration target is unavailable")
	}
	db := sql.OpenDB(session.mysqlConnector)
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("MySQL migration read-only transaction failed")
	}
	defer tx.Rollback()
	var database, role string
	if err := tx.QueryRowContext(ctx, `SELECT DATABASE(), SUBSTRING_INDEX(CURRENT_USER(), '@', 1)`).Scan(&database, &role); err != nil ||
		database != session.target.Database || role != session.target.Role {
		return "", fmt.Errorf("MySQL migration target identity differs")
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("MySQL migration postcondition query failed")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) != 1 || !rows.Next() {
		return "", fmt.Errorf("MySQL migration postcondition is not one scalar")
	}
	var actual sql.NullString
	if err := rows.Scan(&actual); err != nil || !actual.Valid || rows.Next() || rows.Err() != nil {
		return "", fmt.Errorf("MySQL migration postcondition is not one scalar")
	}
	return actual.String, nil
}

var _ supervisor.MigrationPostconditionChecker = (*SQLMigrationPostconditionChecker)(nil)
