package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
)

// TestMySQLRestoreProcessKillWorker is run in a separate test process by the
// qualification below. Keeping the real runner in that process makes SIGKILL
// exercise the same durable boundary that an API-worker crash would cross.
func TestMySQLRestoreProcessKillWorker(t *testing.T) {
	if os.Getenv("NORN_MYSQL_RESTORE_KILL_HELPER") != "1" {
		return
	}
	config, err := pgxpool.ParseConfig(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NORN_MYSQL_RESTORE_KILL_SCHEMA")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &DB{Pool: pool}
	signer, err := NewHMACAcceptanceSigner(acceptanceTestKey)
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := NewPGOperationStore(db, signer, AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	op, err := db.GetOperation(context.Background(), os.Getenv("NORN_MYSQL_RESTORE_KILL_OPERATION"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := NewOperationClaim(op.ID, os.Getenv("NORN_MYSQL_RESTORE_KILL_OWNER"), 1)
	if err != nil {
		t.Fatal(err)
	}
	runner := MySQLRestoreRunner{
		Control: db, Acceptance: acceptance,
		Secrets: mysqlIntentSecrets{"secret:kill/source": os.Getenv("NORN_MYSQL_RESTORE_KILL_SOURCE_SECRET"), "secret:kill/target": os.Getenv("NORN_MYSQL_RESTORE_KILL_TARGET_SECRET"),
			"secret:kill/restore": os.Getenv("NORN_MYSQL_RESTORE_KILL_RESTORE_SECRET"), "secret:kill/fence": os.Getenv("NORN_MYSQL_RESTORE_KILL_FENCE_SECRET")},
		Tool:       database.MySQLRestoreTool{Path: os.Getenv("NORN_MYSQL_RESTORE_KILL_TOOL"), SHA256: os.Getenv("NORN_MYSQL_RESTORE_KILL_TOOL_SHA256")},
		ClaimLease: 30 * time.Second,
	}
	if err := runner.RunClaimed(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
}

// TestMySQLRestoreProcessKillAfterExecutingQualification requires disposable
// PostgreSQL and MySQL. It kills a separate OS worker process after the
// durable intent is executing and a target table proves that mysql has begun
// consuming SQL. A fresh control-store instance then performs restart
// recovery and proves the operation cannot replay automatically.
func TestMySQLRestoreProcessKillAfterExecutingQualification(t *testing.T) {
	if os.Getenv("NORN_TEST_DATABASE_URL") == "" || os.Getenv("NORN_TEST_MYSQL_DSN") == "" {
		t.Skip("disposable PostgreSQL and MySQL DSNs are required")
	}
	dumpTool, err := exec.LookPath("mysqldump")
	if err != nil {
		t.Skip("mysqldump is unavailable")
	}
	restoreTool, err := exec.LookPath("mysql")
	if err != nil {
		t.Skip("mysql is unavailable")
	}
	config, err := mysql.ParseDSN(os.Getenv("NORN_TEST_MYSQL_DSN"))
	if err != nil || config.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP DSN")
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	admin := sql.OpenDB(connector)
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(config.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	sourceDB, targetDB := "kill_src_"+suffix, "kill_dst_"+suffix
	sourceRole, targetRole := "ksrc_"+suffix, "kdst_"+suffix
	restoreRole, fenceRole := "krst_"+suffix, "kfnc_"+suffix
	sourcePassword, targetPassword, restorePassword, fencePassword := "source"+suffix, "target"+suffix, "restore"+suffix, "fence"+suffix
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+sourceDB+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+targetDB+"`")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+sourceRole+"'@'%'")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+targetRole+"'@'%'")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+restoreRole+"'@'%'")
		_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+fenceRole+"'@'%'")
	}()
	for _, item := range []struct{ name, role, password string }{{sourceDB, sourceRole, sourcePassword}, {targetDB, targetRole, targetPassword}} {
		for _, statement := range []string{"CREATE DATABASE `" + item.name + "`", "CREATE USER '" + item.role + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(item.password), "GRANT ALL PRIVILEGES ON `" + item.name + "`.* TO '" + item.role + "'@'%'"} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{"CREATE USER '" + restoreRole + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(restorePassword), "CREATE USER '" + fenceRole + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(fencePassword), "GRANT ALL PRIVILEGES ON `" + targetDB + "`.* TO '" + restoreRole + "'@'%'"} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+sourceDB+"`.prefix (value VARCHAR(64) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+sourceDB+"`.prefix VALUES ('durable-prefix')"); err != nil {
		t.Fatal(err)
	}

	stores, dbs := acceptanceIntegrationStores(t, 2)
	control, recovered := dbs[0], dbs[1]
	var schema string
	if err := control.Pool.QueryRow(ctx, "SHOW search_path").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{APIVersion: database.APIVersion, ID: "kill-mysql", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:kill-mysql", Endpoint: database.DatabaseEndpoint{Host: host, Port: port}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost}, TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}}})
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", RestoreRole: restoreRole, RestoreCredentialRef: "secret:kill/restore", FenceRole: fenceRole, FenceCredentialRef: "secret:kill/fence", FenceAccountHost: "%"}
	catalog.Bindings = append(catalog.Bindings,
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "kill-source", ServiceID: "kill-mysql", Database: sourceDB, Role: sourceRole, Generation: 1, CredentialRef: "secret:kill/source", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "kill-target", ServiceID: "kill-mysql", Database: targetDB, Role: targetRole, Generation: 1, CredentialRef: "secret:kill/target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
	)
	catalog.Profiles[0].DatabaseBindings["kill-source"] = "kill-source"
	catalog.Profiles[0].DatabaseBindings["kill-target"] = "kill-target"
	active, err := control.ActivateDatabaseCatalog(ctx, 0, catalog, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "kill-source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "kill-target"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := mysqlIntentSecrets{"secret:kill/source": fmt.Sprintf(`{"password":%q}`, sourcePassword), "secret:kill/target": fmt.Sprintf(`{"password":%q}`, targetPassword), "secret:kill/restore": fmt.Sprintf(`{"password":%q}`, restorePassword), "secret:kill/fence": fmt.Sprintf(`{"password":%q}`, fencePassword)}
	dumpBytes, err := os.ReadFile(dumpTool)
	if err != nil {
		t.Fatal(err)
	}
	dumpSHA := sha256.Sum256(dumpBytes)
	stage := t.TempDir()
	if err := os.Chmod(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	path, artifact, err := database.StageMySQLSQLSnapshot(ctx, source, source.Target, secrets, dumpTool, fmt.Sprintf("%x", dumpSHA), stage)
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "kill-target", Target: target.Target, Maintenance: *target.MySQLMaintenance, Artifact: artifact, ArtifactPath: path, SourceQuiescence: mysqlRestoreQuiescence(source.Target)}
	input := newAcceptance(t, stores[0], "mysql-process-kill-"+suffix, "operator", "kill-target", false)
	input.Identity.Kind, input.Identity.Resource = MySQLRestoreOperationKind, "mysql/"+targetDB
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encoded, _ := json.Marshal(request)
	input.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	owner := "mysql-process-kill-worker"
	if _, err := control.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1, locked_until=now()+interval '2 minutes' WHERE id=$1`, accepted.Operation.ID, owner); err != nil {
		t.Fatal(err)
	}
	claim, err := NewOperationClaim(accepted.Operation.ID, owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if prepared, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], claim, request, secrets); err != nil || prepared.State != "prepared" {
		t.Fatalf("prepare private restore before worker launch: %+v %v", prepared, err)
	}
	marker := filepath.Join(t.TempDir(), "mysql-client-started")
	tool := filepath.Join(t.TempDir(), "mysql-process-kill-tool")
	quoted := "'" + strings.ReplaceAll(restoreTool, "'", "'\\''") + "'"
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf started > '"+marker+"'\nexec "+quoted+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	toolBytes, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	toolSHA := sha256.Sum256(toolBytes)
	worker := exec.Command(os.Args[0], "-test.run=^TestMySQLRestoreProcessKillWorker$", "-test.v")
	var workerOutput bytes.Buffer
	worker.Stdout, worker.Stderr = &workerOutput, &workerOutput
	worker.Env = append(os.Environ(),
		"NORN_MYSQL_RESTORE_KILL_HELPER=1", "NORN_MYSQL_RESTORE_KILL_SCHEMA="+schema,
		"NORN_MYSQL_RESTORE_KILL_OPERATION="+accepted.Operation.ID, "NORN_MYSQL_RESTORE_KILL_OWNER="+owner,
		"NORN_MYSQL_RESTORE_KILL_SOURCE_SECRET="+secrets["secret:kill/source"], "NORN_MYSQL_RESTORE_KILL_TARGET_SECRET="+secrets["secret:kill/target"], "NORN_MYSQL_RESTORE_KILL_RESTORE_SECRET="+secrets["secret:kill/restore"], "NORN_MYSQL_RESTORE_KILL_FENCE_SECRET="+secrets["secret:kill/fence"],
		"NORN_MYSQL_RESTORE_KILL_TOOL="+tool, "NORN_MYSQL_RESTORE_KILL_TOOL_SHA256="+fmt.Sprintf("%x", toolSHA),
	)
	worker.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var tables int
		_ = admin.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=? AND table_name='prefix'`, targetDB).Scan(&tables)
		if _, err := os.Stat(marker); err == nil && tables == 1 {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-worker.Process.Pid, syscall.SIGKILL)
			_ = worker.Wait()
			var state string
			_ = control.Pool.QueryRow(ctx, `SELECT state FROM mysql_restore_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&state)
			t.Fatalf("mysql did not begin the signed SQL stream after the executing boundary: state=%q worker=%s", state, workerOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Kill the worker and its mysql child as an OS process group. The durable
	// executing record and the created prefix table are independent evidence of
	// the crash point; no in-process cancellation or simulated claim theft is used.
	if err := syscall.Kill(-worker.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err == nil {
		t.Fatal("SIGKILL worker unexpectedly exited successfully")
	}
	if _, err := recovered.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := recovered.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	inspection, err := stores[1].InspectMySQLRestore(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.IntentState != "needs-inspection" || inspection.OperationStatus != "failed" || !inspection.ManualRecovery || inspection.Signature.Value == "" || inspection.CanonicalDigest == "" {
		t.Fatalf("restart recovery did not retain signed inspection evidence: %+v", inspection)
	}
	serialized, _ := json.Marshal(inspection)
	if containsAny(string(serialized), path, `"artifactPath"`, `"profileId"`, `"logicalId"`) {
		t.Fatalf("inspection disclosed private execution material: %s", serialized)
	}
	var attempts int
	if err := recovered.Pool.QueryRow(ctx, `SELECT attempts FROM operations WHERE id=$1`, accepted.Operation.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("crashed restore was automatically retried: attempts=%d err=%v", attempts, err)
	}
	claim, err = NewOperationClaim(accepted.Operation.ID, "recovery-worker", 2)
	if err != nil {
		t.Fatal(err)
	}
	runner := MySQLRestoreRunner{Control: recovered, Acceptance: stores[1], Secrets: secrets, Tool: database.MySQLRestoreTool{Path: tool, SHA256: fmt.Sprintf("%x", toolSHA)}}
	if err := runner.RunClaimed(ctx, claim); err == nil {
		t.Fatal("automatic SQL replay after process kill unexpectedly succeeded")
	}
}
