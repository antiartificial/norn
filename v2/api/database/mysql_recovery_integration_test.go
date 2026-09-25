package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// TestMySQLExactTargetDumpRestore is an opt-in, disposable rehearsal of the
// MySQL client tools and resolver identity. It is deliberately not a recovery
// adapter: no production snapshot/restore capability is enabled by this test.
func TestMySQLExactTargetDumpRestore(t *testing.T) {
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	if adminDSN == "" {
		t.Skip("NORN_TEST_MYSQL_DSN is not set")
	}
	for _, tool := range []string{"mysqldump", "mysql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid disposable MySQL port")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	sourceDB, targetDB := "norn_src_"+suffix, "norn_dst_"+suffix
	sourceRole, targetRole := "src_"+suffix, "dst_"+suffix
	sourcePassword, targetPassword := "source"+suffix, "target"+suffix
	// Cleanup only the unique fixture names, including when preparation fails.
	defer func() {
		for _, name := range []string{sourceDB, targetDB} {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
		}
		for _, name := range []string{sourceRole, targetRole} {
			_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+name+"'@'%'")
		}
	}()
	for _, item := range []struct{ db, role, password string }{{sourceDB, sourceRole, sourcePassword}, {targetDB, targetRole, targetPassword}} {
		for _, statement := range []string{
			"CREATE DATABASE `" + item.db + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
			"CREATE USER '" + item.role + "'@'%' IDENTIFIED BY '" + item.password + "'",
			"GRANT ALL PRIVILEGES ON `" + item.db + "`.* TO '" + item.role + "'@'%'",
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatal("prepare disposable MySQL recovery fixture failed")
			}
		}
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+sourceDB+"`.marker (value VARCHAR(80) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+sourceDB+"`.marker VALUES ('source-only-recovery-marker')"); err != nil {
		t.Fatal(err)
	}

	catalog := testCatalog()
	catalog.Services[4].Endpoint = DatabaseEndpoint{Host: host, Port: port}
	catalog.Services[4].EngineVersion = "8.4"
	catalog.Bindings[4].Database, catalog.Bindings[4].Role = sourceDB, sourceRole
	catalog.Bindings[4].CredentialRef = "secret:recovery/source"
	catalog.Bindings = append(catalog.Bindings, DatabaseBinding{APIVersion: APIVersion, ID: "recovery-target", ServiceID: "wp-mysql", Database: targetDB, Role: targetRole, Generation: 2, CredentialRef: "secret:recovery/target", TLS: DatabaseTLS{Mode: TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["restore-db"] = "recovery-target"
	resolver := mustResolver(t, catalog)
	secrets := literalSecrets{
		"secret:recovery/source": fmt.Sprintf(`{"password":%q}`, sourcePassword),
		"secret:recovery/target": fmt.Sprintf(`{"password":%q}`, targetPassword),
	}
	resolve := func(logical string, expected *TargetIdentity) ResolvedBinding {
		t.Helper()
		result, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: logical, Expected: expected})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	source, target := resolve("wordpress-db", nil), resolve("restore-db", nil)
	if source.Target == target.Target {
		t.Fatal("source and target identities are equal")
	}
	stale := target.Target
	stale.BindingGeneration--
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "restore-db", Expected: &stale})
	var typed *ResolverError
	if !errors.As(err, &typed) || typed.Code != CodeStaleTarget {
		t.Fatalf("stale restore identity was accepted: %v", err)
	}
	for _, capability := range []Capability{CapabilitySnapshot, CapabilityRestore} {
		_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "restore-db", Expected: &target.Target, RequiredCapabilities: []Capability{capability}})
		var typed *ResolverError
		if !errors.As(err, &typed) || typed.Code != CodeUnsupportedCapability {
			t.Fatalf("unimplemented MySQL %s capability became active: %v", capability, err)
		}
	}
	// Re-resolve with exact identities immediately before the scoped tool
	// actions. Production restore still needs a durable lifecycle fence.
	source = resolve("wordpress-db", &source.Target)
	target = resolve("restore-db", &target.Target)
	sourceSession, err := OpenSession(ctx, source, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceSession.Close()
	targetSession, err := OpenSession(ctx, target, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer targetSession.Close()
	for _, session := range []*Session{sourceSession, targetSession} {
		if _, err := session.Probe(ctx); err != nil {
			t.Fatal(err)
		}
	}
	options := func(role, password string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mysql.cnf")
		// Test credentials are alphanumeric; they never appear in argv or logs.
		if err := os.WriteFile(path, []byte("[client]\nuser="+role+"\npassword="+password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sourceOptions, targetOptions := options(sourceRole, sourcePassword), options(targetRole, targetPassword)
	dumpPath := filepath.Join(t.TempDir(), "source.sql")
	dump, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	dumpCommand := exec.CommandContext(ctx, "mysqldump", "--defaults-file="+sourceOptions, "--protocol=tcp", "--host="+host, "--port="+portText, "--single-transaction", "--quick", "--no-tablespaces", "--set-gtid-purged=OFF", sourceDB)
	dumpCommand.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	dumpCommand.Stdout = dump
	var dumpErrors bytes.Buffer
	dumpCommand.Stderr = &dumpErrors
	if err := dumpCommand.Run(); err != nil {
		_ = dump.Close()
		t.Fatalf("source dump failed: %s", sourceSession.Redact(dumpErrors.Bytes()))
	}
	if err := dump.Close(); err != nil {
		t.Fatal(err)
	}
	dumpBytes, err := os.ReadFile(dumpPath)
	if err != nil || len(dumpBytes) == 0 || !strings.Contains(string(dumpBytes), "source-only-recovery-marker") {
		t.Fatal("source dump did not contain the expected marker")
	}
	checksum := sha256.Sum256(dumpBytes)
	input, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	restoreCommand := exec.CommandContext(ctx, "mysql", "--defaults-file="+targetOptions, "--protocol=tcp", "--host="+host, "--port="+portText, "--database="+targetDB)
	restoreCommand.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	restoreCommand.Stdin = input
	if output, err := restoreCommand.CombinedOutput(); err != nil {
		t.Fatalf("target restore failed: %s", targetSession.Redact(output))
	}
	// Verify both ends through the admin connection. The target marker must be
	// present and the source marker must remain untouched.
	for _, db := range []string{sourceDB, targetDB} {
		var marker string
		if err := admin.QueryRowContext(ctx, "SELECT value FROM `"+db+"`.marker").Scan(&marker); err != nil || marker != "source-only-recovery-marker" {
			t.Fatalf("%s marker = %q, %v", db, marker, err)
		}
	}
	t.Logf("disposable MySQL exact-target dump/restore passed: sha256=%x", checksum)
}
