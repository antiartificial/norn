package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/store"
)

// runInspectSource only reads signed control evidence. It does not infer that
// a job stopped, a MySQL account is locked, or an object still exists.
func runInspectSource(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("inspect-source", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	operationID := flags.String("source-operation-id", "", "exact signed source operation ID")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid source inspection arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *operationID == "" || !schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private source inspection selection")
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
	if err != nil {
		return errors.New("control PostgreSQL configuration is invalid")
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	store.DeclareReaderContract(config, "mysql-source-inspection")
	config.ConnConfig.RuntimeParams["search_path"] = *schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot open control PostgreSQL pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("control PostgreSQL is unavailable")
	}
	db := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(auditKey, previous...)
	if err != nil {
		return errors.New("audit signing key is invalid")
	}
	acceptance, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{ExpectedAuthority: *authority})
	if err != nil {
		return errors.New("signed acceptance policy is invalid")
	}
	inspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, *operationID)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(inspection)
}
