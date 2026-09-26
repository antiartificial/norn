// Command norn-mysql-maintenance executes an explicitly selected, signed
// private MySQL recovery. It does not expose an HTTP capability or choose the
// next queued operation. Source and restore admission/execution remain separate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const maxPrivateFileBytes = 1 << 20

var schemaPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type privatePaths []string

func (p *privatePaths) String() string { return fmt.Sprintf("%d private files", len(*p)) }
func (p *privatePaths) Set(value string) error {
	if value == "" {
		return errors.New("previous audit key file path is required")
	}
	*p = append(*p, value)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] != "recover" {
		return errors.New("usage: norn-mysql-maintenance recover --database-url-file PATH --audit-key-file PATH --authority UUID --secrets-dir PATH --nomad-url URL --restore-operation-id UUID --target-database NAME --actor-issuer ISSUER --actor-subject SUBJECT --request-key KEY")
	}
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only current audit signing key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file; repeatable")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	secretsDir := flags.String("secrets-dir", "", "owner-only MySQL secret directory")
	nomadURL := flags.String("nomad-url", "", "Nomad API endpoint for stopped source observation")
	restoreID := flags.String("restore-operation-id", "", "completed signed restore operation UUID")
	targetDatabase := flags.String("target-database", "", "expected restored MySQL database")
	actorIssuer := flags.String("actor-issuer", "", "operator actor issuer")
	actorSubject := flags.String("actor-subject", "", "operator actor subject")
	requestKey := flags.String("request-key", "", "stable idempotency key")
	if err := flags.Parse(arguments[1:]); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid recovery arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *secretsDir == "" || *nomadURL == "" ||
		*restoreID == "" || *targetDatabase == "" || *actorIssuer == "" || *actorSubject == "" || *requestKey == "" ||
		!schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private recovery selection")
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
		return errors.New("database URL is invalid")
	}
	if config.MaxConns < 4 {
		return errors.New("PostgreSQL pool needs at least four connections")
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	store.DeclareReaderContract(config, "mysql-recovery")
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
	secrets, err := database.NewDirectorySecretSource(*secretsDir)
	if err != nil {
		return errors.New("MySQL secret directory is unavailable")
	}
	defer secrets.Close()
	observer, err := nomad.NewClient(*nomadURL)
	if err != nil {
		return errors.New("Nomad client is unavailable")
	}
	liveAuthority, err := acceptance.Authority(ctx)
	if err != nil {
		return errors.New("control authority is unavailable")
	}
	actor := store.OperationActor{Issuer: *actorIssuer, Subject: *actorSubject}
	identity := store.OperationRequestIdentity{Authority: liveAuthority, Actor: actor,
		Kind: store.MySQLRestoreRecoveryOperationKind, Resource: "mysql-restore/" + *restoreID, Key: *requestKey}
	if _, err := acceptance.ResolveIdentity(ctx, identity); errors.Is(err, store.ErrAcceptanceNotFound) {
		ready, readyErr := control.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, *restoreID)
		if readyErr != nil {
			return fmt.Errorf("completed restore is not ready for signed recovery: %w", readyErr)
		}
		if ready.Request.Target.Database != *targetDatabase {
			return errors.New("expected target database does not match signed restore")
		}
	} else if err != nil {
		return fmt.Errorf("recovery request identity is unavailable: %w", err)
	}
	accepted, err := control.AcceptPrivateMySQLRestoreRecovery(ctx, acceptance, store.MySQLRestoreRecoveryAcceptanceInput{
		RestoreOperationID: *restoreID, Actor: actor,
		Key: *requestKey, Audit: store.AcceptanceAuditContext{Source: "private-mysql-maintenance-cli"}})
	if err != nil {
		return fmt.Errorf("signed recovery acceptance failed: %w", err)
	}
	if _, err := acceptance.VerifyAcceptedOperation(ctx, accepted.Operation.ID); err != nil {
		return fmt.Errorf("signed recovery verification failed: %w", err)
	}
	encoded, err := json.Marshal(accepted.Operation.Payload)
	var signedRequest store.MySQLRestoreRecoveryRequest
	if err != nil || json.Unmarshal(encoded, &signedRequest) != nil || signedRequest.RestoreOperationID != *restoreID ||
		signedRequest.Target.Database != *targetDatabase {
		return errors.New("expected target database does not match signed recovery")
	}
	if accepted.Operation.Status == model.OperationSucceeded {
		_, err := fmt.Fprintf(output, "recovery_operation_id=%s status=succeeded\n", accepted.Operation.ID)
		return err
	}
	if _, err := control.AssessCompletedMySQLRestoreLiveSource(ctx, acceptance, *restoreID, observer, secrets); err != nil {
		return fmt.Errorf("stopped source is not ready for signed recovery: %w", err)
	}
	if _, err := control.AssessCompletedMySQLRestoreLiveRecovery(ctx, acceptance, *restoreID, secrets); err != nil {
		return fmt.Errorf("restored target is not ready for signed recovery: %w", err)
	}
	owner := fmt.Sprintf("mysql-recovery:%d:%s", os.Getpid(), accepted.Operation.ID)
	claimed, claim, err := control.ClaimPrivateMySQLOperation(ctx, accepted.Operation.ID, owner, store.MySQLRestoreRecoveryOperationKind, 2*time.Minute)
	if err != nil || claimed == nil || claim.OperationID() != accepted.Operation.ID {
		return fmt.Errorf("signed recovery operation is not claimable: %w", err)
	}
	runner := store.MySQLRestoreRecoveryRunner{Control: control, Acceptance: acceptance, Observer: observer, Secrets: secrets}
	if err := runner.RunClaimedTargetUnlock(ctx, claim); err != nil {
		return fmt.Errorf("target unlock requires inspection: %w", err)
	}
	if err := runner.RunClaimedFenceRelease(ctx, claim); err != nil {
		return fmt.Errorf("runtime fence release requires inspection: %w", err)
	}
	finished, err := control.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || finished.Status != model.OperationSucceeded {
		return errors.New("signed recovery terminal receipt is unavailable")
	}
	active, err := control.RuntimeMutationFenceActive(ctx)
	if err != nil || active {
		return errors.New("runtime mutation fence was not proved released")
	}
	_, err = fmt.Fprintf(output, "recovery_operation_id=%s status=succeeded\n", accepted.Operation.ID)
	return err
}

func readPrivateText(path string) (string, error) {
	if path == "" {
		return "", errors.New("private file path is empty")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxPrivateFileBytes {
		return "", errors.New("private file must be owner-only regular input")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return "", errors.New("private file owner mismatch")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxPrivateFileBytes+1))
	if err != nil || len(content) > maxPrivateFileBytes {
		return "", errors.New("private file is unreadable or oversized")
	}
	value := strings.TrimSuffix(string(content), "\n")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("private file contains invalid text")
	}
	return value, nil
}
