package controlrecovery

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"os/exec"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RestoreOptions struct {
	BundlePath        string
	Identities        []age.Identity
	TrustedKeys       []ed25519.PublicKey
	AvailableKeyIDs   []string
	TargetPool        *pgxpool.Pool
	TargetDatabaseURL string
	IsolatedTarget    bool
	PGRestorePath     string
	EvidenceVerifier  RestoredEvidenceVerifier
}

// RestoredEvidenceVerifier must cryptographically verify original signed
// acceptance, audit, and qualification records with independently recovered
// key material. Key-ID presence alone never satisfies this boundary.
type RestoredEvidenceVerifier interface {
	VerifyRestoredEvidence(context.Context, pgx.Tx, string, []string) error
}

// RestoreReport describes a passive restore. Passive means only pg_restore and
// read-only validation ran against the isolated target: no worker, scheduler,
// webhook, session or effect executor is started, and copied leases, claims,
// session owners and runner attempts are inert data that grant no authority.
// ActivationReady is always false; fenced activation is a separate step.
// UnresolvedEffects counts reserved or launched external effects whose outcome
// was unknown at backup time and must be reconciled before any activation.
type RestoreReport struct {
	BundleID          string `json:"bundleId"`
	Authority         string `json:"authority"`
	Schema            string `json:"schema"`
	Passive           bool   `json:"passive"`
	ActivationReady   bool   `json:"activationReady"`
	UnresolvedEffects int64  `json:"unresolvedEffects"`
}

func RestorePassive(ctx context.Context, options RestoreOptions) (RestoreReport, error) {
	if !options.IsolatedTarget || options.TargetPool == nil || options.TargetDatabaseURL == "" {
		return RestoreReport{}, fmt.Errorf("control recovery restore requires an explicitly isolated target")
	}
	if options.PGRestorePath == "" {
		options.PGRestorePath = "pg_restore"
	}
	service, err := newLibpqService(options.TargetDatabaseURL)
	if err != nil {
		return RestoreReport{}, err
	}
	defer service.Close()
	bundle, err := VerifyBundle(options.BundlePath, options.Identities, options.TrustedKeys, options.AvailableKeyIDs)
	if err != nil {
		return RestoreReport{}, err
	}
	defer bundle.Close()
	manifest := bundle.Manifest
	if len(manifest.RequiredSigningKeyIDs) > 0 && options.EvidenceVerifier == nil {
		return RestoreReport{}, fmt.Errorf("control recovery cryptographic evidence verifier is required")
	}

	targetFingerprint, err := databaseFingerprint(ctx, options.TargetPool)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery target identity check failed")
	}
	dsnConnection, err := pgx.Connect(ctx, options.TargetDatabaseURL)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery target command connection failed")
	}
	dsnFingerprint, fingerprintErr := databaseFingerprint(ctx, dsnConnection)
	_ = dsnConnection.Close(context.Background())
	if fingerprintErr != nil || dsnFingerprint != targetFingerprint {
		return RestoreReport{}, fmt.Errorf("control recovery target pool and command database differ")
	}
	if targetFingerprint == manifest.SourceDatabaseFingerprint {
		return RestoreReport{}, fmt.Errorf("control recovery target is the source database")
	}
	var targetObjects int64
	if err := options.TargetPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','S','f')) +
		(SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1)`, manifest.Schema).Scan(&targetObjects); err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery target emptiness check failed")
	}
	if targetObjects != 0 {
		return RestoreReport{}, fmt.Errorf("control recovery target schema is not empty")
	}

	dump, err := bundle.OpenDump()
	if err != nil {
		return RestoreReport{}, err
	}
	command := exec.CommandContext(ctx, options.PGRestorePath, "--single-transaction", "--exit-on-error", "--no-owner", "--no-privileges", "--dbname=service=norn_recovery")
	command.Env = service.environment
	command.Stdin = dump
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	runErr := command.Run()
	closeErr := dump.Close()
	if runErr != nil || closeErr != nil {
		return RestoreReport{}, fmt.Errorf("control recovery pg_restore failed")
	}

	tx, err := options.TargetPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery passive validation start failed")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := classifySchema(ctx, tx, manifest.Schema, InspectionRegistry()); err != nil {
		return RestoreReport{}, err
	}
	if err := validateCatalog(ctx, tx, manifest.Schema); err != nil {
		return RestoreReport{}, err
	}
	if err := validateRelationships(ctx, tx, manifest.Schema); err != nil {
		return RestoreReport{}, err
	}
	var authority string
	if err := tx.QueryRow(ctx, `SELECT authority::text FROM `+pgx.Identifier{manifest.Schema, "control_plane_identity"}.Sanitize()+` WHERE singleton`).Scan(&authority); err != nil || authority != manifest.Authority {
		return RestoreReport{}, fmt.Errorf("control recovery restored authority differs from manifest")
	}
	for _, table := range manifest.Tables {
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{manifest.Schema, table.Name}.Sanitize()).Scan(&count); err != nil || count != table.Count {
			return RestoreReport{}, fmt.Errorf("control recovery restored table counts differ from manifest")
		}
	}
	var unresolved int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{manifest.Schema, "operation_effects"}.Sanitize()+` WHERE lifecycle IN ('reserved','launched')`).Scan(&unresolved); err != nil || unresolved != manifest.UnresolvedEffects {
		return RestoreReport{}, fmt.Errorf("control recovery restored unresolved effects differ from manifest")
	}
	if options.EvidenceVerifier != nil {
		if err := options.EvidenceVerifier.VerifyRestoredEvidence(ctx, tx, manifest.Schema, manifest.RequiredSigningKeyIDs); err != nil {
			return RestoreReport{}, fmt.Errorf("control recovery restored signature verification failed: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery passive validation commit failed")
	}
	return RestoreReport{BundleID: manifest.BundleID, Authority: authority, Schema: manifest.Schema, Passive: true, ActivationReady: false, UnresolvedEffects: manifest.UnresolvedEffects}, nil
}
