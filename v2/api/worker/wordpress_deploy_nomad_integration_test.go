package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
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

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/hub"
	"norn/v2/api/internal/s3emulator"
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
// disposable services. Source-quiescence mode also requires a row containing
// "source-rehearsal" in the disposable database, a snapshot account with
// SELECT/SHOW VIEW/TRIGGER/EVENT/LOCK TABLES, and a fence account allowed to
// inspect mysql.user and lock the runtime account. The test deregisters its
// uniquely named job, deletes its Nomad Variable, and drops its PostgreSQL
// schema. NORN_TEST_WORDPRESS_DEPLOY_RESTORE=1 additionally requires an empty
// distinct database, a runtime account, and a wp_restore account with import
// rights. Supply their names/passwords through the RESTORE_DATABASE,
// RESTORE_USER, RESTORE_PASSWORD, and RESTORE_ROLE_PASSWORD variables. This
// mode restores the retained signed artifact into that disposable target,
// completes signed target unlock/fence release, then deploys a fresh pinned
// WordPress allocation against the recovered target profile.
func TestClaimedWordPressVerifiedTLSDeployInNomad(t *testing.T) {
	address, controlURL := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DATABASE_URL")
	host, serverName := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_HOST"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_SERVER_NAME")
	user, password, databaseName := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_USER"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PASSWORD"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_DATABASE")
	volume := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_CONTENT_VOLUME")
	quiesceSource := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_SOURCE_QUIESCE") == "1"
	restoreSource := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_RESTORE") == "1"
	snapshotPassword, fencePassword := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_SNAPSHOT_PASSWORD"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_FENCE_PASSWORD")
	restoreDatabase, restoreUser := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_RESTORE_DATABASE"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_RESTORE_USER")
	restorePassword, restoreRolePassword := os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_RESTORE_PASSWORD"), os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_RESTORE_ROLE_PASSWORD")
	port, portErr := strconv.Atoi(os.Getenv("NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PORT"))
	ca, goodCA := deployQualificationPEM(t, "NORN_TEST_WORDPRESS_DEPLOY_MYSQL_CA_PEM_B64")
	wrongCA, badCA := deployQualificationPEM(t, "NORN_TEST_WORDPRESS_DEPLOY_MYSQL_WRONG_CA_PEM_B64")
	if address == "" || controlURL == "" || host == "" || serverName == "" || host != serverName || user == "" || password == "" || databaseName == "" || volume == "" || !goodCA || !badCA || portErr != nil || port < 1 || port > 65535 || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable Nomad/PostgreSQL/MySQL TLS variables and NORN_TEST_NOMAD_DOCKER=1 for claimed WordPress deploy qualification")
	}
	if quiesceSource && (snapshotPassword == "" || fencePassword == "") {
		t.Skip("set disposable snapshot and fence account passwords for source quiescence qualification")
	}
	if restoreSource && (!quiesceSource || restoreDatabase == "" || restoreDatabase == databaseName || restoreUser == "" || restorePassword == "" || restoreRolePassword == "") {
		t.Skip("set source quiescence and a distinct disposable empty MySQL restore database with runtime and restore credentials")
	}
	if restoreSource && os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI") == "" {
		t.Skip("set the built private MySQL maintenance CLI for signed recovery qualification")
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
	if restoreSource {
		writeDeploySecret(t, secretRoot, "wp/restore-runtime", []byte(fmt.Sprintf(`{"password":%q}`, restorePassword)))
		writeDeploySecret(t, secretRoot, "wp/restore", []byte(fmt.Sprintf(`{"password":%q}`, restoreRolePassword)))
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
	if restoreSource {
		maintenance := wordpressDeployMySQLMaintenance()
		catalog.Bindings = append(catalog.Bindings, database.DatabaseBinding{APIVersion: database.APIVersion, ID: "wp-restore-target", ServiceID: "wp-mysql",
			Database: restoreDatabase, Role: restoreUser, Generation: 1, CredentialRef: "secret:wp/restore-runtime",
			TLS: database.DatabaseTLS{Mode: database.TLSVerifyFull, ServerName: host, CARef: "secret:wp/ca"}, MySQLMaintenance: &maintenance})
		catalog.Profiles[0].DatabaseBindings["restore"] = "wp-restore-target"
		catalog.Profiles = append(catalog.Profiles, database.DeploymentProfile{APIVersion: database.APIVersion, ID: "qualification-recovered",
			Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"primary": "wp-restore-target"}})
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
		if allocations, _, inspectErr := client.API().Jobs().Allocations(app, false, nil); inspectErr == nil {
			for _, allocation := range allocations {
				t.Logf("failed deploy allocation: id=%s job=%s eval=%s desired=%s client=%s", allocation.ID, allocation.JobID, allocation.EvalID, allocation.DesiredStatus, allocation.ClientStatus)
				if evaluation, _, evalErr := client.API().Evaluations().Info(allocation.EvalID, nil); evalErr == nil {
					t.Logf("allocation evaluation: id=%s job=%s previous=%s next=%s jobIndex=%d", evaluation.ID, evaluation.JobID, evaluation.PreviousEval, evaluation.NextEval, evaluation.JobModifyIndex)
				}
			}
		} else {
			t.Logf("failed deploy allocation inspection: %v", inspectErr)
		}
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
	if quiesceSource {
		wordpressDeployInstall(t, client, app)
	}
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
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if _, _, err := db.ClaimPrivateMySQLOperation(ctx, uuid.NewString(), "wordpress-foreign-source", store.MySQLSourceSnapshotOperationKind, time.Minute); !errors.Is(err, store.ErrMySQLMaintenanceClaimUnavailable) {
			t.Fatalf("exact private claim selected an unrelated queued source operation: %v", err)
		}
		if restoreSource {
			emulator, server, sourceID, receipt := wordpressSourceThroughCommand(t, ctx, db, operations, client, selection,
				sourceAccepted.Operation.ID, sourceRequest, dumpTool, secretRoot, controlURL, authority, request.Actor)
			if err := database.InspectMySQLRuntimeAccountLockForRestore(ctx, source, *source.MySQLMaintenance, secrets); err != nil {
				t.Fatalf("WordPress source runtime account was not locked: %v", err)
			}
			wordpressRestoreStagedSource(t, ctx, db, operations, client, secrets, secretRoot, controlURL, catalog,
				sourceID, store.OperationClaim{}, receipt, restoreDatabase, emulator, server)
			wordpressDeployRecovered(t, ctx, db, client, pipe, request, app, volume, secrets)
			return
		}
		claimed, claim, err := db.ClaimPrivateMySQLOperation(ctx, sourceAccepted.Operation.ID, "wordpress-source-qualification", store.MySQLSourceSnapshotOperationKind, 2*time.Minute)
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
		if err != nil || !bytes.Contains(staged, []byte("source-rehearsal")) ||
			!bytes.Contains(staged, []byte("wp_options")) || !bytes.Contains(staged, []byte("wp_users")) {
			t.Fatalf("staged SQL did not retain the WordPress tables and disposable source marker: read=%v marker=%t options=%t users=%t", err,
				bytes.Contains(staged, []byte("source-rehearsal")), bytes.Contains(staged, []byte("wp_options")), bytes.Contains(staged, []byte("wp_users")))
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

func wordpressDeployRecovered(t *testing.T, ctx context.Context, db *store.DB, client *nomad.Client,
	pipe *pipeline.Pipeline, request pipeline.EnqueueRequest, app, volume string, secrets database.SecretSource) {
	t.Helper()
	recoveredApp := app + "-recovered"
	recoveredAppsDir, recoveredSpec := wordpressDeploySource(t, recoveredApp, volume)
	recoveredPipe := *pipe
	recoveredPipe.AppsDir = recoveredAppsDir
	recoveredPipe.DatabaseTargets = &pipeline.DatabaseTargets{ProfileID: "qualification-recovered", Catalog: db.ActiveDatabaseCatalog, Secrets: secrets}
	recoveredRequest := request
	recoveredRequest.Key = "wordpress-restored-target"
	recoveredAccepted, err := recoveredPipe.Run(ctx, recoveredSpec, "HEAD", recoveredRequest)
	if err != nil {
		t.Fatalf("signed WordPress deploy against recovered MySQL target: %v", err)
	}
	t.Cleanup(func() {
		_, _, _ = client.API().Jobs().Deregister(recoveredApp, true, nil)
		_, _ = client.API().Variables().Delete(nomad.DatabaseVariablePath(recoveredApp), nil)
	})
	recoveredWorker := &OperationWorker{db: db, pipeline: &recoveredPipe, id: "wordpress-recovered-qualification",
		kinds: []string{"app.deploy"}, lease: time.Minute, poll: time.Second}
	recoveredOperation := wordpressDeployRun(t, db, recoveredWorker, recoveredAccepted.Operation.ID)
	if recoveredOperation.Status != model.OperationSucceeded {
		t.Fatalf("recovered WordPress deploy = %s: %s", recoveredOperation.Status, recoveredOperation.Message)
	}
	wordpressDeployAssertAllocationPage(t, client, recoveredApp, true)
}

type wordpressStopWithLostResponse struct{ client *nomad.Client }

func (stopper wordpressStopWithLostResponse) StopJobCAS(ctx context.Context, request nomad.CASStopJobRequest) error {
	if err := stopper.client.StopJobCAS(ctx, request); err != nil {
		return err
	}
	return errors.New("disposable Nomad stop response lost")
}

func wordpressSourceThroughCommand(t *testing.T, ctx context.Context, db *store.DB, operations *store.PGOperationStore,
	client *nomad.Client, selection store.MySQLSourceSnapshotAdmissionRequest, sourceID string,
	sourceRequest store.MySQLSourceSnapshotRequest, dumpTool, secretRoot, controlURL, authority string,
	actor store.OperationActor) (*s3emulator.Emulator, *httptest.Server, string, store.SignedMySQLSourceArtifactReceipt) {
	t.Helper()
	emulator, server := s3emulator.Start("norn-wordpress-artifacts", "wordpress-artifact-writer")
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	stage := t.TempDir()
	spool := t.TempDir()
	for _, directory := range []string{private, stage, spool} {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	selectionFile := filepath.Join(private, "selection.json")
	databaseURLFile := filepath.Join(private, "database-url")
	auditKeyFile := filepath.Join(private, "audit-key")
	accessFile := filepath.Join(private, "s3-access")
	secretFile := filepath.Join(private, "s3-secret")
	caFile := filepath.Join(private, "s3-ca.pem")
	for path, value := range map[string][]byte{selectionFile: selectionJSON, databaseURLFile: []byte(controlURL),
		auditKeyFile: []byte("wordpress-deploy-qualification-signing-key"), accessFile: []byte("wordpress-artifact-writer"),
		secretFile: []byte("test-secret"), caFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var schema string
	if err := db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	args := []string{"source", "--database-url-file", databaseURLFile, "--audit-key-file", auditKeyFile,
		"--authority", authority, "--schema", schema, "--secrets-dir", secretRoot,
		"--nomad-url", os.Getenv("NORN_TEST_NOMAD_ADDR"), "--selection-file", selectionFile,
		"--source-database", selection.Binding.Source.Database, "--actor-issuer", actor.Issuer,
		"--actor-subject", actor.Subject, "--request-key", "wordpress-bound-source",
		"--stage-dir", stage, "--dump-tool-path", dumpTool, "--s3-endpoint", endpoint.Host,
		"--s3-bucket", "norn-wordpress-artifacts", "--s3-prefix", "mysql/wordpress", "--s3-region", "us-east-1",
		"--s3-access-key-file", accessFile, "--s3-secret-key-file", secretFile,
		"--s3-spool-dir", spool, "--s3-spool-capacity", fmt.Sprint(64 << 20)}
	var orphanStagePath string
	if os.Getenv("NORN_TEST_WORDPRESS_SOURCE_RECONCILE") == "1" {
		claimed, claim, err := db.ClaimPrivateMySQLOperation(ctx, sourceID, "wordpress-lost-stop-response", store.MySQLSourceSnapshotOperationKind, time.Minute)
		if err != nil || claimed == nil {
			t.Fatalf("claim source before ambiguous stop: %+v %v", claimed, err)
		}
		if err := db.StopClaimedMySQLSourceJob(ctx, operations, claim, sourceRequest,
			wordpressStopWithLostResponse{client: client}); !errors.Is(err, store.ErrMySQLSourceStopIndeterminate) {
			t.Fatalf("source stop did not preserve ambiguous result: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
			t.Fatal(err)
		}
		if err := db.RecoverExpiredOperations(ctx); err != nil {
			t.Fatal(err)
		}
		prior, err := db.GetOperation(ctx, sourceID)
		if err != nil || prior.Status != model.OperationFailed || prior.Metadata["manualRecoveryRequired"] != true {
			t.Fatalf("ambiguous source was not fenced for reconciliation: %+v %v", prior, err)
		}
		args[0] = "reconcile-source"
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--selection-file" {
				args[i], args[i+1] = "--prior-source-operation-id", sourceID
			}
			if args[i] == "--request-key" {
				args[i+1] = "wordpress-bound-source-reconciliation"
			}
		}
		crashBeforeCommit := os.Getenv("NORN_TEST_WORDPRESS_SOURCE_CRASH_BEFORE_COMMIT") == "1"
		crashAfterStage := os.Getenv("NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_STAGE") == "1"
		crashBeforePublish := os.Getenv("NORN_TEST_WORDPRESS_SOURCE_CRASH_BEFORE_PUBLISH") == "1"
		crashAfterPublish := os.Getenv("NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_PUBLISH") == "1"
		if os.Getenv("NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_TRANSFER") == "1" || crashBeforeCommit || crashAfterStage || crashBeforePublish || crashAfterPublish {
			firstArgs := append([]string(nil), args...)
			for i := 0; i < len(firstArgs)-1; i++ {
				if firstArgs[i] == "--request-key" {
					firstArgs[i+1] = "wordpress-first-reconciliation"
				}
			}
			marker := filepath.Join(private, "transfer-checkpoint")
			command := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), firstArgs...)
			var commandOutput bytes.Buffer
			command.Stdout, command.Stderr = &commandOutput, &commandOutput
			markerEnv := "NORN_TEST_SOURCE_TRANSFER_MARKER=" + marker
			if crashBeforeCommit {
				markerEnv = "NORN_TEST_SOURCE_BEFORE_COMMIT_MARKER=" + marker
			} else if crashAfterStage {
				markerEnv = "NORN_TEST_SOURCE_STAGE_DUMP_MARKER=" + marker
			} else if crashBeforePublish {
				markerEnv = "NORN_TEST_SOURCE_PUBLISH_BEFORE_UPLOAD_MARKER=" + marker
			} else if crashAfterPublish {
				markerEnv = "NORN_TEST_SOURCE_PUBLISH_AFTER_VERIFY_MARKER=" + marker
			}
			command.Env = append(os.Environ(), "SSL_CERT_FILE="+caFile, markerEnv)
			if err := command.Start(); err != nil {
				t.Fatalf("start first reconciliation process: %v", err)
			}
			t.Cleanup(func() { _ = command.Process.Kill() })
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			var firstID string
			deadline := time.After(30 * time.Second)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
		waitForTransfer:
			for {
				select {
				case err := <-done:
					t.Fatalf("first reconciliation exited before transfer marker: %v: %s", err, commandOutput.String())
				case <-deadline:
					_ = command.Process.Kill()
					<-done
					t.Fatalf("first reconciliation did not reach transfer marker: %s", commandOutput.String())
				case <-ticker.C:
					content, err := os.ReadFile(marker)
					if err == nil {
						firstID = strings.SplitN(string(content), "\n", 2)[0]
						break waitForTransfer
					}
					if !os.IsNotExist(err) {
						_ = command.Process.Kill()
						<-done
						t.Fatalf("read transfer marker: %v", err)
					}
				}
			}
			if !crashBeforeCommit {
				priorInspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, operations, sourceID)
				if err != nil || priorInspection.ReconciledByOperationID != firstID || !priorInspection.RuntimeFenceHeld {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("transfer marker lacked committed signed proof: %+v %v", priorInspection, err)
				}
			}
			if crashAfterStage {
				content, err := os.ReadFile(marker)
				parts := strings.SplitN(string(content), "\n", 2)
				if err != nil || len(parts) != 2 || !filepath.IsAbs(parts[1]) {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("stage marker lacks local artifact: %v", err)
				}
				orphanStagePath = parts[1]
				info, err := os.Stat(orphanStagePath)
				stageInspection, inspectErr := db.InspectPrivateMySQLSourceSnapshot(ctx, operations, firstID)
				if err != nil || !info.Mode().IsRegular() || inspectErr != nil ||
					stageInspection.IntentState != "stage-intended" || stageInspection.StageReceiptVerified {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("stage checkpoint lacks unsigned local dump: %v %+v %v", err, stageInspection, inspectErr)
				}
			}
			if crashBeforePublish || crashAfterPublish {
				stageInspection, inspectErr := db.InspectPrivateMySQLSourceSnapshot(ctx, operations, firstID)
				stageReceipt, receiptErr := db.LoadSignedMySQLSourceArtifactReceipt(ctx, operations, firstID)
				if inspectErr != nil || receiptErr != nil || stageInspection.IntentState != "publish-intended" ||
					!stageInspection.StageReceiptVerified || stageInspection.RetentionReceiptVerified {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("publication checkpoint lacks signed stage and pending retention: %+v %v %v", stageInspection, inspectErr, receiptErr)
				}
				verifier, err := artifactstore.OpenS3ReadOnlyVerifier(ctx, artifactstore.S3Config{
					Endpoint: endpoint.Host, Bucket: "norn-wordpress-artifacts", Prefix: "mysql/wordpress",
					Region: "us-east-1", AccessKey: "wordpress-artifact-writer", SecretKey: "test-secret",
					Transport: server.Client().Transport})
				if err != nil {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("open exact object verifier: %v", err)
				}
				artifact := stageReceipt.Receipt.Artifact
				descriptor := artifactstore.Descriptor{Key: artifactstore.KeyForSHA256(artifact.SHA256), SHA256: artifact.SHA256, Size: artifact.Bytes}
				verifyErr := verifier.Verify(ctx, descriptor)
				if crashBeforePublish && !errors.Is(verifyErr, artifactstore.ErrArtifactNotFound) || crashAfterPublish && verifyErr != nil {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("publication checkpoint object outcome unexpected: before=%t after=%t err=%v", crashBeforePublish, crashAfterPublish, verifyErr)
				}
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatalf("kill first reconciliation at transfer checkpoint: %v", err)
			}
			if err := <-done; err == nil {
				t.Fatal("first reconciliation exited successfully despite process kill")
			}
			if crashBeforeCommit {
				inspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, operations, sourceID)
				if err != nil || inspection.ReconciledByOperationID != "" || inspection.IntentState != "stop-intended" ||
					!inspection.RuntimeFenceHeld {
					t.Fatalf("killed uncommitted transfer changed predecessor: %+v %v", inspection, err)
				}
				var proofCount int
				if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM mysql_source_snapshot_reconciliations
					WHERE prior_operation_id=$1`, sourceID).Scan(&proofCount); err != nil || proofCount != 0 {
					t.Fatalf("killed uncommitted transfer left proof: %d %v", proofCount, err)
				}
			}
			if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, firstID); err != nil {
				t.Fatal(err)
			}
			if err := db.RecoverExpiredOperations(ctx); err != nil {
				t.Fatal(err)
			}
			failedFirst, err := db.GetOperation(ctx, firstID)
			if err != nil || failedFirst.Status != model.OperationFailed || failedFirst.Metadata["manualRecoveryRequired"] != true {
				t.Fatalf("killed transferred successor did not fail closed: %+v %v", failedFirst, err)
			}
			if !crashBeforeCommit {
				sourceID = firstID
				for i := 0; i < len(args)-1; i++ {
					if args[i] == "--prior-source-operation-id" {
						args[i+1] = firstID
					}
				}
			}
		}
		if crashAfterStage {
			t.Cleanup(func() { _ = os.Remove(orphanStagePath) })
		}
	}
	run := func(input []string) ([]byte, error) {
		command := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), input...)
		command.Env = append(os.Environ(), "SSL_CERT_FILE="+caFile)
		return command.CombinedOutput()
	}
	wrong := append([]string(nil), args...)
	for index := range wrong {
		if wrong[index] == "--source-database" {
			wrong[index+1] += "_wrong"
			break
		}
	}
	if output, err := run(wrong); err == nil || !bytes.Contains(output, []byte("different")) || !bytes.Contains(output, []byte("database")) {
		t.Fatalf("private source accepted wrong database: %v: %s", err, output)
	}
	for attempt := 0; attempt < 2; attempt++ {
		output, err := run(args)
		if attempt == 0 && err == nil && os.Getenv("NORN_TEST_WORDPRESS_SOURCE_RECONCILE") == "1" {
			fields := strings.Fields(string(output))
			if len(fields) > 0 {
				sourceID = strings.TrimPrefix(fields[0], "source_operation_id=")
			}
		}
		if err != nil || !bytes.Contains(output, []byte("source_operation_id="+sourceID+" status=succeeded retention=retained-proved")) {
			t.Fatalf("private source invocation %d: %v: %s", attempt+1, err, output)
		}
	}
	completed, err := db.GetOperation(ctx, sourceID)
	if err != nil || completed.Status != model.OperationSucceeded {
		t.Fatalf("retained source operation is not terminal: %+v, %v", completed, err)
	}
	inspectArgs := []string{"inspect-source", "--database-url-file", databaseURLFile,
		"--audit-key-file", auditKeyFile, "--authority", authority, "--schema", schema,
		"--source-operation-id", sourceID}
	inspectOutput, err := run(inspectArgs)
	if err != nil {
		t.Fatalf("private source inspection command: %v: %s", err, inspectOutput)
	}
	var inspection store.MySQLSourceSnapshotInspection
	if json.Unmarshal(inspectOutput, &inspection) != nil || inspection.OperationID != sourceID ||
		inspection.OperationStatus != model.OperationSucceeded || inspection.IntentState != "retained-proved" ||
		!inspection.StageReceiptVerified || !inspection.RetentionReceiptVerified || !inspection.RuntimeFenceHeld {
		t.Fatalf("private source inspection did not verify retained fence: %s", inspectOutput)
	}
	externalArgs := append(append([]string(nil), inspectArgs...), "--observe-external", "--secrets-dir", secretRoot,
		"--nomad-url", os.Getenv("NORN_TEST_NOMAD_ADDR"), "--s3-endpoint", endpoint.Host,
		"--s3-bucket", "norn-wordpress-artifacts", "--s3-prefix", "mysql/wordpress", "--s3-region", "us-east-1",
		"--s3-access-key-file", accessFile, "--s3-secret-key-file", secretFile)
	externalOutput, err := run(externalArgs)
	if err != nil {
		t.Fatalf("private external source inspection command: %v: %s", err, externalOutput)
	}
	var external store.MySQLSourceSnapshotLiveInspection
	if json.Unmarshal(externalOutput, &external) != nil || !external.NomadStoppedVerified ||
		!external.RuntimeAccountLocked || !external.RetainedObjectVerified || !external.RuntimeFenceHeld {
		t.Fatalf("external source inspection did not prove stopped, locked and retained: %s", externalOutput)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatalf("source completion did not survive operation recovery: %v", err)
	}
	completed, err = db.GetOperation(ctx, sourceID)
	if err != nil || completed.Status != model.OperationSucceeded {
		t.Fatalf("operation recovery changed retained source terminal state: %+v, %v", completed, err)
	}
	receipt, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, operations, sourceID)
	if err != nil || receipt.Receipt.Artifact.Bytes <= 0 {
		t.Fatalf("signed source stage unavailable: %v", err)
	}
	if orphanStagePath != "" && receipt.Receipt.ArtifactPath == orphanStagePath {
		t.Fatal("successor adopted predecessor's unsigned SQL dump")
	}
	if _, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, operations, sourceID); err != nil {
		t.Fatalf("signed source retention unavailable: %v", err)
	}
	if _, err := os.Stat(receipt.Receipt.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("local SQL stage was not cleaned after retention: %v", err)
	}
	return emulator, server, sourceID, receipt
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

// wordpressRestoreStagedSource consumes the exact source operation and receipt
// produced by the deployed WordPress allocation. Its independent S3 clients
// share only the disposable network endpoint and signed object descriptor.
func wordpressRestoreStagedSource(t *testing.T, ctx context.Context, db *store.DB, operations *store.PGOperationStore,
	client *nomad.Client, secrets database.SecretSource, secretRoot, controlURL string, catalog database.Catalog, sourceOperationID string, sourceClaim store.OperationClaim,
	staged store.SignedMySQLSourceArtifactReceipt, targetDatabase string, objectEmulator *s3emulator.Emulator, objectServer *httptest.Server) {
	t.Helper()
	alreadyRetained := objectServer != nil
	if !alreadyRetained {
		objectEmulator, objectServer = s3emulator.Start("norn-wordpress-artifacts", "wordpress-artifact-writer")
		defer objectServer.Close()
	}
	objectEndpoint, err := url.Parse(objectServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	capacity := staged.Receipt.Artifact.Bytes * 2
	if capacity < 64<<20 {
		capacity = 64 << 20
	}
	publishSpool := t.TempDir()
	readerSpool := t.TempDir()
	for _, spool := range []string{publishSpool, readerSpool} {
		if err := os.Chmod(spool, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	objectConfig := artifactstore.S3Config{Endpoint: objectEndpoint.Host, Bucket: "norn-wordpress-artifacts", Prefix: "mysql/wordpress",
		Region: "us-east-1", AccessKey: "wordpress-artifact-writer", SecretKey: "test-secret",
		Transport: objectServer.Client().Transport, SpoolDirectory: publishSpool, SpoolCapacity: capacity, RetainFor: 24 * time.Hour}
	var publisher artifactstore.Store
	if !alreadyRetained {
		publisher, err = artifactstore.OpenS3(ctx, objectConfig)
		if err != nil {
			t.Fatal(err)
		}
	}
	objectConfig.SpoolDirectory = readerSpool
	reader, err := artifactstore.OpenS3(ctx, objectConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !alreadyRetained {
		if _, err := db.RetainClaimedMySQLSourceArtifact(ctx, operations, sourceClaim, publisher); err != nil {
			t.Fatalf("retain signed WordPress source artifact: %v", err)
		}
	}
	resolver, err := database.NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "qualification", Purpose: database.PurposeApplication, LogicalResourceID: "primary"})
	if err != nil || source.MySQLMaintenance == nil {
		t.Fatalf("WordPress recovery source binding is unavailable: %v", err)
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "qualification", Purpose: database.PurposeApplication, LogicalResourceID: "restore"})
	if err != nil || target.MySQLMaintenance == nil || target.Target.Database != targetDatabase {
		t.Fatalf("WordPress restore target is unavailable: %v", err)
	}
	authority, err := operations.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.OperationActor{Issuer: authority + "/qualification", Subject: "operator"}
	privateInput := t.TempDir()
	if err := os.Chmod(privateInput, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseURLFile := filepath.Join(privateInput, "database-url")
	auditKeyFile := filepath.Join(privateInput, "audit-key")
	for path, value := range map[string]string{databaseURLFile: controlURL, auditKeyFile: "wordpress-deploy-qualification-signing-key"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var schema string
	if err := db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	admitArgs := []string{"admit-restore", "--database-url-file", databaseURLFile, "--audit-key-file", auditKeyFile,
		"--authority", authority, "--schema", schema, "--source-operation-id", sourceOperationID,
		"--target-profile", "qualification", "--target-logical-id", "restore", "--target-database", targetDatabase,
		"--actor-issuer", actor.Issuer, "--actor-subject", actor.Subject, "--request-key", "wordpress-restore-" + sourceOperationID}
	wrongAdmission := append([]string(nil), admitArgs...)
	for index := range wrongAdmission {
		if wrongAdmission[index] == "--target-database" {
			wrongAdmission[index+1] += "_wrong"
			break
		}
	}
	if output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), wrongAdmission...).CombinedOutput(); err == nil {
		t.Fatalf("private restore admission accepted wrong target: %s", output)
	}
	var restoreID string
	for attempt := 0; attempt < 2; attempt++ {
		output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), admitArgs...).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte(" status=queued")) {
			t.Fatalf("private restore admission %d: %v: %s", attempt+1, err, output)
		}
		fields := strings.Fields(string(output))
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "restore_operation_id=") {
			t.Fatalf("restore admission output: %s", output)
		}
		id := strings.TrimPrefix(fields[0], "restore_operation_id=")
		if restoreID != "" && restoreID != id {
			t.Fatalf("restore admission replay changed identity: %s", output)
		}
		restoreID = id
	}
	conflictingSource := append([]string(nil), admitArgs...)
	for index := range conflictingSource {
		if conflictingSource[index] == "--source-operation-id" {
			conflictingSource[index+1] = uuid.NewString()
			break
		}
	}
	if output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), conflictingSource...).CombinedOutput(); err == nil || !bytes.Contains(output, []byte("conflict")) {
		t.Fatalf("restore admission changed a signed request identity's source: %v: %s", err, output)
	}
	accepted, err := operations.VerifyAcceptedOperation(ctx, restoreID)
	if err != nil || accepted.Operation.Kind != store.MySQLRestoreOperationKind || accepted.Intent.Signature.Value == "" {
		t.Fatalf("signed WordPress restore admission unavailable: %v", err)
	}
	var signedRequest store.MySQLRestoreRequest
	encoded, err := json.Marshal(accepted.Operation.Payload)
	if err != nil || json.Unmarshal(encoded, &signedRequest) != nil || signedRequest.Target != target.Target ||
		signedRequest.Artifact != staged.Receipt.Artifact || signedRequest.SourceArtifact.ReceiptSHA256 != staged.SHA256 {
		t.Fatalf("restore admission selected wrong target or artifact: %v", err)
	}
	materialized := t.TempDir()
	if err := os.Chmod(materialized, 0o700); err != nil {
		t.Fatal(err)
	}
	retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, operations, sourceOperationID)
	if err != nil {
		t.Fatalf("load signed WordPress retention receipt: %v", err)
	}
	objectKey := "mysql/wordpress/" + retained.Receipt.Artifact.Key
	stagedBytes := []byte(nil)
	if alreadyRetained {
		path, err := artifactstore.MaterializePrivate(ctx, reader, retained.Receipt.Artifact, materialized)
		if err != nil {
			t.Fatalf("materialize retained source before tamper test: %v", err)
		}
		stagedBytes, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	} else {
		stagedBytes, err = os.ReadFile(staged.Receipt.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(staged.Receipt.ArtifactPath); err != nil {
			t.Fatal(err)
		}
	}
	if len(stagedBytes) == 0 {
		t.Fatal("retained source bytes are empty")
	}
	tampered := append([]byte(nil), stagedBytes...)
	tampered[0] ^= 1
	objectEmulator.Tamper(objectKey, tampered)
	if _, err := artifactstore.MaterializePrivate(ctx, reader, retained.Receipt.Artifact, materialized); !errors.Is(err, artifactstore.ErrArtifactCorrupt) {
		t.Fatalf("tampered retained WordPress SQL was materialized before import: %v", err)
	}
	var preparedCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM mysql_restore_intents WHERE operation_id=$1`, restoreID).Scan(&preparedCount); err != nil || preparedCount != 0 {
		t.Fatalf("tampered WordPress artifact crossed durable restore prepare: count=%d err=%v", preparedCount, err)
	}
	objectEmulator.Tamper(objectKey, stagedBytes)
	toolPath, err := exec.LookPath("mysql")
	if err != nil {
		t.Fatal(err)
	}
	toolBytes, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	accessFile := filepath.Join(privateInput, "s3-access")
	secretFile := filepath.Join(privateInput, "s3-secret")
	for path, value := range map[string]string{accessFile: objectConfig.AccessKey, secretFile: objectConfig.SecretKey} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restoreArgs := []string{"restore", "--database-url-file", databaseURLFile, "--audit-key-file", auditKeyFile,
		"--authority", authority, "--schema", schema, "--secrets-dir", secretRoot,
		"--restore-operation-id", restoreID, "--target-database", targetDatabase,
		"--materialize-dir", materialized, "--mysql-tool-path", toolPath,
		"--mysql-tool-sha256", fmt.Sprintf("%x", sha256.Sum256(toolBytes)),
		"--s3-endpoint", objectEndpoint.Host, "--s3-bucket", objectConfig.Bucket,
		"--s3-prefix", objectConfig.Prefix, "--s3-region", objectConfig.Region,
		"--s3-access-key-file", accessFile, "--s3-secret-key-file", secretFile,
		"--s3-spool-dir", readerSpool, "--s3-spool-capacity", fmt.Sprint(capacity)}
	caFile := filepath.Join(privateInput, "s3-test-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: objectServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	runRestore := func(args []string) ([]byte, error) {
		command := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), args...)
		command.Env = append(os.Environ(), "SSL_CERT_FILE="+caFile)
		return command.CombinedOutput()
	}
	wrongRestore := append([]string(nil), restoreArgs...)
	for index := range wrongRestore {
		if wrongRestore[index] == "--target-database" {
			wrongRestore[index+1] = targetDatabase + "_wrong"
			break
		}
	}
	if output, err := runRestore(wrongRestore); err == nil || !bytes.Contains(output, []byte("expected target database")) {
		t.Fatalf("private restore accepted wrong target: %v: %s", err, output)
	}
	if output, err := runRestore(restoreArgs); err != nil || !bytes.Contains(output, []byte("status=succeeded")) {
		t.Fatalf("private retained WordPress restore: %v: %s", err, output)
	}
	verified, err := database.MySQLRestoreBinding(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.VerifyMySQLRestoreTarget(ctx, verified, secrets, staged.Receipt.Artifact.Expectation); err != nil {
		t.Fatalf("restored WordPress schema/data differs from signed source expectation: %v", err)
	}
	if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || !active {
		t.Fatalf("WordPress restore did not retain its runtime fence: active=%t err=%v", active, err)
	}
	if _, err := db.AssessCompletedMySQLRestoreLiveSource(ctx, operations, restoreID, client, secrets); err != nil {
		t.Fatalf("restored WordPress source stop and account lock were not live: %v", err)
	}
	if _, err := db.AssessCompletedMySQLRestoreLiveRecovery(ctx, operations, restoreID, secrets); err != nil {
		t.Fatalf("restored WordPress target was not ready for signed recovery: %v", err)
	}
	cliArgs := []string{"recover", "--database-url-file", databaseURLFile, "--audit-key-file", auditKeyFile,
		"--authority", authority, "--schema", schema, "--secrets-dir", secretRoot, "--nomad-url", os.Getenv("NORN_TEST_NOMAD_ADDR"),
		"--restore-operation-id", restoreID, "--target-database", targetDatabase,
		"--actor-issuer", actor.Issuer, "--actor-subject", actor.Subject,
		"--request-key", "wordpress-recovery-" + restoreID}
	wrongTarget := append([]string(nil), cliArgs...)
	for index := range wrongTarget {
		if wrongTarget[index] == "--target-database" {
			wrongTarget[index+1] = targetDatabase + "_wrong"
			break
		}
	}
	if output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), wrongTarget...).CombinedOutput(); err == nil || !bytes.Contains(output, []byte("expected target database")) {
		t.Fatalf("private recovery accepted a wrong target selection: %v: %s", err, output)
	}
	identity := store.OperationRequestIdentity{Authority: authority, Actor: actor,
		Kind: store.MySQLRestoreRecoveryOperationKind, Resource: "mysql-restore/" + restoreID,
		Key: "wordpress-recovery-" + restoreID}
	if _, err := operations.ResolveIdentity(ctx, identity); !errors.Is(err, store.ErrAcceptanceNotFound) {
		t.Fatalf("wrong target selection created a signed recovery operation: %v", err)
	}
	inspectArgs := append(append([]string(nil), cliArgs...), "--inspect-only")
	if output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), inspectArgs...).CombinedOutput(); err == nil || !bytes.Contains(output, []byte("inspection requires an existing signed recovery")) {
		t.Fatalf("inspection accepted a missing recovery identity: %v: %s", err, output)
	}
	acceptArgs := append(append([]string(nil), cliArgs...), "--accept-only")
	acceptedOutput, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), acceptArgs...).CombinedOutput()
	if err != nil || !bytes.Contains(acceptedOutput, []byte(" status=queued")) {
		t.Fatalf("private recovery acceptance: %v: %s", err, acceptedOutput)
	}
	acceptedRecovery, err := operations.ResolveIdentity(ctx, identity)
	if err != nil || !bytes.Contains(acceptedOutput, []byte("recovery_operation_id="+acceptedRecovery.Operation.ID)) {
		t.Fatalf("accept-only did not retain exact signed identity: %+v %v: %s", acceptedRecovery, err, acceptedOutput)
	}
	inspectionOutput, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), inspectArgs...).CombinedOutput()
	var inspection store.MySQLRestoreRecoveryInspection
	if err != nil || json.Unmarshal(inspectionOutput, &inspection) != nil || inspection.RecoveryOperationID != acceptedRecovery.Operation.ID ||
		inspection.IntentState != "not-started" || !inspection.SourceVerified || !inspection.TargetDataVerified || inspection.TargetAccountState != "locked" {
		t.Fatalf("private recovery inspection=%+v err=%v output=%s", inspection, err, inspectionOutput)
	}
	if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || !active {
		t.Fatalf("acceptance or inspection released the WordPress fence: %v %v", active, err)
	}
	recoveryArgs := cliArgs
	if os.Getenv("NORN_TEST_WORDPRESS_RECONCILE") == "1" {
		claimed, priorClaim, err := db.ClaimPrivateMySQLOperation(ctx, acceptedRecovery.Operation.ID, "wordPress-interrupted-recovery", store.MySQLRestoreRecoveryOperationKind, 2*time.Minute)
		if err != nil || claimed == nil {
			t.Fatalf("claim prior signed WordPress recovery: %+v %v", claimed, err)
		}
		priorRunner := store.MySQLRestoreRecoveryRunner{Control: db, Acceptance: operations, Observer: client, Secrets: secrets}
		if err := priorRunner.RunClaimedTargetUnlock(ctx, priorClaim); err != nil {
			t.Fatalf("unlock prior WordPress target before interruption: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, priorClaim.OperationID()); err != nil {
			t.Fatal(err)
		}
		if err := db.RecoverExpiredOperations(ctx); err != nil {
			t.Fatalf("expire interrupted WordPress recovery: %v", err)
		}
		observedOutput, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), inspectArgs...).CombinedOutput()
		var observed store.MySQLRestoreRecoveryInspection
		if err != nil || json.Unmarshal(observedOutput, &observed) != nil || observed.OperationStatus != model.OperationFailed ||
			observed.IntentState != "target-unlock-proved" || !observed.SourceVerified || !observed.TargetDataVerified || observed.TargetAccountState != "unlocked" {
			t.Fatalf("interrupted WordPress recovery inspection=%+v err=%v output=%s", observed, err, observedOutput)
		}
		if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || !active {
			t.Fatalf("interrupted WordPress recovery released fence: %v %v", active, err)
		}
		recoveryArgs = append(append([]string(nil), cliArgs...), "--reconcile-prior-recovery-id", priorClaim.OperationID())
		for index := range recoveryArgs {
			if recoveryArgs[index] == "--request-key" {
				recoveryArgs[index+1] = "wordpress-reconcile-" + restoreID
				break
			}
		}
	}
	var recoveryID string
	for attempt := 0; attempt < 2; attempt++ {
		output, err := exec.CommandContext(ctx, os.Getenv("NORN_TEST_WORDPRESS_MAINTENANCE_CLI"), recoveryArgs...).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte(" status=succeeded")) {
			t.Fatalf("private signed WordPress recovery invocation %d: %v: %s", attempt+1, err, output)
		}
		fields := strings.Fields(string(output))
		if len(fields) < 2 || len(fields) > 3 || !strings.HasPrefix(fields[0], "recovery_operation_id=") || fields[1] != "status=succeeded" {
			t.Fatalf("private recovery output is invalid: %s", output)
		}
		id := strings.TrimPrefix(fields[0], "recovery_operation_id=")
		if recoveryID != "" && id != recoveryID {
			t.Fatalf("private recovery replay created another operation: %s", output)
		}
		recoveryID = id
	}
	if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || active {
		t.Fatalf("signed WordPress recovery left runtime fence active: active=%t err=%v", active, err)
	}
	recoveredOperation, err := db.GetOperation(ctx, recoveryID)
	if err != nil || recoveredOperation.Status != model.OperationSucceeded {
		t.Fatalf("signed WordPress recovery did not terminalize successfully: operation=%+v err=%v", recoveredOperation, err)
	}
	if err := database.InspectMySQLRuntimeAccountLockForRestore(ctx, source, *source.MySQLMaintenance, secrets); err != nil {
		t.Fatalf("signed recovery unlocked the stopped WordPress source account: %v", err)
	}
	session, err := database.OpenSession(ctx, target, secrets)
	if err != nil {
		t.Fatalf("recovered WordPress target runtime credential cannot connect: %v", err)
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		t.Fatalf("recovered WordPress target runtime credential cannot authenticate: %v", err)
	}
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

