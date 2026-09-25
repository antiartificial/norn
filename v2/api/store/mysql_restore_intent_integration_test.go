package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/internal/s3emulator"
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
	request := MySQLRestoreRequest{CatalogRevision: 1, ProfileID: "mini", LogicalID: "db", SourceArtifact: MySQLRestoreSourceArtifact{OperationID: "source-op", ReceiptSHA256: strings.Repeat("a", 64)}}
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
	data, _ = json.Marshal(request)
	_ = json.Unmarshal(data, &payload)
	receipt := payload["sourceArtifact"].(map[string]interface{})
	receipt["receiptSha256"] = strings.Repeat("b", 64)
	if sameMySQLRestorePayload(payload, request) {
		t.Fatal("altered source artifact receipt was accepted")
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
	sourceDB, targetDB, lostTargetDB := "intent_src_"+suffix, "intent_dst_"+suffix, "intent_lost_"+suffix
	sourceRole, targetRole, lostTargetRole := "isrc_"+suffix, "idst_"+suffix, "ilst_"+suffix
	restoreRole, fenceRole := "irst_"+suffix, "ifnc_"+suffix
	sourcePassword, targetPassword, restorePassword, fencePassword := "source"+suffix, `target\quote"`+suffix, "restore"+suffix, "fence"+suffix
	defer func() {
		for _, name := range []string{sourceDB, targetDB, lostTargetDB} {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
		}
		for _, name := range []string{sourceRole, targetRole, lostTargetRole, restoreRole, fenceRole} {
			_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+name+"'@'%'")
		}
	}()
	for _, item := range []struct{ name, role, password string }{{sourceDB, sourceRole, sourcePassword}, {targetDB, targetRole, targetPassword}, {lostTargetDB, lostTargetRole, targetPassword}} {
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
	for _, statement := range []string{
		"CREATE USER '" + restoreRole + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(restorePassword),
		"CREATE USER '" + fenceRole + "'@'%' IDENTIFIED BY " + mysqlRestoreSQLLiteral(fencePassword),
		"GRANT ALL PRIVILEGES ON `" + targetDB + "`.* TO '" + restoreRole + "'@'%'",
		"GRANT ALL PRIVILEGES ON `" + lostTargetDB + "`.* TO '" + restoreRole + "'@'%'",
		"GRANT CREATE USER, PROCESS, CONNECTION_ADMIN ON *.* TO '" + fenceRole + "'@'%'",
		"GRANT SELECT ON mysql.user TO '" + fenceRole + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal("disposable MySQL maintenance fixture setup failed")
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
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", RestoreRole: restoreRole, RestoreAccountHost: "%", RestoreCredentialRef: "secret:intent/restore", FenceRole: fenceRole, FenceCredentialRef: "secret:intent/fence", FenceAccountHost: "%"}
	catalog.Bindings = append(catalog.Bindings,
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "intent-source", ServiceID: "intent-mysql", Database: sourceDB, Role: sourceRole, Generation: 1, CredentialRef: "secret:intent/source", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "intent-target", ServiceID: "intent-mysql", Database: targetDB, Role: targetRole, Generation: 1, CredentialRef: "secret:intent/target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "intent-lost-target", ServiceID: "intent-mysql", Database: lostTargetDB, Role: lostTargetRole, Generation: 1, CredentialRef: "secret:intent/target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
	)
	catalog.Profiles[0].DatabaseBindings["intent-source"] = "intent-source"
	catalog.Profiles[0].DatabaseBindings["intent-target"] = "intent-target"
	catalog.Profiles[0].DatabaseBindings["intent-lost-target"] = "intent-lost-target"
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
	lostTarget, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "intent-lost-target"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := mysqlIntentSecrets{
		"secret:intent/source":  fmt.Sprintf(`{"password":%q}`, sourcePassword),
		"secret:intent/target":  fmt.Sprintf(`{"password":%q}`, targetPassword),
		"secret:intent/restore": fmt.Sprintf(`{"password":%q}`, restorePassword),
		"secret:intent/fence":   fmt.Sprintf(`{"password":%q}`, fencePassword),
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
	receipt := testMySQLSourceArtifactReceipt(t, control, stores[0], active.Revision, artifact.Source, path, artifact)
	testBindMySQLSourceFence(t, control, receipt)
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "intent-target", Target: target.Target, Maintenance: *target.MySQLMaintenance, Artifact: artifact, ArtifactPath: path,
		SourceArtifact: receipt}
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
	if _, err := control.BeginClaimedMySQLRestore(ctx, stores[0], claim, secrets); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("Begin crossed the SQL boundary without a verified runtime account lock: %v", err)
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
	// Publish the source under its signed retention receipt, then remove the
	// staging file. The SQL runner below must consume the retained bytes.
	_, objectServer := s3emulator.Start("norn-artifacts", "artifact-writer")
	defer objectServer.Close()
	objectEndpoint, err := url.Parse(objectServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	publicationSpool := t.TempDir()
	if err := os.Chmod(publicationSpool, 0o700); err != nil {
		t.Fatal(err)
	}
	objectConfig := artifactstore.S3Config{Endpoint: objectEndpoint.Host, Bucket: "norn-artifacts", Prefix: "mysql/recovery", Region: "us-east-1",
		AccessKey: "artifact-writer", SecretKey: "test-secret", Transport: objectServer.Client().Transport,
		SpoolDirectory: publicationSpool, SpoolCapacity: 64 << 20, RetainFor: 24 * time.Hour}
	retainedObjects, err := artifactstore.OpenS3(ctx, objectConfig)
	if err != nil {
		t.Fatal(err)
	}
	recoverySpool := t.TempDir()
	if err := os.Chmod(recoverySpool, 0o700); err != nil {
		t.Fatal(err)
	}
	objectConfig.SpoolDirectory = recoverySpool
	recoveryObjects, err := artifactstore.OpenS3(ctx, objectConfig)
	if err != nil {
		t.Fatal(err)
	}
	stagedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceClaim, err := NewOperationClaim(receipt.OperationID, "source-retain", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Pool.Exec(ctx, `UPDATE operations SET status='running',attempts=1,locked_by=$2,lock_generation=1,
		locked_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, sourceClaim.OperationID(), sourceClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RetainClaimedMySQLSourceArtifact(ctx, stores[0], sourceClaim, retainedObjects); err != nil {
		t.Fatalf("retain signed source: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	materializeDirectory := filepath.Join(t.TempDir(), "materialized")
	if err := os.Mkdir(materializeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if replay, err := control.PrepareClaimedMySQLRestoreFromRetained(ctx, stores[0], claim, request, secrets, recoveryObjects, materializeDirectory); err != nil || !replay.Replayed {
		t.Fatalf("retained prepare without staged file: %+v %v", replay, err)
	}
	// The short lease expires while mysql is deliberately delayed. Completion
	// therefore proves the private runner renewed its claim during the import.
	delayedTool := filepath.Join(t.TempDir(), "mysql-delayed")
	quotedTool := "'" + strings.ReplaceAll(restoreTool, "'", "'\\''") + "'"
	if err := os.WriteFile(delayedTool, []byte("#!/bin/sh\nsleep 0.35\nexec "+quotedTool+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	restoreBytes, err := os.ReadFile(delayedTool)
	if err != nil {
		t.Fatal(err)
	}
	restoreSHA := sha256.Sum256(restoreBytes)
	// Lock runtime access before replay and Begin. Every restore preflight,
	// import, and expectation check must use the separate restore identity.
	if _, err := admin.ExecContext(ctx, "ALTER USER '"+targetRole+"'@'%' ACCOUNT LOCK"); err != nil {
		t.Fatal("lock runtime account after preflight")
	}
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "ALTER USER '"+targetRole+"'@'%' ACCOUNT UNLOCK")
	}()
	lockedSession, err := database.OpenSession(ctx, target, secrets)
	if err == nil {
		defer lockedSession.Close()
		_, err = lockedSession.Probe(ctx)
	}
	if err == nil {
		t.Fatal("locked runtime account remained usable; restore credential split was not exercised")
	}
	if replay, err := control.PrepareClaimedMySQLRestoreFromRetained(ctx, stores[0], claim, request, secrets, recoveryObjects, materializeDirectory); err != nil || !replay.Replayed {
		t.Fatalf("prepare replay with locked runtime account: %+v %v", replay, err)
	}
	runner := MySQLRestoreRunner{Control: control, Acceptance: stores[0], Secrets: secrets, ClaimLease: 120 * time.Millisecond,
		Objects: recoveryObjects, MaterializeDirectory: materializeDirectory,
		Tool: database.MySQLRestoreTool{Path: delayedTool, SHA256: fmt.Sprintf("%x", restoreSHA)}}
	if err := runner.RunClaimed(ctx, claim); err != nil {
		t.Fatalf("supervised MySQL restore: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore recreated or read staged path: %v", err)
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
	var mutationFence RuntimeMutationFence
	var fenceActive bool
	if err := control.Pool.QueryRow(ctx, `SELECT epoch, owner, active FROM runtime_mutation_fence WHERE singleton=true`).Scan(&mutationFence.Epoch, &mutationFence.Owner, &fenceActive); err != nil || !fenceActive || mutationFence.Owner != "mysql-restore:"+claim.OperationID() {
		t.Fatalf("restore did not retain its runtime mutation fence: %+v active=%t err=%v", mutationFence, fenceActive, err)
	}
	if ready, err := control.AssessCompletedMySQLRestoreLiveRecovery(ctx, stores[0], claim.OperationID(), secrets); err != nil || ready.Fence.Epoch != mutationFence.Epoch {
		t.Fatalf("live completed restore assessment: %+v %v", ready, err)
	}
	if _, err := admin.ExecContext(ctx, "UPDATE `"+targetDB+"`.marker SET value='drifted'"); err != nil {
		t.Fatal(err)
	}
	if _, err := control.AssessCompletedMySQLRestoreLiveRecovery(ctx, stores[0], claim.OperationID(), secrets); err == nil {
		t.Fatal("live recovery assessment accepted changed target contents")
	}
	if _, err := admin.ExecContext(ctx, "UPDATE `"+targetDB+"`.marker SET value='signed-intent-source'"); err != nil {
		t.Fatal(err)
	}
	insertOperationFixture(t, control, "app.deploy", 1, nil)
	if claimed, _, err := control.ClaimNextOperation(ctx, "concurrent-deploy", time.Minute, []string{"app.deploy"}); err != nil || claimed != nil {
		t.Fatalf("restore admitted queued deploy while runtime fence held: %+v %v", claimed, err)
	}
	if err := control.ReleaseRuntimeMutationFence(ctx, mutationFence); !errors.Is(err, ErrRuntimeMutationFenceOwnershipLost) {
		t.Fatalf("generic release bypassed transferred source/restore fence: %v", err)
	}
	if claimed, _, err := control.ClaimNextOperation(ctx, "post-restore-deploy", time.Minute, []string{"app.deploy"}); err != nil || claimed != nil {
		t.Fatalf("completed restore resumed queued deploy without signed recovery: %+v %v", claimed, err)
	}
	// The later process-kill case uses a separate staged-source fixture.
	if err := os.WriteFile(path, stagedBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Claim theft after the durable intent enters executing must cancel the
	// private client, leave no success receipt, contain the intent for operator
	// inspection, and reject any automatic replay under the successor claim.
	lossStores, lossDBs := acceptanceIntegrationStores(t, 1)
	lossControl := lossDBs[0]
	lossCatalog, err := lossControl.ActivateDatabaseCatalog(ctx, 0, catalog, "test-operator-loss")
	if err != nil {
		t.Fatal(err)
	}
	lostRequest := request
	lostRequest.CatalogRevision = lossCatalog.Revision
	lostRequest.SourceArtifact = testMySQLSourceArtifactReceipt(t, lossControl, lossStores[0], lossCatalog.Revision, artifact.Source, path, artifact)
	testBindMySQLSourceFence(t, lossControl, lostRequest.SourceArtifact)
	lostRequest.LogicalID, lostRequest.Target = "intent-lost-target", lostTarget.Target
	lostEncoded, _ := json.Marshal(lostRequest)
	lostInput := newAcceptance(t, lossStores[0], "mysql-intent-claim-loss-"+suffix, "operator", "intent-lost-target", false)
	lostInput.Identity.Kind, lostInput.Identity.Resource = MySQLRestoreOperationKind, "mysql/"+lostTargetDB
	lostInput.Operation.Kind, lostInput.Operation.MaxAttempts, lostInput.Operation.Payload = MySQLRestoreOperationKind, 1, nil
	if err := json.Unmarshal(lostEncoded, &lostInput.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	lostInput.Fingerprint, err = CanonicalOperationRequestFingerprint(lostInput)
	if err != nil {
		t.Fatal(err)
	}
	lostAccepted, err := lossStores[0].Accept(ctx, lostInput)
	if err != nil {
		t.Fatal(err)
	}
	lostClaim, err := NewOperationClaim(lostAccepted.Operation.ID, "mysql-worker-loss", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lossControl.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1,
		locked_until=now()+interval '2 minutes' WHERE id=$1`, lostClaim.OperationID(), lostClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	if prepared, err := lossControl.PrepareClaimedMySQLRestore(ctx, lossStores[0], lostClaim, lostRequest, secrets); err != nil || prepared.State != "prepared" {
		t.Fatalf("claim-loss prepare: %+v %v", prepared, err)
	}
	clientStarted := filepath.Join(t.TempDir(), "mysql-client-started")
	lossTool := filepath.Join(t.TempDir(), "mysql-delayed-claim-loss")
	if err := os.WriteFile(lossTool, []byte("#!/bin/sh\nprintf started > '"+clientStarted+"'\nsleep 2\nexec "+quotedTool+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	lossBytes, err := os.ReadFile(lossTool)
	if err != nil {
		t.Fatal(err)
	}
	lossSHA := sha256.Sum256(lossBytes)
	lossRunner := MySQLRestoreRunner{Control: lossControl, Acceptance: lossStores[0], Secrets: secrets, ClaimLease: 90 * time.Millisecond,
		Tool: database.MySQLRestoreTool{Path: lossTool, SHA256: fmt.Sprintf("%x", lossSHA)}}
	runResult := make(chan error, 1)
	go func() { runResult <- lossRunner.RunClaimed(ctx, lostClaim) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(clientStarted); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delayed private MySQL client did not start after intent entered executing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := lossControl.Pool.Exec(ctx, `UPDATE operations SET locked_by='mysql-claim-thief', lock_generation=2,
		locked_until=now()+interval '2 minutes' WHERE id=$1`, lostClaim.OperationID()); err != nil {
		t.Fatal(err)
	}
	cancelledAt := time.Now()
	select {
	case err := <-runResult:
		if err == nil {
			t.Fatal("claim loss allowed delayed private MySQL client to succeed")
		}
		if elapsed := time.Since(cancelledAt); elapsed >= time.Second {
			t.Fatalf("claim loss returned after delayed MySQL wrapper could have continued: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("claim loss did not cancel delayed private MySQL client")
	}
	var lostState, operationStatus string
	var finishedAt interface{}
	if err := lossControl.Pool.QueryRow(ctx, `SELECT i.state, o.status, o.finished_at FROM mysql_restore_intents i JOIN operations o ON o.id=i.operation_id WHERE i.operation_id=$1`, lostClaim.OperationID()).Scan(&lostState, &operationStatus, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if lostState != "needs-inspection" || operationStatus == "succeeded" || finishedAt != nil {
		t.Fatalf("claim-loss containment state=%q operation=%q finished=%v", lostState, operationStatus, finishedAt)
	}
	thiefClaim, err := NewOperationClaim(lostClaim.OperationID(), "mysql-claim-thief", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := lossRunner.RunClaimed(ctx, thiefClaim); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("successor replay after inspection containment = %v", err)
	}
	if err := lossControl.Pool.QueryRow(ctx, `SELECT state FROM mysql_restore_intents WHERE operation_id=$1`, lostClaim.OperationID()).Scan(&lostState); err != nil || lostState != "needs-inspection" {
		t.Fatalf("automatic replay changed contained state=%q err=%v", lostState, err)
	}
}

func mysqlRestoreSQLLiteral(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value) + "'"
}

type MySQLRestoreSourceQuiescence struct {
	Source         database.TargetIdentity `json:"source"`
	ObservedAt     time.Time               `json:"observedAt"`
	Method         string                  `json:"method"`
	EvidenceSHA256 string                  `json:"evidenceSha256"`
}

func mysqlRestoreQuiescence(source database.TargetIdentity) MySQLRestoreSourceQuiescence {
	return MySQLRestoreSourceQuiescence{Source: source, ObservedAt: time.Now().UTC(), Method: "legacy fixture only", EvidenceSHA256: strings.Repeat("a", 64)}
}
