package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/store"
)

// admit-restore signs a catalog-derived destination and the exact retained
// source receipt. It never opens MySQL, S3, or an operator-supplied SQL path.
func runAdmitRestore(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("admit-restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	sourceID := flags.String("source-operation-id", "", "signed retained source operation UUID")
	profile := flags.String("target-profile", "", "catalog deployment profile")
	logical := flags.String("target-logical-id", "", "catalog logical database ID")
	database := flags.String("target-database", "", "expected destination database")
	actorIssuer := flags.String("actor-issuer", "", "operator actor issuer")
	actorSubject := flags.String("actor-subject", "", "operator actor subject")
	requestKey := flags.String("request-key", "", "stable idempotency key")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid restore admission arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *sourceID == "" ||
		*profile == "" || *logical == "" || *database == "" || *actorIssuer == "" ||
		*actorSubject == "" || *requestKey == "" || !schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private restore admission selection")
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
	store.DeclareReaderContract(config, "mysql-restore-admission")
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
	accepted, err := control.AcceptPrivateMySQLRestore(ctx, acceptance, store.MySQLRestoreAcceptanceInput{
		SourceOperationID: *sourceID, ProfileID: *profile, LogicalID: *logical, ExpectedDatabase: *database,
		Actor: store.OperationActor{Issuer: *actorIssuer, Subject: *actorSubject}, Key: *requestKey,
		Audit: store.AcceptanceAuditContext{Source: "private-mysql-maintenance-cli"}})
	if err != nil {
		return fmt.Errorf("signed restore admission failed: %w", err)
	}
	if _, err := acceptance.VerifyAcceptedOperation(ctx, accepted.Operation.ID); err != nil {
		return fmt.Errorf("signed restore verification failed: %w", err)
	}
	_, err = fmt.Fprintf(output, "restore_operation_id=%s status=%s\n", accepted.Operation.ID, accepted.Operation.Status)
	return err
}
