package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// reconcile-source names one failed source operation. It signs a successor,
// observes the exact external effects, and continues only after the source
// reservation and fence transfer atomically to that successor.
func runReconcileSource(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("reconcile-source", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	priorID := flags.String("prior-source-operation-id", "", "failed signed source operation ID")
	sourceDatabase := flags.String("source-database", "", "expected source database")
	actorIssuer := flags.String("actor-issuer", "", "operator actor issuer")
	actorSubject := flags.String("actor-subject", "", "operator actor subject")
	requestKey := flags.String("request-key", "", "stable successor idempotency key")
	secretsDir := flags.String("secrets-dir", "", "owner-only MySQL secret directory")
	nomadURL := flags.String("nomad-url", "", "Nomad endpoint for exact stopped-job observation")
	stageDir := flags.String("stage-dir", "", "owner-only SQL stage directory")
	dumpTool := flags.String("dump-tool-path", "", "absolute mysqldump path")
	s3Endpoint := flags.String("s3-endpoint", "", "S3 host:port")
	s3Bucket := flags.String("s3-bucket", "", "immutable artifact bucket")
	s3Prefix := flags.String("s3-prefix", "", "artifact prefix")
	s3Region := flags.String("s3-region", "", "S3 region")
	s3AccessFile := flags.String("s3-access-key-file", "", "owner-only S3 access key file")
	s3SecretFile := flags.String("s3-secret-key-file", "", "owner-only S3 secret key file")
	s3Spool := flags.String("s3-spool-dir", "", "owner-only S3 spool directory")
	s3Capacity := flags.Int64("s3-spool-capacity", 0, "S3 spool capacity in bytes")
	s3Insecure := flags.Bool("s3-loopback-http", false, "allow HTTP only for numeric loopback S3 endpoint")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid source reconciliation arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *priorID == "" ||
		*sourceDatabase == "" || *actorIssuer == "" || *actorSubject == "" || *requestKey == "" ||
		*secretsDir == "" || *nomadURL == "" || *stageDir == "" || *dumpTool == "" ||
		*s3Endpoint == "" || *s3Bucket == "" || *s3Region == "" || *s3AccessFile == "" ||
		*s3SecretFile == "" || *s3Spool == "" || *s3Capacity <= 0 || !schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private source reconciliation selection")
	}
	if privateDirectory(*stageDir) != nil || privateDirectory(*s3Spool) != nil {
		return errors.New("source reconciliation directories are not owner-only")
	}
	if !filepath.IsAbs(*dumpTool) {
		return errors.New("mysqldump path must be absolute")
	}
	databaseURL, err := readPrivateText(*databaseURLFile)
	if err != nil {
		return errors.New("database URL file is not owner-only regular input")
	}
	auditKey, err := readPrivateText(*auditKeyFile)
	if err != nil {
		return errors.New("audit key file is not owner-only regular input")
	}
	previous := make([]string, 0, len(previousKeyFiles))
	for _, path := range previousKeyFiles {
		key, err := readPrivateText(path)
		if err != nil {
			return errors.New("previous audit key file is not owner-only regular input")
		}
		previous = append(previous, key)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil || config.MaxConns < 4 {
		return errors.New("control PostgreSQL configuration is invalid")
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	store.DeclareReaderContract(config, "mysql-source-reconciliation")
	config.ConnConfig.RuntimeParams["search_path"] = *schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot open control PostgreSQL pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("control PostgreSQL is unavailable")
	}
	control := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(auditKey, previous...)
	if err != nil {
		return errors.New("audit signing key is invalid")
	}
	acceptance, err := store.NewPGOperationStore(control, signer, store.AcceptancePolicy{ExpectedAuthority: *authority})
	if err != nil {
		return errors.New("signed acceptance policy is invalid")
	}
	prior, err := acceptance.VerifyAcceptedOperation(ctx, *priorID)
	if err != nil {
		return errors.New("failed source predecessor is not signed")
	}
	var priorRequest store.MySQLSourceSnapshotRequest
	if err := decodeSignedSourceRequest(prior.Operation.Payload, &priorRequest); err != nil ||
		priorRequest.Source.Database != *sourceDatabase {
		return errors.New("signed predecessor names a different source database")
	}
	toolBytes, err := os.ReadFile(*dumpTool)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(toolBytes)) != priorRequest.DumpToolSHA256 {
		return errors.New("mysqldump digest differs from signed predecessor")
	}
	accepted, err := control.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance,
		store.MySQLSourceReconciliationAcceptanceInput{PriorSourceOperationID: *priorID,
			Actor: store.OperationActor{Issuer: *actorIssuer, Subject: *actorSubject}, Key: *requestKey,
			Audit: store.AcceptanceAuditContext{Source: "private-mysql-maintenance-cli"}})
	if err != nil {
		return fmt.Errorf("signed source reconciliation admission failed: %w", err)
	}
	if accepted.Operation.Status != model.OperationQueued {
		if accepted.Operation.Status != model.OperationSucceeded {
			return fmt.Errorf("source successor %s requires inspection", accepted.Operation.ID)
		}
		retained, err := control.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, accepted.Operation.ID)
		if err != nil {
			return errors.New("successful successor lacks signed retention proof")
		}
		stage, err := control.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, accepted.Operation.ID)
		if err != nil || cleanupRetainedSourceStage(*stageDir, stage, retained) != nil {
			return errors.New("retained successor local stage cleanup requires inspection")
		}
		_, err = fmt.Fprintf(output, "source_operation_id=%s status=succeeded retention=retained-proved\n", accepted.Operation.ID)
		return err
	}
	verified, err := acceptance.VerifyAcceptedOperation(ctx, accepted.Operation.ID)
	if err != nil {
		return fmt.Errorf("signed source successor verification failed: %w", err)
	}
	var request store.MySQLSourceSnapshotRequest
	if err := decodeSignedSourceRequest(verified.Operation.Payload, &request); err != nil || request.Source.Database != *sourceDatabase {
		return errors.New("signed successor names a different source database")
	}
	if request.DumpToolSHA256 != priorRequest.DumpToolSHA256 {
		return errors.New("signed successor changed source dump tool")
	}
	secrets, err := database.NewDirectorySecretSource(*secretsDir)
	if err != nil {
		return errors.New("MySQL secret directory is unavailable")
	}
	defer secrets.Close()
	observer, err := nomad.NewClient(*nomadURL)
	if err != nil {
		return errors.New("Nomad client is unavailable")
	}
	access, err := readPrivateText(*s3AccessFile)
	if err != nil {
		return errors.New("S3 access key file is not owner-only regular input")
	}
	secret, err := readPrivateText(*s3SecretFile)
	if err != nil {
		return errors.New("S3 secret key file is not owner-only regular input")
	}
	objects, err := artifactstore.OpenS3(ctx, artifactstore.S3Config{Endpoint: *s3Endpoint, Bucket: *s3Bucket,
		Prefix: *s3Prefix, Region: *s3Region, AccessKey: access, SecretKey: secret,
		Insecure: *s3Insecure, SpoolDirectory: *s3Spool, SpoolCapacity: *s3Capacity, RetainFor: 24 * time.Hour})
	if err != nil {
		return fmt.Errorf("retained artifact store is unavailable: %w", err)
	}
	owner := fmt.Sprintf("mysql-source-reconcile:%d:%s", os.Getpid(), accepted.Operation.ID)
	claimed, claim, err := control.ClaimPrivateMySQLOperation(ctx, accepted.Operation.ID, owner,
		store.MySQLSourceSnapshotOperationKind, 2*time.Minute)
	if err != nil || claimed == nil || claim.OperationID() != accepted.Operation.ID {
		return fmt.Errorf("signed source successor is not claimable: %w", err)
	}
	runner := store.MySQLSourceSnapshotRunner{Control: control, Acceptance: acceptance, Secrets: secrets, Observer: observer, Objects: objects}
	if err := runner.RunClaimedReconciliation(ctx, claim); err != nil {
		return fmt.Errorf("source successor %s requires inspection: %w", accepted.Operation.ID, err)
	}
	if err := afterReconcileSourceTransfer(accepted.Operation.ID); err != nil {
		return fmt.Errorf("source successor %s transfer checkpoint failed: %w", accepted.Operation.ID, err)
	}
	priorInspection, err := control.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, *priorID)
	if err != nil || priorInspection.ReconciledByOperationID != accepted.Operation.ID || !priorInspection.RuntimeFenceHeld {
		return errors.New("source predecessor transfer proof requires inspection")
	}
	successorInspection, err := control.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, accepted.Operation.ID)
	if err != nil || !successorInspection.RuntimeFenceHeld {
		return errors.New("source successor fence requires inspection")
	}
	switch successorInspection.IntentState {
	case "stop-proved":
		if err := runner.LockClaimedAfterReconciliation(ctx, claim, request); err != nil {
			return fmt.Errorf("source successor account lock requires inspection: %w", err)
		}
	case "lock-proved":
		// The account was independently proved locked in the reconciliation.
	default:
		return errors.New("source successor checkpoint requires inspection")
	}
	return finishClaimedSource(ctx, control, acceptance, runner, claim, request, *dumpTool, *stageDir, objects, output)
}

func decodeSignedSourceRequest(payload map[string]interface{}, request *store.MySQLSourceSnapshotRequest) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return errors.New("signed source request has trailing data")
	}
	return nil
}
