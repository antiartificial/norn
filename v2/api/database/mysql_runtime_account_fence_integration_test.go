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
)

// TestMySQLRuntimeAccountFence is opt-in and requires a disposable MySQL 8.0 or 8.4
// instance. It proves that the fence locks the exact account, drops a
// preexisting runtime session, refuses fresh authentication, and only the
// separate explicit unfence operation restores access.
func TestMySQLRuntimeAccountFence(t *testing.T) {
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
	var version string
	if err := admin.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil || (!strings.HasPrefix(version, "8.0.") && !strings.HasPrefix(version, "8.4.")) {
		t.Skip("NORN_TEST_MYSQL_DSN is not a disposable MySQL 8.0 or 8.4 server")
	}

	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	databaseName, runtimeUser, fenceUser := "norn_"+suffix, "runtime_"+suffix, "fence_"+suffix
	const runtimePassword = "runtime-account-fence-canary"
	const fencePassword = "fence-account-canary"
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+runtimeUser+"'@'%'")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+fenceUser+"'@'%'")
	}()
	for index, statement := range []string{
		"CREATE DATABASE `" + databaseName + "`",
		"CREATE USER '" + runtimeUser + "'@'%' IDENTIFIED BY '" + runtimePassword + "'",
		"CREATE USER '" + fenceUser + "'@'%' IDENTIFIED BY '" + fencePassword + "'",
		"GRANT ALL PRIVILEGES ON `" + databaseName + "`.* TO '" + runtimeUser + "'@'%'",
		"GRANT CREATE USER, PROCESS, CONNECTION_ADMIN ON *.* TO '" + fenceUser + "'@'%'",
		"GRANT SELECT ON mysql.user TO '" + fenceUser + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare disposable MySQL runtime-account fence fixture statement %d failed", index+1)
		}
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal("invalid disposable MySQL address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("invalid disposable MySQL port")
	}
	resolved := ResolvedBinding{Target: TargetIdentity{ServiceID: "mysql-local", ServiceGeneration: 1, BindingID: "runtime-account", BindingGeneration: 1, Engine: EngineMySQL, Database: databaseName, Role: runtimeUser}, Purpose: PurposeApplication, CredentialRef: "secret:runtime", Endpoint: DatabaseEndpoint{Host: host, Port: port}, TLS: DatabaseTLS{Mode: TLSDisabled}}
	fence := mysqlRuntimeAccountFence{FenceUser: fenceUser, FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%", DedicatedRuntimeUsername: runtimeUser}
	secrets := literalSecrets{"secret:runtime": `{"password":"` + runtimePassword + `"}`, "secret:fence": `{"password":"` + fencePassword + `"}`}
	maintenance := MySQLMaintenanceCredentials{FenceRole: fenceUser, FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%"}
	if err := InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, maintenance, secrets); err == nil {
		t.Fatal("unlocked runtime account appeared fenced")
	}

	clientConfig := mysql.NewConfig()
	clientConfig.User, clientConfig.Passwd, clientConfig.DBName = runtimeUser, runtimePassword, databaseName
	clientConfig.Net, clientConfig.Addr = "tcp", adminConfig.Addr
	client := sql.OpenDB(mustMySQLConnector(t, clientConfig))
	defer client.Close()
	if err := client.PingContext(ctx); err != nil {
		t.Fatal("preexisting runtime session could not connect")
	}
	var existingSessionID uint64
	if err := client.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&existingSessionID); err != nil {
		t.Fatal("inspect preexisting runtime session")
	}
	if err := fenceMySQLRuntimeAccount(ctx, resolved, fence, secrets); err != nil {
		t.Fatal("runtime account fence failed")
	}
	if err := InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, maintenance, secrets); err != nil {
		t.Fatalf("locked account failed read-only inspection: %v", err)
	}
	var survivingSessions int
	if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.PROCESSLIST WHERE ID = ?", existingSessionID).Scan(&survivingSessions); err != nil || survivingSessions != 0 {
		t.Fatal("preexisting runtime session was not terminated by fence")
	}
	if err := client.PingContext(ctx); err == nil {
		t.Fatal("preexisting runtime session remained usable after fence")
	}
	newClient := sql.OpenDB(mustMySQLConnector(t, clientConfig))
	defer newClient.Close()
	if err := newClient.PingContext(ctx); err == nil {
		t.Fatal("locked runtime account accepted new authentication")
	}
	if err := unfenceMySQLRuntimeAccount(ctx, resolved, fence, secrets); err != nil {
		t.Fatal("explicit runtime account unfence failed")
	}
	if err := InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, maintenance, secrets); err == nil {
		t.Fatal("unfenced runtime account appeared fenced")
	}
	if err := newClient.PingContext(ctx); err != nil {
		t.Fatal("runtime account did not authenticate after explicit unfence")
	}
}

func TestMySQLRuntimeAccountFenceAdmissionFailsClosed(t *testing.T) {
	resolved := ResolvedBinding{Target: TargetIdentity{Engine: EngineMySQL, BindingID: "runtime", Role: "runtime"}, Purpose: PurposeApplication, CredentialRef: "secret:runtime", Endpoint: DatabaseEndpoint{Host: "127.0.0.1", Port: 3306}}
	valid := mysqlRuntimeAccountFence{FenceUser: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%", DedicatedRuntimeUsername: "runtime"}
	if !validMySQLFence(resolved, valid) {
		t.Fatal("valid dedicated fence admission was rejected")
	}
	for _, fence := range []mysqlRuntimeAccountFence{
		{FenceUser: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%"},
		{FenceUser: "runtime", FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%", DedicatedRuntimeUsername: "runtime"},
		{FenceUser: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:runtime", RuntimeAccountHost: "%", DedicatedRuntimeUsername: "runtime"},
		{FenceUser: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "bad\nhost", DedicatedRuntimeUsername: "runtime"},
		{FenceUser: "fence", FenceCredentialRef: "secret:fence", RuntimeAccountHost: "%", DedicatedRuntimeUsername: "runtime"},
	} {
		if validMySQLFence(resolved, fence) {
			t.Fatal("unsafe runtime account fence admission was accepted")
		}
	}
}