func wordpressDeployInstall(t *testing.T, client *nomad.Client, jobID string) {
	t.Helper()
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
		password := uuid.NewString() + uuid.NewString()
		command := []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "25", "--request", "POST",
			"--data-urlencode", "weblog_title=Norn qualification", "--data-urlencode", "user_name=norn_operator",
			"--data-urlencode", "admin_password=" + password, "--data-urlencode", "admin_password2=" + password,
			"--data-urlencode", "admin_email=norn@example.invalid", "--data-urlencode", "blog_public=0",
			"--data-urlencode", "Submit=Install WordPress", "http://127.0.0.1/wp-admin/install.php?step=2"}
		var stdout, stderr bytes.Buffer
		size := make(chan nomadapi.TerminalSize)
		close(size)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		exit, err := client.API().Allocations().Exec(ctx, full, "web", false, command, strings.NewReader(""), &stdout, &stderr, size, nil)
		cancel()
		if err != nil || exit != 0 || !bytes.Contains(stdout.Bytes(), []byte("Success!")) {
			t.Fatalf("WordPress install in verified-TLS allocation failed: exit=%d err=%v stderr=%s", exit, err, strings.TrimSpace(stderr.String()))
		}
		return
	}
	t.Fatal("no running WordPress allocation available for installation")
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
