package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// TestMySQLSnapshotMaintenanceAccountAfterRuntimeFence is an opt-in
// disposable MySQL 8.4 rehearsal. It proves a distinct catalog-bound snapshot
// account can stage a dump after the runtime account is locked, and that a
// caller cannot substitute another account for that snapshot identity.
func TestMySQLSnapshotMaintenanceAccountAfterRuntimeFence(t *testing.T) {
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	if adminDSN == "" {
		t.Skip("NORN_TEST_MYSQL_DSN is not set")
	}
	dumpTool, err := exec.LookPath("mysqldump")
	if err != nil {
		t.Skip("mysqldump is not installed")
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	var version string
	if err := admin.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil || !strings.HasPrefix(version, "8.4.") {
		t.Skip("NORN_TEST_MYSQL_DSN is not a disposable MySQL 8.4 server")
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal("invalid disposable MySQL address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid disposable MySQL port")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	databaseName := "norn_snapshot_" + suffix
	runtimeUser, snapshotUser, wrongUser, fenceUser := "runtime_"+suffix, "snapshot_"+suffix, "wrong_"+suffix, "fence_"+suffix
	const runtimePassword = "runtime-snapshot-canary"
	const snapshotPassword = "snapshot-account-canary"
	const wrongPassword = "wrong-snapshot-canary"
	const fencePassword = "fence-snapshot-canary"
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		for _, user := range []string{runtimeUser, snapshotUser, wrongUser, fenceUser} {
			_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+user+"'@'%'")
		}
	}()
	for index, statement := range []string{
		"CREATE DATABASE `" + databaseName + "`",
		"CREATE USER '" + runtimeUser + "'@'%' IDENTIFIED BY '" + runtimePassword + "'",
		"CREATE USER '" + snapshotUser + "'@'%' IDENTIFIED BY '" + snapshotPassword + "'",
		"CREATE USER '" + wrongUser + "'@'%' IDENTIFIED BY '" + wrongPassword + "'",
		"CREATE USER '" + fenceUser + "'@'%' IDENTIFIED BY '" + fencePassword + "'",
		"GRANT ALL PRIVILEGES ON `" + databaseName + "`.* TO '" + runtimeUser + "'@'%'",
		"GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON `" + databaseName + "`.* TO '" + snapshotUser + "'@'%'",
		"GRANT CREATE USER, PROCESS, CONNECTION_ADMIN ON *.* TO '" + fenceUser + "'@'%'",
		"GRANT SELECT ON mysql.user TO '" + fenceUser + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare disposable MySQL snapshot fixture statement %d failed: %v", index+1, err)
		}
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+databaseName+"`.marker (value VARCHAR(80) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+databaseName+"`.marker VALUES ('snapshot-maintenance-marker')"); err != nil {
		t.Fatal(err)
	}
	toolBytes, err := os.ReadFile(dumpTool)
	if err != nil {
		t.Fatal(err)
	}
	toolSHA := sha256.Sum256(toolBytes)
	stage := t.TempDir()
	if err := os.Chmod(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	maintenance := MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: snapshotUser, SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:snapshot", RestoreRole: "restore_" + suffix, RestoreAccountHost: "%", RestoreCredentialRef: "secret:restore", FenceRole: fenceUser, FenceAccountHost: "%", FenceCredentialRef: "secret:fence"}
	source := ResolvedBinding{Target: TargetIdentity{ServiceID: "mysql-local", ServiceGeneration: 1, BindingID: "snapshot-source", BindingGeneration: 1, Engine: EngineMySQL, Database: databaseName, Role: runtimeUser}, Purpose: PurposeApplication, CredentialRef: "secret:runtime", Endpoint: DatabaseEndpoint{Host: host, Port: port}, TLS: DatabaseTLS{Mode: TLSDisabled}, MySQLMaintenance: &maintenance}
	secrets := literalSecrets{"secret:runtime": fmt.Sprintf(`{"password":%q}`, runtimePassword), "secret:snapshot": fmt.Sprintf(`{"password":%q}`, snapshotPassword), "secret:wrong": fmt.Sprintf(`{"password":%q}`, wrongPassword), "secret:fence": fmt.Sprintf(`{"password":%q}`, fencePassword)}
	if err := FenceMySQLRuntimeAccountForRestore(ctx, source, maintenance, secrets); err != nil {
		t.Fatal("runtime account fence failed", err)
	}
	runtimeConfig := mysql.NewConfig()
	runtimeConfig.User, runtimeConfig.Passwd, runtimeConfig.DBName, runtimeConfig.Net, runtimeConfig.Addr = runtimeUser, runtimePassword, databaseName, "tcp", adminConfig.Addr
	runtime := sql.OpenDB(mustMySQLConnector(t, runtimeConfig))
	defer runtime.Close()
	if err := runtime.PingContext(ctx); err == nil {
		t.Fatal("locked runtime account accepted new authentication")
	}
	path, artifact, err := StageMySQLSQLSnapshotWithMaintenanceCredential(ctx, source, source.Target, secrets, dumpTool, fmt.Sprintf("%x", toolSHA), stage)
	if err != nil {
		t.Fatalf("snapshot account could not stage a dump after runtime lock: %v", err)
	}
	defer os.Remove(path)
	dump, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(dump), "snapshot-maintenance-marker") || artifact.Source != source.Target {
		t.Fatalf("maintenance snapshot artifact is invalid: artifact=%+v err=%v", artifact, err)
	}
	wrong := source
	wrong.Target.Role, wrong.CredentialRef = wrongUser, "secret:wrong"
	if _, _, err := StageMySQLSQLSnapshotWithResolvedCredential(ctx, source, wrong, source.Target, secrets, dumpTool, fmt.Sprintf("%x", toolSHA), stage); err == nil {
		t.Fatal("snapshot staging accepted a substituted snapshot account")
	}
}
