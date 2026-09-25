package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
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

	"norn/v2/api/database"
)

type mysqlIntentSecrets map[string]string

func (s mysqlIntentSecrets) Resolve(_ context.Context, reference string) ([]byte, error) {
	value, ok := s[reference]
	if !ok {
		return nil, fmt.Errorf("secret unavailable")
	}
	return []byte(value), nil
}

func TestMySQLRestoreIntentPayloadExact(t *testing.T) {
	request := MySQLRestoreRequest{CatalogRevision: 1, ProfileID: "mini", LogicalID: "db"}
	data, _ := json.Marshal(request)
	var payload map[string]interface{}
	_ = json.Unmarshal(data, &payload)
	if !sameMySQLRestorePayload(payload, request) {
		t.Fatal("exact payload rejected")
	}
	payload["extra"] = "unsigned"
	if sameMySQLRestorePayload(payload, request) || decodeMySQLRestorePayload(payload, &request) == nil {
		t.Fatal("extra execution field accepted")
	}
}

// TestMySQLRestoreIntentAgainstDisposableEngines requires a disposable
// PostgreSQL control database and MySQL 8 target. It stages an actual dump,
// signs the exact request, persists the target fence, and demonstrates that
// the external-effect ambiguity boundary cannot be entered twice.
func TestMySQLRestoreIntentAgainstDisposableEngines(t *testing.T) {
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
	sourceDB, targetDB := "intent_src_"+suffix, "intent_dst_"+suffix
	sourceRole, targetRole := "isrc_"+suffix, "idst_"+suffix
	sourcePassword, targetPassword := "source"+suffix, `target\quote"`+suffix
	defer func() {
		for _, name := range []string{sourceDB, targetDB} {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
		}
		for _, name := range []string{sourceRole, targetRole} {
			_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+name+"'@'%'")
		}
	}()
	for _, item := range []struct{ name, role, password string }{{sourceDB, sourceRole, sourcePassword}, {targetDB, targetRole, targetPassword}} {
		for _, statement := range []string{
			"CREATE DATABASE `" + item.name + "`",
			"CREATE USER '" + item.role + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(item.password),
			"GRANT ALL PRIVILEGES ON `" + item.name + "`.* TO '" + item.role + "'@'%'",
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatal("disposable MySQL fixture setup failed")
			}
		}
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+sourceDB+"`.marker (value VARCHAR(64) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+sourceDB+"`.marker VALUES ('signed-intent-source')"); err != nil {
		t.Fatal(err)
	}
	stores, dbs := acceptanceIntegrationStores(t, 1)
	control := dbs[0]
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{
		APIVersion: database.APIVersion, ID: "intent-mysql", Generation: 1, Purpose: database.PurposeApplication,
		Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:intent-mysql",
		Endpoint: database.DatabaseEndpoint{Host: host, Port: port},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}},
	})
	catalog.Bindings = append(catalog.Bindings,
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "intent-source", ServiceID: "intent-mysql", Database: sourceDB, Role: sourceRole, Generation: 1, CredentialRef: "secret:intent/source", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "intent-target", ServiceID: "intent-mysql", Database: targetDB, Role: targetRole, Generation: 1, CredentialRef: "secret:intent/target", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
	)
	catalog.Profiles[0].DatabaseBindings["intent-source"] = "intent-source"
	catalog.Profiles[0].DatabaseBindings["intent-target"] = "intent-target"
	active, err := control.ActivateDatabaseCatalog(ctx, 0, catalog, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "intent-source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "intent-target"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := mysqlIntentSecrets{
		"secret:intent/source": fmt.Sprintf(`{"password":%q}`, sourcePassword),
		"secret:intent/target": fmt.Sprintf(`{"password":%q}`, targetPassword),
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
	path, artifact, err := database.StageMySQLSQLSnapshot(ctx, source, source.Target, secrets, dumpTool, fmt.Sprintf("%x", toolSHA), stage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Clean(stage)+string(os.PathSeparator)) {
		t.Fatal("snapshot escaped private stage")
	}
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "intent-target", Target: target.Target, Artifact: artifact, ArtifactPath: path}
	input := newAcceptance(t, stores[0], "mysql-intent-"+suffix, "operator", "intent-target", false)
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
	claim, err := NewOperationClaim(accepted.Operation.ID, "mysql-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1,
		locked_until=now()+interval '2 minutes' WHERE id=$1`, claim.OperationID(), claim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	prepared, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], claim, request, secrets)
	if err != nil || prepared.State != "prepared" || prepared.Replayed {
		t.Fatalf("first durable prepare: %+v %v", prepared, err)
	}
	replay, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], claim, request, secrets)
	if err != nil || !replay.Replayed {
		t.Fatalf("same operation replay: %+v %v", replay, err)
	}
	staleClaim, err := NewOperationClaim(claim.OperationID(), claim.OwnerID(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], staleClaim, request, secrets); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale claim was accepted: %v", err)
	}
	second := newAcceptance(t, stores[0], "mysql-intent-other-"+suffix, "operator", "intent-target", false)
	second.Identity.Kind, second.Identity.Resource = MySQLRestoreOperationKind, "mysql/"+targetDB
	second.Operation.Kind, second.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	second.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &second.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	second.Fingerprint, err = CanonicalOperationRequestFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	secondAccepted, err := stores[0].Accept(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := NewOperationClaim(secondAccepted.Operation.ID, "mysql-worker-b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1,
		locked_until=now()+interval '2 minutes' WHERE id=$1`, secondClaim.OperationID(), secondClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], secondClaim, request, secrets); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("second operation consumed the same target: %v", err)
	}
	wrong := request
	wrong.Target.BindingGeneration++
	if _, err := control.PrepareClaimedMySQLRestore(ctx, stores[0], claim, wrong, secrets); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("changed signed target was accepted: %v", err)
	}
	if _, err := control.Pool.Exec(ctx, `UPDATE mysql_restore_intents SET artifact_path='/tmp/forged.sql' WHERE operation_id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.BeginClaimedMySQLRestore(ctx, stores[0], claim, secrets); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("tampered durable artifact path was accepted: %v", err)
	}
	if _, err := control.Pool.Exec(ctx, `UPDATE mysql_restore_intents SET artifact_path=$2 WHERE operation_id=$1`, claim.OperationID(), path); err != nil {
		t.Fatal(err)
	}
	restoreBytes, err := os.ReadFile(restoreTool)
	if err != nil {
		t.Fatal(err)
	}
	restoreSHA := sha256.Sum256(restoreBytes)
	runner := MySQLRestoreRunner{Control: control, Acceptance: stores[0], Secrets: secrets,
		Tool: database.MySQLRestoreTool{Path: restoreTool, SHA256: fmt.Sprintf("%x", restoreSHA)}}
	if err := runner.RunClaimed(ctx, claim); err != nil {
		t.Fatalf("supervised MySQL restore: %v", err)
	}
	if _, err := control.BeginClaimedMySQLRestore(ctx, stores[0], claim, secrets); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("ambiguous SQL retry was accepted: %v", err)
	}
	var state string
	if err := control.Pool.QueryRow(ctx, `SELECT state FROM mysql_restore_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("durable state = %q, %v", state, err)
	}
	var marker string
	if err := admin.QueryRowContext(ctx, "SELECT value FROM `"+targetDB+"`.marker").Scan(&marker); err != nil || marker != "signed-intent-source" {
		t.Fatalf("restored marker = %q, %v", marker, err)
	}
}

func mysqlRestoreSQLLiteral(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value) + "'"
}
