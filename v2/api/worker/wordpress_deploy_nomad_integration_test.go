package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// TestClaimedWordPressVerifiedTLSDeployInNomad qualifies the production-shaped
// path that matters for the supported WordPress database adapter. It accepts
// a signed app.deploy into PostgreSQL, lets the normal claimed worker resolve
// a clean immutable Git source and exact prebuilt image, opens the declared
// declared MySQL verify-full target, renders its CA through a private Nomad
// Variable, and starts the generated WordPress allocation.
//
// The test intentionally takes disposable runtime addresses as input rather
// than starting a database or scheduler itself. The invoking fixture owns
// those processes and supplies a Nomad agent with the declared host volume.
// Set all NORN_TEST_WORDPRESS_DEPLOY_* variables named below, plus
// NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL. Values must describe only
// loopback disposable services. The test deregisters its uniquely named job,
// deletes its Nomad Variable, and drops its PostgreSQL schema.
func TestClaimedWordPressVerifiedTLSDeployInNomad(t *testing.T) {
	address, controlURL := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DATABASE_URL")
	host, serverName := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_HOST"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_SERVER_NAME")
	user, password, databaseName := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_USER"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_DATABASE")
	volume := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_CONTENT_VOLUME")
	quiesceSource := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_SOURCE_QUIESCE") == "1"
	snapshotPassword, fencePassword := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_SNAPSHOT_PASSWORD"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_FENCE_PASSWORD")
	port, portErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PORT"))
	ca, goodCA := deployQualificationPEM(t, "NORN_TEST_WORDPRESS_DEPLOY_MYSQL_CA_PEM_B64")
	wrongCA, badCA := deployQualificationPEM(t, "NORN_TEST_WORDPRESS_DEPLOY_MYSQL_WRONG_CA_PEM_B64")
	if address == "" || controlURL == "" || host == "" || serverName == "" || host != serverName || user == "" || password == "" || databaseName == "" || volume == "" || !goodCA || !badCA || portErr != nil || port < 1 || port > 65535 || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable Nomad/PostgreSQL/MySQL TLS variables and NORN_TEST_NOMAD_DOCKER=1 for claimed WordPress deploy qualification")
	}
	if quiesceSource && (snapshotPassword == "" || fencePassword == "") {
		t.Skip("set disposable snapshot and fence account passwords for source quiescence qualification")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		t.Fatal("qualification requires a loopback Nomad HTTP endpoint")
	}
	db := wordpressDeployControlDB(t, controlURL)
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}

	app := "norn-wp-deploy-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	appsDir, spec := wordpressDeploySource(t, app, volume)
	secretRoot := t.TempDir()
	if err := os.Chmod(secretRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeDeploySecret(t, secretRoot, "wp/password", []byte(fmt.Sprintf(`{"password":%q}`, password)))
	writeDeploySecret(t, secretRoot, "wp/ca", ca)
	writeDeploySecret(t, secretRoot, "wp/wrong-ca", wrongCA)
	if quiesceSource {
		writeDeploySecret(t, secretRoot, "wp/snapshot", []byte(fmt.Sprintf(`{"password":%q}`, snapshotPassword)))
		writeDeploySecret(t, secretRoot, "wp/fence", []byte(fmt.Sprintf(`{"password":%q}`, fencePassword)))
	}
	secrets, err := database.NewDirectorySecretSource(secretRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })

	catalog := wordpressDeployCatalog(host, port, user, databaseName, "secret:wp/ca")
	if engineVersion := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_ENGINE_VERSION"); engineVersion != "" {
		catalog.Services[0].EngineVersion = engineVersion
	}
	if _, err := db.ActivateDatabaseCatalog(context.Background(), 0, catalog, "wordpress-deploy-qualification"); err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner("wordpress-deploy-qualification-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operations.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ws := hub.New(nil)
	go ws.Run()
	pipe := &pipeline.Pipeline{
		DB: db, OperationStore: operations, CheckpointStore: db, SagaStore: saga.NewPostgresStore(db.Pool),
		AppsDir: appsDir, Nomad: client, WS: ws, Production: true, RegistryURL: "docker.io/library",
		DatabaseTargets: &pipeline.DatabaseTargets{ProfileID: "qualification", Catalog: db.ActiveDatabaseCatalog, Secrets: secrets},
		WPColdStartGate: true,
		VerifyArtifact: func(_ context.Context, image string) error {
			if image != model.QualifiedWordPressVerifiedTLSImage {
				return fmt.Errorf("unqualified image %q", image)
			}
			return nil
		},
		ScanArtifact: func(_ context.Context, image string) error {
			if image != model.QualifiedWordPressVerifiedTLSImage {
				return fmt.Errorf("unqualified image %q", image)
			}
			return nil
		},
	}
	request := pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/qualification", Subject: "operator"}, Key: "wordpress-good-ca", Audit: store.AcceptanceAuditContext{Source: "wordpress-deploy-nomad-integration"}}
	accepted, err := pipe.Run(context.Background(), spec, "HEAD", request)
	if err != nil {
		t.Fatalf("signed deployment acceptance: %v", err)
	}
	if accepted.Replayed || accepted.Intent.Signature.Value == "" || accepted.Operation.Payload["databaseTargets"] == nil {
		t.Fatalf("accepted operation lacks signed database binding: %+v", accepted)
	}
	t.Cleanup(func() {
		_, _, _ = client.API().Jobs().Deregister(app, true, nil)
		_, _ = client.API().Variables().Delete(nomad.DatabaseVariablePath(app), nil)
	})
	worker := &OperationWorker{db: db, pipeline: pipe, id: "wordpress-deploy-qualification", kinds: []string{"app.deploy"}, lease: time.Minute, poll: time.Second}
	completed := wordpressDeployRun(t, db, worker, accepted.Operation.ID)
	if completed.Status != model.OperationSucceeded {
		t.Fatalf("good-CA deploy = %s: %s", completed.Status, completed.Message)
	}
	deploymentID, _ := accepted.Operation.Payload["deploymentId"].(string)
	deployment, err := db.GetDeployment(context.Background(), deploymentID)
	if err != nil || deployment.ImageTag != model.QualifiedWordPressVerifiedTLSImage || deployment.SourceKind != "git_clone" || deployment.CommitSHA == "" || deployment.SourceDirty {
		t.Fatalf("completed deployment provenance=%+v err=%v", deployment, err)
	}
	variable, _, err := client.API().Variables().Read(nomad.DatabaseVariablePath(app), nil)
	if err != nil || variable == nil || !bytes.Equal([]byte(variable.Items[nomad.DatabaseTLSItemKey("primary", "ca")]), ca) {
		t.Fatalf("rendered private CA variable was unavailable or did not match the trusted CA: %v", err)
	}
	wordpressDeployAssertAllocationPage(t, client, app, true)
	resolver, err := database.NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "qualification", Purpose: database.PurposeApplication, LogicalResourceID: "primary"})
	if err != nil || len(accepted.Regions) != 1 || source.MySQLMaintenance == nil {
		t.Fatalf("deployed WordPress source binding unavailable: %v", err)
	}
	dumpTool, dumpDigest := "", strings.Repeat("d", 64)
	if quiesceSource {
		dumpTool, err = exec.LookPath("mysqldump")
		if err != nil {
			t.Fatal(err)
		}
		toolBytes, readErr := os.ReadFile(dumpTool)
		if readErr != nil {
			t.Fatal(readErr)
		}
		dumpDigest = fmt.Sprintf("%x", sha256.Sum256(toolBytes))
	}
	selection := store.MySQLSourceSnapshotAdmissionRequest{Binding: store.MySQLDeployedSourceBindingRequest{
		DeploymentID: deployment.ID, App: app, SpecDigest: deployment.SpecDigest,
		Region: accepted.Regions[0].Name, NomadRegion: accepted.Regions[0].NomadRegion,
		ProfileID: "qualification", LogicalID: "primary", CatalogRevision: 1, Source: source.Target},
		Maintenance: *source.MySQLMaintenance, DumpToolSHA256: dumpDigest}
	wrongMaintenance := selection
	wrongMaintenance.Maintenance.SnapshotCredentialRef = "secret:wp/unbound-snapshot"
	if _, err := db.AcceptPrivateMySQLSourceSnapshot(context.Background(), operations, client,
		store.MySQLSourceSnapshotAcceptanceInput{Selection: wrongMaintenance, Actor: request.Actor, Key: "wordpress-unbound-source"}); !errors.Is(err, store.ErrMySQLSourceSnapshotFence) {
		t.Fatalf("unbound source snapshot credential was accepted: %v", err)
	}
	sourceAccepted, err := db.AcceptPrivateMySQLSourceSnapshot(context.Background(), operations, client,
		store.MySQLSourceSnapshotAcceptanceInput{Selection: selection, Actor: request.Actor, Key: "wordpress-bound-source",
			Audit: store.AcceptanceAuditContext{Source: "wordpress-deploy-nomad-integration"}})
	if err != nil {
		t.Fatalf("deployed WordPress source snapshot admission: %v", err)
	}
	var sourceRequest store.MySQLSourceSnapshotRequest
	encodedSource, err := json.Marshal(sourceAccepted.Operation.Payload)
	if err != nil || json.Unmarshal(encodedSource, &sourceRequest) != nil || sourceAccepted.Intent.Signature.Value == "" ||
		sourceRequest.JobIdentity.JobID != app || len(sourceRequest.JobIdentity.AllocationIDs) == 0 || sourceRequest.Source != source.Target {
		t.Fatalf("signed deployed WordPress source identity was incomplete: %v", err)
	}
	if quiesceSource {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		claimed, claim, err := db.ClaimNextOperation(ctx, "wordpress-source-qualification", 2*time.Minute, []string{store.MySQLSourceSnapshotOperationKind})
		if err != nil || claimed == nil || claim.OperationID() != sourceAccepted.Operation.ID {
			t.Fatalf("claim accepted WordPress source operation: %+v, %v", claimed, err)
		}
		runner := store.MySQLSourceSnapshotRunner{Control: db, Acceptance: operations, Secrets: secrets, Stopper: client}
		if err := runner.RunClaimed(ctx, claim, sourceRequest); err != nil {
			t.Fatalf("quiesce deployed WordPress source: %v", err)
		}
		if err := database.InspectMySQLRuntimeAccountLockForRestore(ctx, source, *source.MySQLMaintenance, secrets); err != nil {
			t.Fatalf("WordPress runtime account was not locked: %v", err)
		}
		stage := t.TempDir()
		if err := os.Chmod(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		receipt, err := runner.StageClaimed(ctx, claim, sourceRequest, dumpTool, stage, wordpressSourceDatabaseStager{})
		if err != nil || receipt.Receipt.OperationID != sourceAccepted.Operation.ID || receipt.Receipt.Artifact.Bytes <= 0 || receipt.Signature.Value == "" {
			t.Fatalf("stage stopped WordPress source: %+v, %v", receipt, err)
		}
		var launchState, sourceState string
		if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_runtime_launch_reservations WHERE reservation_id=$1`, sourceRequest.RuntimeLaunchReservationID).Scan(&launchState); err != nil || launchState != "stopped" {
			t.Fatalf("WordPress launch reservation after signed stop=%q, %v", launchState, err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, sourceAccepted.Operation.ID).Scan(&sourceState); err != nil || sourceState != "stage-proved" {
			t.Fatalf("WordPress source intent after staging=%q, %v", sourceState, err)
		}
		staged, err := os.ReadFile(receipt.Receipt.ArtifactPath)
		if err != nil || !bytes.Contains(staged, []byte("source-rehearsal")) {
			t.Fatalf("staged SQL did not retain the disposable source marker: %v", err)
		}
		return
	}

	// Rotate only the private CA reference. The target identity remains the
	// same, but the control-plane session verifies the new CA before it ever
	// stages a variable. This is intentionally earlier than WordPress startup:
	// an unrelated CA must not reach an allocation through app.deploy.
	wrongCatalog := wordpressDeployCatalog(host, port, user, databaseName, "secret:wp/wrong-ca")
	wrongCatalog.Services[0].EngineVersion = catalog.Services[0].EngineVersion
	if _, err := db.ActivateDatabaseCatalog(context.Background(), 1, wrongCatalog, "wordpress-deploy-qualification"); err != nil {
		t.Fatal(err)
	}
	request.Key = "wordpress-wrong-ca"
	bad, err := pipe.Run(context.Background(), spec, "HEAD", request)
	if err != nil {
		t.Fatalf("wrong-CA signed deployment acceptance: %v", err)
	}
	failed := wordpressDeployRun(t, db, worker, bad.Operation.ID)
	if failed.Status != model.OperationFailed {
		t.Fatalf("wrong-CA deploy = %s: %s", failed.Status, failed.Message)
	}
	variable, _, err = client.API().Variables().Read(nomad.DatabaseVariablePath(app), nil)
	if err != nil || variable == nil || !bytes.Equal([]byte(variable.Items[nomad.DatabaseTLSItemKey("primary", "ca")]), ca) || variable.Items["norn_rev2_db_tls_ca_primary"] != "" {
		t.Fatalf("wrong CA reached a Nomad delivery variable or trusted delivery was lost: %v", err)
	}
}

type wordpressSourceDatabaseStager struct{}

func (wordpressSourceDatabaseStager) Stage(ctx context.Context, source database.ResolvedBinding, expected database.TargetIdentity, secrets database.SecretSource, tool, digest, directory string) (string, database.MySQLSQLArtifact, error) {
	return database.StageMySQLSQLSnapshotWithMaintenanceCredential(ctx, source, expected, secrets, tool, digest, directory)
}

func deployQualificationPEM(t *testing.T, name string) ([]byte, bool) {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return nil, false
	}
	value, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || !bytes.Contains(value, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("%s must be base64 PEM", name)
	}
	return value, true
}

func wordpressDeployControlDB(t *testing.T, databaseURL string) *store.DB {
	t.Helper()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "wordpress_deploy_live_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	poolConfig := adminConfig.Copy()
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	if poolConfig.MaxConns < 4 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func wordpressDeploySource(t *testing.T, app, volume string) (string, *model.InfraSpec) {
	t.Helper()
	appsDir, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(appsDir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	specText := fmt.Sprintf(`schemaVersion: norn.app/v2
name: %s
deploy: true
repo:
  url: file://%s
  branch: main
build:
  image: %s
startupAdapter: wordpress-verified-tls/v1
processes:
  web:
    port: 80
    health:
      path: /wp-admin/install.php
      interval: 2s
      timeout: 1s
    resources:
      cpu: 500
      memory: 512
volumes:
  - name: %s
    mount: /var/www/html/wp-content
databases:
  - name: primary
    purpose: application
    capabilities: [runtime]
    runtime:
      components:
        host: WORDPRESS_DB_HOST
        user: WORDPRESS_DB_USER
        password: WORDPRESS_DB_PASSWORD
        name: WORDPRESS_DB_NAME
      tls:
        caFileEnv: MYSQL_SSL_CA
`, app, source, model.QualifiedWordPressVerifiedTLSImage, volume)
	for _, root := range []string{source, filepath.Join(appsDir, app)} {
		if err := os.WriteFile(filepath.Join(root, "infraspec.yaml"), []byte(specText), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("git", "-C", source, "init", "-q", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for _, args := range [][]string{{"add", "infraspec.yaml"}, {"-c", "user.name=qualification", "-c", "user.email=qualification@example.invalid", "commit", "-q", "-m", "source"}} {
		command = exec.Command("git", append([]string{"-C", source}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appsDir, app, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if result := model.ValidateSpec(spec); !result.Valid {
		t.Fatalf("qualification spec invalid: %+v", result.Findings)
	}
	return appsDir, spec
}

func writeDeploySecret(t *testing.T, root, name string, content []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func wordpressDeployCatalog(host string, port int, role, databaseName, caRef string) database.Catalog {
	maintenance := wordpressDeployMySQLMaintenance()
	return database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "wp-mysql", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "disposable:mysql",
			Endpoint: database.DatabaseEndpoint{Host: host, Port: port}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSVerifyFull}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot, database.CapabilityRestore}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "wp-primary", ServiceID: "wp-mysql", Database: databaseName, Role: role, Generation: 1, CredentialRef: "secret:wp/password",
			TLS: database.DatabaseTLS{Mode: database.TLSVerifyFull, ServerName: host, CARef: caRef}, MySQLMaintenance: &maintenance}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "qualification", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"primary": "wp-primary"}}},
	}
}

func wordpressDeployMySQLMaintenance() database.MySQLMaintenanceCredentials {
	return database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "wp_snapshot", SnapshotAccountHost: "%",
		SnapshotCredentialRef: "secret:wp/snapshot", RestoreRole: "wp_restore", RestoreAccountHost: "%",
		RestoreCredentialRef: "secret:wp/restore", FenceRole: "wp_fence", FenceAccountHost: "%", FenceCredentialRef: "secret:wp/fence"}
}

func wordpressDeployRun(t *testing.T, db *store.DB, worker *OperationWorker, operationID string) *model.Operation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for ctx.Err() == nil {
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at=clock_timestamp() WHERE id=$1 AND status='queued'`, operationID); err != nil {
			t.Fatal(err)
		}
		if err := worker.runOnce(ctx); err != nil {
			t.Fatal(err)
		}
		op, err := db.GetOperation(ctx, operationID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == model.OperationSucceeded || op.Status == model.OperationFailed {
			return op
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("operation %s did not finish: %v", operationID, ctx.Err())
	return nil
}

func wordpressDeployAssertAllocationPage(t *testing.T, client *nomad.Client, jobID string, available bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		allocations, _, err := client.API().Jobs().Allocations(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, allocation := range allocations {
			if allocation.ClientStatus != "running" {
				continue
			}
			full, _, err := client.API().Allocations().Info(allocation.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			size := make(chan nomadapi.TerminalSize)
			close(size)
			execCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			exit, err := client.API().Allocations().Exec(execCtx, full, "web", false, []string{"php", "-r", `echo @file_get_contents("http://127.0.0.1/wp-admin/install.php");`}, strings.NewReader(""), &stdout, &stderr, size, nil)
			cancel()
			last = strings.TrimSpace(stdout.String() + " " + stderr.String())
			if err == nil && exit == 0 {
				page := stdout.String()
				good := strings.Contains(page, "WordPress") && !strings.Contains(page, "Error establishing a database connection")
				if good == available {
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	if available {
		t.Fatalf("generated WordPress allocation did not serve through its verified database path: %s", last)
	}
	t.Fatalf("generated WordPress allocation accepted the wrong CA or gave no decisive database error: %s", last)
}
