package database

import (
	"context"
	"database/sql"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"norn/v2/api/effect/supervisor"
	"norn/v2/api/internal/pgtest"
)

func TestMigrationPostconditionSQLRejectsUnsafeShape(t *testing.T) {
	for _, query := range []string{"", "DELETE FROM app_state", "SELECT 1; DELETE FROM app_state", "/* comment */ SELECT 1"} {
		if _, err := (MigrationPostconditionSQL{Engine: EnginePostgreSQL, Query: query, ExpectedValue: "1"}).SHA256(); err == nil {
			t.Fatalf("accepted unreviewable postcondition query %q", query)
		}
	}
	if _, err := (MigrationPostconditionSQL{Engine: EngineMySQL, Query: "SELECT COUNT(*) FROM app_state", ExpectedValue: "1"}).SHA256(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLMigrationPostconditionChecksOriginalMySQLTarget(t *testing.T) {
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	if adminDSN == "" {
		t.Skip("NORN_TEST_MYSQL_DSN is not set")
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	databaseName, role := "norn_mig_"+suffix, "mig_"+suffix
	const password = "migration-postcondition-canary"
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+role+"'@'%'")
	}()
	for _, statement := range []string{
		"CREATE DATABASE `" + databaseName + "`",
		"CREATE USER '" + role + "'@'%' IDENTIFIED BY '" + password + "'",
		"GRANT ALL PRIVILEGES ON `" + databaseName + "`.* TO '" + role + "'@'%'",
		"CREATE TABLE `" + databaseName + "`.app_state (id integer PRIMARY KEY)",
		"INSERT INTO `" + databaseName + "`.app_state VALUES (1)",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal("prepare disposable MySQL postcondition target failed")
		}
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	resolved := ResolvedBinding{Target: TargetIdentity{ServiceID: "scoped-mysql", ServiceGeneration: 1,
		BindingID: "mysql-review", BindingGeneration: 2, Engine: EngineMySQL,
		Database: databaseName, Role: role}, Purpose: PurposeApplication,
		CredentialRef: "secret:mysql-review", TLS: DatabaseTLS{Mode: TLSDisabled},
		Endpoint: DatabaseEndpoint{Host: host, Port: port}}
	spec := MigrationPostconditionSQL{Engine: EngineMySQL, Query: `SELECT CAST(COUNT(*) AS CHAR) FROM app_state`, ExpectedValue: "1"}
	digest, err := spec.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	targetSHA, err := TargetIdentitySHA256(resolved.Target)
	if err != nil {
		t.Fatal(err)
	}
	intent := supervisor.MigrationIntent{TargetSHA256: targetSHA, TargetBindingID: resolved.Target.BindingID,
		TargetGeneration: int64(resolved.Target.BindingGeneration), PostconditionSHA256: digest}
	checker := &SQLMigrationPostconditionChecker{Resolved: resolved, Secrets: literalSecrets{"secret:mysql-review": `{"password":"` + password + `"}`}, Spec: spec}
	result, err := checker.CheckMigrationPostcondition(ctx, intent)
	if err != nil || !result.Satisfied {
		t.Fatalf("original MySQL postcondition=%+v err=%v", result, err)
	}
	checker.Spec.ExpectedValue = "2"
	intent.PostconditionSHA256, _ = checker.Spec.SHA256()
	result, err = checker.CheckMigrationPostcondition(ctx, intent)
	if err != nil || result.Satisfied {
		t.Fatalf("false MySQL postcondition=%+v err=%v", result, err)
	}
}

func TestSQLMigrationPostconditionChecksOriginalPostgreSQLTarget(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "migration_review")
	server.Exec(t, "migration_review", `CREATE TABLE app_state (id integer PRIMARY KEY); INSERT INTO app_state VALUES (1)`)
	resolved := ResolvedBinding{Target: TargetIdentity{ServiceID: "scoped-pg", ServiceGeneration: 1,
		BindingID: "review-db", BindingGeneration: 4, Engine: EnginePostgreSQL,
		Database: "migration_review", Role: server.User}, Purpose: PurposeApplication,
		CredentialRef: "secret:review/db", TLS: DatabaseTLS{Mode: TLSDisabled},
		Endpoint: DatabaseEndpoint{Host: server.SocketDir, Port: server.Port}}
	spec := MigrationPostconditionSQL{Engine: EnginePostgreSQL, Query: `SELECT COUNT(*)::text FROM app_state`, ExpectedValue: "1"}
	digest, err := spec.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	targetSHA, err := TargetIdentitySHA256(resolved.Target)
	if err != nil {
		t.Fatal(err)
	}
	intent := supervisor.MigrationIntent{TargetSHA256: targetSHA, TargetBindingID: resolved.Target.BindingID,
		TargetGeneration: int64(resolved.Target.BindingGeneration), PostconditionSHA256: digest}
	checker := &SQLMigrationPostconditionChecker{Resolved: resolved, Secrets: reviewMaterialSource(`{}`), Spec: spec}
	result, err := checker.CheckMigrationPostcondition(context.Background(), intent)
	if err != nil || !result.Satisfied || result.TargetSHA256 != targetSHA || result.PostconditionSHA256 != digest {
		t.Fatalf("original-target postcondition=%+v err=%v", result, err)
	}
	checker.Spec.ExpectedValue = "2"
	intent.PostconditionSHA256, _ = checker.Spec.SHA256()
	result, err = checker.CheckMigrationPostcondition(context.Background(), intent)
	if err != nil || result.Satisfied {
		t.Fatalf("false postcondition=%+v err=%v", result, err)
	}
	intent.TargetGeneration++
	if _, err := checker.CheckMigrationPostcondition(context.Background(), intent); err == nil {
		t.Fatal("changed binding generation reused original target")
	}
	intent.TargetGeneration--
	checker.Spec.Query = `SELECT id::text FROM app_state UNION ALL SELECT id::text FROM app_state`
	intent.PostconditionSHA256, _ = checker.Spec.SHA256()
	if _, err := checker.CheckMigrationPostcondition(context.Background(), intent); err == nil {
		t.Fatal("multiple postcondition rows were accepted")
	}
	if strings.Contains(result.TargetSHA256, server.SocketDir) {
		t.Fatal("postcondition result exposed private connection location")
	}
}
