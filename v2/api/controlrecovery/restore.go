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
// After validation succeeds, the only write is advancing the restored fleet
// authority epoch (FleetAuthorityEpoch is the new value), which makes every
// restored fence an older-epoch fence until it is re-bound or released.
// UnresolvedEffects counts reserved or launched external effects whose outcome
// was unknown at backup time and must be reconciled before any activation.
type RestoreReport struct {
	BundleID          string `json:"bundleId"`
	Authority         string `json:"authority"`
	Schema            string `json:"schema"`
	Passive           bool   `json:"passive"`
	ActivationReady   bool   `json:"activationReady"`
	UnresolvedEffects int64  `json:"unresolvedEffects"`
	// FleetAuthorityEpoch is the restored authority epoch after the restore
	// advance. It is always greater than the epoch captured in the bundle.
	FleetAuthorityEpoch int64 `json:"fleetAuthorityEpoch"`
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
	// The epoch restored from the bundle, read in the validated snapshot; the
	// advance below is a CAS from exactly this value.
	var restoredEpoch int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM `+pgx.Identifier{manifest.Schema, "fleet_authority_epoch"}.Sanitize()+` WHERE singleton`).Scan(&restoredEpoch); err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery restored fleet authority epoch read failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return RestoreReport{}, fmt.Errorf("control recovery passive validation commit failed")
	}
	// Q4: invalidate pre-restore fence ownership. The advance is a CAS on the
	// restored singleton and rewrites no fence, attempt or history row. If it
	// fails, the error is returned and no report is produced; the restored
	// target stays passive (nothing is started) and the target-emptiness check
	// refuses a second RestorePassive into it, so the advance never doubles.
	fleetEpoch, err := advanceRestoredFleetAuthorityEpoch(ctx, options.TargetPool, manifest.Schema, manifest.BundleID, restoredEpoch)
	if err != nil {
		return RestoreReport{}, err
	}
	return RestoreReport{BundleID: manifest.BundleID, Authority: authority, Schema: manifest.Schema, Passive: true, ActivationReady: false, UnresolvedEffects: manifest.UnresolvedEffects, FleetAuthorityEpoch: fleetEpoch}, nil
}

// epochQuerier is the pool or transaction handle RestorePassive already holds
// for the restored target.
type epochQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// advanceRestoredFleetAuthorityEpoch CASes the restored schema's fleet
// authority epoch from expected to expected+1 (reason restore:<bundleID>) and
// returns the new value. It fails if the epoch is no longer expected. It
// rewrites no fence, attempt or history row, so every pre-restore fence
// trails the new epoch until it is re-bound or released.
func advanceRestoredFleetAuthorityEpoch(ctx context.Context, q epochQuerier, schema, bundleID string, expected int64) (int64, error) {
	var epoch int64
	if err := q.QueryRow(ctx, `UPDATE `+pgx.Identifier{schema, "fleet_authority_epoch"}.Sanitize()+`
		SET epoch = epoch + 1, activated_at = now(), reason = $1
		WHERE singleton AND epoch = $2 RETURNING epoch`, "restore:"+bundleID, expected).Scan(&epoch); err != nil {
		return 0, fmt.Errorf("control recovery restored fleet authority epoch advance failed")
	}
	return epoch, nil
}
