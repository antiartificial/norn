package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/model"
)

var ErrExternalDeploymentNonceConsumed = errors.New("external deployment nonce is consumed or unavailable")
var ErrExternalDeploymentIdempotencyConflict = errors.New("external deployment idempotency key conflicts")
var ErrExternalDeploymentNonceLimit = errors.New("external deployment nonce issuance limit reached")
var ErrExternalDeploymentAdmissionUnavailable = errors.New("external deployment admission is unavailable")
var ErrExternalDeploymentCheckpointConflict = errors.New("external deployment checkpoint reference conflicts")

const externalDeploymentNonceMaxOutstandingPerRun = 3

// ExternalDeploymentNonce is deliberately metadata-only. Callers receive the
// opaque raw nonce once; the database keeps only a SHA-256 digest.
type ExternalDeploymentNonce struct {
	ID                     string
	NonceSHA256            string
	App                    string
	Environment            string
	CIRepository           string
	CIRunID                string
	CIRunAttempt           string
	ExpiresAt              time.Time
	AdmissionID            string
	RegistrationGeneration int64
	RegistrationRef        string
	IssuerSubject          string
	IssuerTokenID          string
	RegistrationMetadata   map[string]string
	State                  string
	RegisteredAt           *time.Time
	ClaimedAt              *time.Time
	SupersededAt           *time.Time
	Revision               int64
	ServiceRevision        int64
}

// ExternalDeploymentAdmissionState is a server-owned audit lifecycle. The
// receipt itself remains in the operation metadata; this small row provides a
// durable, queryable binding between an idempotent admission, its nonce, and
// its terminal operation without retaining raw nonce material.
type ExternalDeploymentAdmissionState string

const (
	ExternalDeploymentAdmissionInitiated        ExternalDeploymentAdmissionState = "initiated"
	ExternalDeploymentAdmissionNonceRegistering ExternalDeploymentAdmissionState = "nonce_registering"
	ExternalDeploymentAdmissionNonceReady       ExternalDeploymentAdmissionState = "nonce_ready"
	ExternalDeploymentAdmissionEvidenceClaimed  ExternalDeploymentAdmissionState = "evidence_claimed"
	ExternalDeploymentAdmissionCommitted        ExternalDeploymentAdmissionState = "committed"
	ExternalDeploymentAdmissionCleanupPending   ExternalDeploymentAdmissionState = "cleanup_pending"
	ExternalDeploymentAdmissionComplete         ExternalDeploymentAdmissionState = "complete"
	ExternalDeploymentAdmissionExpired          ExternalDeploymentAdmissionState = "expired"
)

type ExternalDeploymentAdmissionLifecycle struct {
	ID              string
	IdempotencyKey  string
	RequestDigest   string
	App             string
	Environment     string
	CIRepository    string
	State           ExternalDeploymentAdmissionState
	NonceID         string
	NonceGeneration int64
	RegistrationRef string
	OperationID     string
	FailureCode     string
}

// ExternalDeploymentNonceRegistration is the durable, redacted registration
// saga context for a v4 admission. It intentionally exposes only the nonce
// digest and registration metadata; raw nonce material is never persisted.
type ExternalDeploymentNonceRegistration struct {
	AdmissionID          string
	NonceID              string
	NonceSHA256          string
	CIRepository         string
	CIRunID              string
	CIRunAttempt         string
	Generation           int64
	RegistrationRef      string
	IssuerSubject        string
	IssuerTokenID        string
	RegistrationMetadata map[string]string
	State                string
	ExpiresAt            time.Time
	RegisteredAt         *time.Time
	ClaimedAt            *time.Time
	SupersededAt         *time.Time
	Revision             int64
	ServiceRevision      int64
}

// ExternalDeploymentCheckpointRef persists opaque evidence pointers only. It
// intentionally does not duplicate checkpoint payloads, deployment secrets,
// or raw nonce material in the control-plane database.
type ExternalDeploymentCheckpointRef struct {
	Phase          string
	CheckpointID   string
	AttemptID      string
	EvidenceRef    string
	EvidenceSHA256 string
}

// ExternalDeploymentServiceSnapshot is the immutable, hash-only projection of
// the Norn-owner evidence-service status response. It is deliberately stored
// separately from a receipt so recovery cannot substitute caller timestamps or
// cleanup claims for the service's observed state.
type ExternalDeploymentServiceSnapshot struct {
	AdmissionID         string
	SnapshotID          string
	SnapshotRef         string
	SnapshotSHA256      string
	RetryLineage        []string
	ReceiptDigest       string
	ProofDigest         string
	ClaimRevision       int64
	CommitRevision      int64
	CleanupRevision     int64
	CleanupIntentSHA256 string
	AbsenceProofSHA256  string
}

func (db *DB) RecordExternalDeploymentServiceSnapshot(ctx context.Context, snapshot ExternalDeploymentServiceSnapshot) error {
	if db == nil || db.Pool == nil || snapshot.AdmissionID == "" || snapshot.SnapshotID == "" || snapshot.SnapshotRef == "" || len(snapshot.SnapshotSHA256) != 64 || len(snapshot.ReceiptDigest) != 64 || len(snapshot.ProofDigest) != 64 || snapshot.ClaimRevision < 1 || len(snapshot.RetryLineage) == 0 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	lineage, err := json.Marshal(snapshot.RetryLineage)
	if err != nil {
		return err
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET service_snapshot_id=$2, service_snapshot_ref=$3, service_snapshot_sha256=$4, service_claim_revision=$5, service_retry_lineage=$6::jsonb, service_receipt_sha256=$7, service_proof_sha256=$8, updated_at=now()
		WHERE id=$1 AND state IN ('nonce_ready','evidence_claimed') AND (service_snapshot_id='' OR (service_snapshot_id=$2 AND service_snapshot_ref=$3 AND service_snapshot_sha256=$4 AND service_claim_revision=$5 AND service_retry_lineage=$6::jsonb AND service_receipt_sha256=$7 AND service_proof_sha256=$8))`, snapshot.AdmissionID, snapshot.SnapshotID, snapshot.SnapshotRef, snapshot.SnapshotSHA256, snapshot.ClaimRevision, string(lineage), snapshot.ReceiptDigest, snapshot.ProofDigest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

func (db *DB) RecordExternalDeploymentCleanupBindings(ctx context.Context, snapshot ExternalDeploymentServiceSnapshot) error {
	if db == nil || db.Pool == nil || snapshot.AdmissionID == "" || snapshot.CommitRevision < 1 || snapshot.CleanupRevision <= snapshot.CommitRevision || len(snapshot.CleanupIntentSHA256) != 64 || len(snapshot.AbsenceProofSHA256) != 64 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	// Commit writes the intent first. Cleanup can only append its absence proof
	// against that exact commit revision; it can never replace the intent.
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET service_cleanup_revision=$3, absence_proof_sha256=$5, updated_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending') AND service_commit_revision=$2 AND cleanup_intent_sha256=$4
			AND (absence_proof_sha256='' OR (service_cleanup_revision=$3 AND absence_proof_sha256=$5))`, snapshot.AdmissionID, snapshot.CommitRevision, snapshot.CleanupRevision, snapshot.CleanupIntentSHA256, snapshot.AbsenceProofSHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

// RecordExternalDeploymentCommitBinding records the service CAS result before
// cleanup evidence exists.  Absence proof is intentionally not invented at
// commit time: it is supplied only by the later protected cleanup transition.
func (db *DB) RecordExternalDeploymentCommitBinding(ctx context.Context, snapshot ExternalDeploymentServiceSnapshot) error {
	if db == nil || db.Pool == nil || snapshot.AdmissionID == "" || snapshot.CommitRevision < 1 || len(snapshot.CleanupIntentSHA256) != 64 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET service_commit_revision=$2, cleanup_intent_sha256=$3, updated_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending') AND (cleanup_intent_sha256='' OR (service_commit_revision=0 AND cleanup_intent_sha256=$3) OR (service_commit_revision=$2 AND cleanup_intent_sha256=$3))`, snapshot.AdmissionID, snapshot.CommitRevision, snapshot.CleanupIntentSHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

// RecordExternalDeploymentCommitPending makes a post-operation remote commit
// recoverable. It stores the deterministic intent before any network call;
// a later authenticated reconciliation can reconstruct only the exact CAS.
func (db *DB) RecordExternalDeploymentCommitPending(ctx context.Context, admissionID, cleanupIntentSHA256 string) error {
	if db == nil || db.Pool == nil || admissionID == "" || len(cleanupIntentSHA256) != 64 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET cleanup_intent_sha256=$2, updated_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending') AND (cleanup_intent_sha256='' OR cleanup_intent_sha256=$2)`, admissionID, cleanupIntentSHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

// BeginExternalDeploymentAdmission creates (or retrieves) the durable
// idempotency/lifecycle record before a nonce is registered or external facts
// are checked. Exact retries return the existing row. A key can never be
// rebound to different logical deployment material.
func (db *DB) BeginExternalDeploymentAdmission(ctx context.Context, admissionID, idempotencyKey, requestDigest, app, environment, ciRepository string) (*ExternalDeploymentAdmissionLifecycle, error) {
	if db == nil || db.Pool == nil || admissionID == "" || idempotencyKey == "" || requestDigest == "" || app == "" || environment == "" || ciRepository == "" {
		return nil, ErrExternalDeploymentAdmissionUnavailable
	}
	row := db.Pool.QueryRow(ctx, `INSERT INTO external_deployment_admissions
		(id, idempotency_key, request_digest, app, environment, ci_repository, state)
		VALUES ($1,$2,$3,$4,$5,$6,'initiated')
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, idempotency_key, request_digest, app, environment, ci_repository, state, COALESCE(nonce_id,''), nonce_generation, registration_ref, COALESCE(operation_id,''), failure_code`,
		admissionID, idempotencyKey, requestDigest, app, environment, ciRepository)
	admission, err := scanExternalDeploymentAdmission(row)
	if err == nil {
		return admission, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		return nil, err
	}
	admission, err = scanExternalDeploymentAdmission(db.Pool.QueryRow(ctx, `SELECT id, idempotency_key, request_digest, app, environment, ci_repository, state, COALESCE(nonce_id,''), nonce_generation, registration_ref, COALESCE(operation_id,''), failure_code
		FROM external_deployment_admissions WHERE idempotency_key=$1`, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		// A conflicting primary-key insert may have won while the index lookup
		// was resolving. Do not turn that ambiguity into a new admission.
		return nil, ErrExternalDeploymentIdempotencyConflict
	}
	if err != nil {
		return nil, err
	}
	if admission.RequestDigest != requestDigest || admission.App != app || admission.Environment != environment || admission.CIRepository != ciRepository {
		return nil, ErrExternalDeploymentIdempotencyConflict
	}
	return admission, nil
}

// GetExternalDeploymentAdmission returns the server-owned lifecycle for an
// already-authorized application identity. The caller supplies every binding
// rather than looking it up by an unscoped ID.
func (db *DB) GetExternalDeploymentAdmission(ctx context.Context, admissionID, app, environment, ciRepository string) (*ExternalDeploymentAdmissionLifecycle, error) {
	if db == nil || db.Pool == nil || admissionID == "" || app == "" || environment == "" || ciRepository == "" {
		return nil, ErrExternalDeploymentAdmissionUnavailable
	}
	admission, err := scanExternalDeploymentAdmission(db.Pool.QueryRow(ctx, `SELECT id, idempotency_key, request_digest, app, environment, ci_repository, state, COALESCE(nonce_id,''), nonce_generation, registration_ref, COALESCE(operation_id,''), failure_code
		FROM external_deployment_admissions WHERE id=$1 AND app=$2 AND environment=$3 AND ci_repository=$4`, admissionID, app, environment, ciRepository))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrExternalDeploymentAdmissionUnavailable
	}
	return admission, err
}

// GetExternalDeploymentNonceRegistration returns durable registration state
// so remote register/claim/cleanup calls can be retried from an exact local
// generation and reference. It is deliberately scoped by admission ID.
func (db *DB) GetExternalDeploymentNonceRegistration(ctx context.Context, admissionID, nonceID string) (*ExternalDeploymentNonceRegistration, error) {
	if db == nil || db.Pool == nil || admissionID == "" || nonceID == "" {
		return nil, ErrExternalDeploymentAdmissionUnavailable
	}
	var registration ExternalDeploymentNonceRegistration
	var metadata []byte
	err := db.Pool.QueryRow(ctx, `SELECT admission_id, id, nonce_sha256, ci_repository, ci_run_id, ci_run_attempt, registration_generation, registration_ref,
		issuer_subject, issuer_token_id, registration_metadata, state, expires_at, registered_at, claimed_at, superseded_at, revision
		FROM external_deployment_nonces WHERE admission_id=$1 AND id=$2`, admissionID, nonceID).Scan(
		&registration.AdmissionID, &registration.NonceID, &registration.NonceSHA256, &registration.CIRepository, &registration.CIRunID, &registration.CIRunAttempt, &registration.Generation, &registration.RegistrationRef,
		&registration.IssuerSubject, &registration.IssuerTokenID, &metadata, &registration.State, &registration.ExpiresAt,
		&registration.RegisteredAt, &registration.ClaimedAt, &registration.SupersededAt, &registration.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrExternalDeploymentAdmissionUnavailable
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(metadata, &registration.RegistrationMetadata); err != nil {
		return nil, fmt.Errorf("decode external deployment nonce registration metadata: %w", err)
	}
	if raw := registration.RegistrationMetadata["serviceRevision"]; raw != "" {
		if _, err := fmt.Sscan(raw, &registration.ServiceRevision); err != nil || registration.ServiceRevision < 1 {
			return nil, ErrExternalDeploymentAdmissionUnavailable
		}
	}
	return &registration, nil
}

// ClaimExternalDeploymentAdmissionEvidence atomically moves a ready nonce
// into evidence verification. It is deliberately monotonic and idempotent so
// an API retry cannot move an admission backwards.
func (db *DB) ClaimExternalDeploymentAdmissionEvidence(ctx context.Context, admissionID, nonceID string) error {
	if db == nil || db.Pool == nil || admissionID == "" || nonceID == "" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE external_deployment_nonces SET state='claimed', claimed_at=now(), revision=revision+1
		WHERE id=$1 AND admission_id=$2 AND state='ready' AND expires_at > now()`, nonceID, admissionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return externalDeploymentEvidenceAlreadyClaimed(ctx, tx, admissionID, nonceID)
	}
	tag, err = tx.Exec(ctx, `UPDATE external_deployment_admissions SET state='evidence_claimed', updated_at=now()
		WHERE id=$1 AND nonce_id=$2 AND state='nonce_ready'`, admissionID, nonceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return externalDeploymentEvidenceAlreadyClaimed(ctx, tx, admissionID, nonceID)
	}
	return tx.Commit(ctx)
}

func externalDeploymentEvidenceAlreadyClaimed(ctx context.Context, tx pgx.Tx, admissionID, nonceID string) error {
	var admissionState, nonceState string
	err := tx.QueryRow(ctx, `SELECT admission.state, nonce.state
		FROM external_deployment_admissions admission
		JOIN external_deployment_nonces nonce ON nonce.id=admission.nonce_id
		WHERE admission.id=$1 AND nonce.id=$2 AND nonce.expires_at > now() FOR KEY SHARE`, admissionID, nonceID).Scan(&admissionState, &nonceState)
	if err != nil || admissionState != string(ExternalDeploymentAdmissionEvidenceClaimed) || nonceState != "claimed" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return tx.Commit(ctx)
}

// MarkExternalDeploymentNonceReady is called only after the external nonce
// registration write has succeeded. It is the registration-before-disclosure
// barrier: callers must not return the raw nonce until this transaction commits.
func (db *DB) MarkExternalDeploymentNonceReady(ctx context.Context, admissionID, nonceID string, generation int64, registrationRef string, serviceRevision int64) error {
	if db == nil || db.Pool == nil || admissionID == "" || nonceID == "" || generation <= 0 || registrationRef == "" || serviceRevision < 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE external_deployment_nonces SET state='ready', registered_at=now(), revision=revision+1, registration_metadata=registration_metadata || jsonb_build_object('serviceRevision',$5::text)
		WHERE id=$1 AND admission_id=$2 AND registration_generation=$3 AND registration_ref=$4 AND state='registering' AND expires_at > now()`, nonceID, admissionID, generation, registrationRef, fmt.Sprint(serviceRevision))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return externalDeploymentNonceAlreadyReady(ctx, tx, admissionID, nonceID, generation, registrationRef)
	}
	tag, err = tx.Exec(ctx, `UPDATE external_deployment_admissions SET state='nonce_ready', updated_at=now()
		WHERE id=$1 AND nonce_id=$2 AND nonce_generation=$3 AND registration_ref=$4 AND state='nonce_registering'`, admissionID, nonceID, generation, registrationRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return externalDeploymentNonceAlreadyReady(ctx, tx, admissionID, nonceID, generation, registrationRef)
	}
	if _, err := tx.Exec(ctx, `UPDATE external_deployment_nonces SET state='superseded', superseded_at=now(), revision=revision+1
		WHERE admission_id=$1 AND id<>$2 AND state IN ('registering','ready')`, admissionID, nonceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func externalDeploymentNonceAlreadyReady(ctx context.Context, tx pgx.Tx, admissionID, nonceID string, generation int64, registrationRef string) error {
	var admissionState, nonceState string
	err := tx.QueryRow(ctx, `SELECT admission.state, nonce.state
		FROM external_deployment_admissions admission
		JOIN external_deployment_nonces nonce ON nonce.id=admission.nonce_id
		WHERE admission.id=$1 AND nonce.id=$2 AND admission.nonce_generation=$3 AND admission.registration_ref=$4
			AND nonce.registration_generation=$3 AND nonce.registration_ref=$4 AND nonce.expires_at > now() FOR KEY SHARE`, admissionID, nonceID, generation, registrationRef).Scan(&admissionState, &nonceState)
	if err != nil || admissionState != string(ExternalDeploymentAdmissionNonceReady) || nonceState != "ready" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return tx.Commit(ctx)
}

// MarkExternalDeploymentAdmissionCleanupPending and
// CompleteExternalDeploymentAdmission make post-commit cleanup durable. A
// caller can retry cleanup without reopening an evidence claim or receipt.
func (db *DB) MarkExternalDeploymentAdmissionCleanupPending(ctx context.Context, admissionID string) error {
	if db == nil || db.Pool == nil || admissionID == "" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET state='cleanup_pending', updated_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending')`, admissionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

func (db *DB) CompleteExternalDeploymentAdmission(ctx context.Context, admissionID string) error {
	if db == nil || db.Pool == nil || admissionID == "" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_admissions SET state='complete', updated_at=now(), completed_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending','complete')`, admissionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return nil
}

// CompleteExternalDeploymentAdmissionWithCleanupCheckpoint writes the final
// service-owned absence checkpoint and lifecycle state in one transaction.
// The checkpoint is never synthesized at admission time.
func (db *DB) CompleteExternalDeploymentAdmissionWithCleanupCheckpoint(ctx context.Context, admissionID string, checkpoint ExternalDeploymentCheckpointRef) error {
	if db == nil || db.Pool == nil || admissionID == "" || checkpoint.Phase != "external_cleanup" || !validExternalDeploymentCheckpointRef(checkpoint) {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := upsertExternalDeploymentCheckpointRef(ctx, tx, "external_deployment_admission_checkpoints", admissionID, checkpoint); err != nil {
		return err
	}
	if err := upsertExternalDeploymentCheckpointRef(ctx, tx, "fleet_runner_checkpoint_refs", admissionID, checkpoint); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE external_deployment_admissions SET state='complete', updated_at=now(), completed_at=now()
		WHERE id=$1 AND state IN ('committed','cleanup_pending','complete')`, admissionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return tx.Commit(ctx)
}

// ExpireExternalDeploymentAdmission records a safe terminal expiry without
// replacing an already-committed operation.
func (db *DB) ExpireExternalDeploymentAdmission(ctx context.Context, admissionID, failureCode string) error {
	if db == nil || db.Pool == nil || admissionID == "" {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE external_deployment_nonces SET state='expired', revision=revision+1
		WHERE admission_id=$1 AND state IN ('registering','ready','claimed')`, admissionID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE external_deployment_admissions SET state='expired', failure_code=$2, updated_at=now(), completed_at=now()
		WHERE id=$1 AND state IN ('initiated','nonce_registering','nonce_ready','evidence_claimed')`, admissionID, failureCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExternalDeploymentAdmissionUnavailable
	}
	return tx.Commit(ctx)
}

type externalDeploymentAdmissionScanner interface {
	Scan(...any) error
}

func scanExternalDeploymentAdmission(row externalDeploymentAdmissionScanner) (*ExternalDeploymentAdmissionLifecycle, error) {
	var admission ExternalDeploymentAdmissionLifecycle
	if err := row.Scan(&admission.ID, &admission.IdempotencyKey, &admission.RequestDigest, &admission.App, &admission.Environment, &admission.CIRepository, &admission.State, &admission.NonceID, &admission.NonceGeneration, &admission.RegistrationRef, &admission.OperationID, &admission.FailureCode); err != nil {
		return nil, err
	}
	return &admission, nil
}

func (db *DB) IssueExternalDeploymentNonce(ctx context.Context, nonce ExternalDeploymentNonce) error {
	if db == nil || db.Pool == nil || nonce.ID == "" || nonce.NonceSHA256 == "" || nonce.App == "" || nonce.Environment == "" || nonce.CIRepository == "" || nonce.CIRunID == "" || nonce.CIRunAttempt == "" || nonce.ExpiresAt.IsZero() {
		return fmt.Errorf("external deployment nonce store is unavailable")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Remove expired rows whether or not they were consumed. Issuance performs
	// this bounded maintenance so abandoned pilot runs cannot accumulate state.
	if _, err := tx.Exec(ctx, `DELETE FROM external_deployment_nonces WHERE ctid IN (SELECT ctid FROM external_deployment_nonces nonce WHERE expires_at < now() AND NOT EXISTS (SELECT 1 FROM external_deployment_admissions admission WHERE admission.nonce_id=nonce.id) LIMIT 1000)`); err != nil {
		return err
	}
	// Serialize the per-run count-and-insert decision. Advisory-lock collisions
	// only make independent issuances wait; they cannot exceed the cap.
	scope := fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s%d:%s", len(nonce.App), nonce.App, len(nonce.Environment), nonce.Environment, len(nonce.CIRepository), nonce.CIRepository, len(nonce.CIRunID), nonce.CIRunID, len(nonce.CIRunAttempt), nonce.CIRunAttempt)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, scope); err != nil {
		return err
	}
	var outstanding int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM external_deployment_nonces WHERE app=$1 AND environment=$2 AND ci_repository=$3 AND ci_run_id=$4 AND ci_run_attempt=$5 AND consumed_at IS NULL AND expires_at > now() AND state IN ('registering','ready')`, nonce.App, nonce.Environment, nonce.CIRepository, nonce.CIRunID, nonce.CIRunAttempt).Scan(&outstanding); err != nil {
		return err
	}
	if outstanding >= externalDeploymentNonceMaxOutstandingPerRun {
		return ErrExternalDeploymentNonceLimit
	}
	registrationMetadata, err := json.Marshal(nonce.RegistrationMetadata)
	if err != nil {
		return fmt.Errorf("encode external deployment nonce registration metadata: %w", err)
	}
	if nonce.AdmissionID != "" {
		if nonce.RegistrationGeneration <= 0 || nonce.RegistrationRef == "" {
			return ErrExternalDeploymentAdmissionUnavailable
		}
		var state ExternalDeploymentAdmissionState
		if err := tx.QueryRow(ctx, `SELECT state FROM external_deployment_admissions WHERE id=$1 AND app=$2 AND environment=$3 AND ci_repository=$4 FOR UPDATE`, nonce.AdmissionID, nonce.App, nonce.Environment, nonce.CIRepository).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrExternalDeploymentAdmissionUnavailable
			}
			return err
		}
		if state != ExternalDeploymentAdmissionInitiated && state != ExternalDeploymentAdmissionNonceRegistering && state != ExternalDeploymentAdmissionNonceReady {
			return ErrExternalDeploymentAdmissionUnavailable
		}
		// The admission pointer moves to the successor registering row, so the
		// old nonce cannot be consumed. It remains locally intact until the
		// successor remote registration is recorded by MarkReady.
	}
	if _, err := tx.Exec(ctx, `INSERT INTO external_deployment_nonces
		(id, nonce_sha256, app, environment, ci_repository, ci_run_id, ci_run_attempt, expires_at, admission_id, registration_generation, registration_ref, issuer_subject, issuer_token_id, registration_metadata, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,CASE WHEN $9<>'' THEN 'registering' ELSE 'ready' END)`, nonce.ID, nonce.NonceSHA256, nonce.App, nonce.Environment, nonce.CIRepository, nonce.CIRunID, nonce.CIRunAttempt, nonce.ExpiresAt, nonce.AdmissionID, nonce.RegistrationGeneration, nonce.RegistrationRef, nonce.IssuerSubject, nonce.IssuerTokenID, registrationMetadata); err != nil {
		return err
	}
	if nonce.AdmissionID != "" {
		tag, err := tx.Exec(ctx, `UPDATE external_deployment_admissions
			SET state='nonce_registering', nonce_id=$2, nonce_generation=$3, registration_ref=$4, updated_at=now()
			WHERE id=$1 AND state IN ('initiated','nonce_registering','nonce_ready')`, nonce.AdmissionID, nonce.ID, nonce.RegistrationGeneration, nonce.RegistrationRef)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrExternalDeploymentAdmissionUnavailable
		}
	}
	return tx.Commit(ctx)
}

// ConsumeExternalDeploymentNonce is the final compare-and-set before durable
// admission. A verifier may read live evidence before this call, but only one
// caller can consume the nonce and create the corresponding receipt.
func (db *DB) ConsumeExternalDeploymentNonce(ctx context.Context, nonce ExternalDeploymentNonce) (bool, error) {
	if db == nil || db.Pool == nil || nonce.ID == "" || nonce.NonceSHA256 == "" || nonce.App == "" || nonce.Environment == "" || nonce.CIRepository == "" || nonce.CIRunID == "" || nonce.CIRunAttempt == "" {
		return false, fmt.Errorf("external deployment nonce store is unavailable")
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_nonces SET consumed_at=now(), state='claimed', claimed_at=COALESCE(claimed_at, now()), revision=revision+1 WHERE id=$1 AND nonce_sha256=$2 AND app=$3 AND environment=$4 AND ci_repository=$5 AND ci_run_id=$6 AND ci_run_attempt=$7 AND state='ready' AND consumed_at IS NULL AND expires_at > now()`, nonce.ID, nonce.NonceSHA256, nonce.App, nonce.Environment, nonce.CIRepository, nonce.CIRunID, nonce.CIRunAttempt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ExternalDeploymentAdmission is the complete terminal write set for a
// verifier-approved direct Fleet workload. The raw nonce never reaches this
// type; only its SHA-256 binding is accepted.
type ExternalDeploymentAdmission struct {
	Nonce           ExternalDeploymentNonce
	Deployment      *model.Deployment
	Regions         []model.DeploymentRegion
	Operation       *model.Operation
	IdempotencyKey  string
	RequestDigest   string
	AdmissionID     string
	NonceGeneration int64
	CheckpointRefs  []ExternalDeploymentCheckpointRef
	ServiceSnapshot ExternalDeploymentServiceSnapshot
}

type ExternalDeploymentAdmissionResult struct {
	Operation *model.Operation
	Replayed  bool
}

// AdmitExternalDeployment atomically consumes the one-use nonce and writes a
// terminal deployment, its verified regional truth, and its terminal
// operation. A crash at any point rolls all of it back. Replays are exact: the
// same idempotency key and request digest return the original operation;
// another request never gets to consume the nonce after that operation exists.
func (db *DB) AdmitExternalDeployment(ctx context.Context, admission ExternalDeploymentAdmission) (*ExternalDeploymentAdmissionResult, error) {
	if db == nil || db.Pool == nil || admission.Deployment == nil || admission.Operation == nil || admission.IdempotencyKey == "" || admission.RequestDigest == "" || admission.Nonce.ID == "" || admission.Nonce.NonceSHA256 == "" || admission.Nonce.App == "" || admission.Nonce.Environment == "" || admission.Nonce.CIRepository == "" || admission.Nonce.CIRunID == "" || admission.Nonce.CIRunAttempt == "" || !admission.Operation.Status.Terminal() || admission.Deployment.FinishedAt == nil || admission.Operation.FinishedAt == nil || len(admission.Regions) == 0 || (admission.AdmissionID != "" && (admission.NonceGeneration <= 0 || admission.ServiceSnapshot.SnapshotID == "" || len(admission.ServiceSnapshot.SnapshotSHA256) != 64 || len(admission.ServiceSnapshot.ReceiptDigest) != 64 || len(admission.ServiceSnapshot.ProofDigest) != 64 || len(admission.ServiceSnapshot.CleanupIntentSHA256) != 64)) || (admission.AdmissionID == "" && admission.NonceGeneration != 0) {
		return nil, fmt.Errorf("external deployment admission store is unavailable")
	}
	for _, checkpoint := range admission.CheckpointRefs {
		if !validExternalDeploymentCheckpointRef(checkpoint) {
			return nil, fmt.Errorf("external deployment checkpoint reference is invalid")
		}
	}
	payload, metadata, err := prepareOperation(admission.Operation)
	if err != nil {
		return nil, err
	}
	changes, err := json.Marshal(admission.Deployment.SourceChanges)
	if err != nil {
		return nil, fmt.Errorf("encode external deployment source changes: %w", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Bound expiry cleanup prevents one nonce row per pilot run from becoming
	// permanent state. It is intentionally best-effort and cannot affect live
	// rows because it selects only expired entries.
	_, _ = tx.Exec(ctx, `DELETE FROM external_deployment_nonces WHERE ctid IN (SELECT ctid FROM external_deployment_nonces nonce WHERE expires_at < now() AND NOT EXISTS (SELECT 1 FROM external_deployment_admissions admission WHERE admission.nonce_id=nonce.id) LIMIT 1000)`)
	var existingID, existingDigest string
	err = tx.QueryRow(ctx, `SELECT id, COALESCE(metadata->>'requestDigest','') FROM operations WHERE metadata->>'idempotencyKey'=$1 FOR KEY SHARE`, admission.IdempotencyKey).Scan(&existingID, &existingDigest)
	if err == nil {
		if existingDigest != admission.RequestDigest {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		if err := completeExternalDeploymentAdmission(ctx, tx, admission, existingID); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		op, err := db.GetOperation(ctx, existingID)
		if err != nil {
			return nil, err
		}
		return &ExternalDeploymentAdmissionResult{Operation: op, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if admission.AdmissionID != "" {
		var snapshotID, snapshotRef, snapshotSHA, receiptDigest, proofDigest string
		var claimRevision int64
		err := tx.QueryRow(ctx, `SELECT service_snapshot_id, service_snapshot_ref, service_snapshot_sha256, service_receipt_sha256, service_proof_sha256, service_claim_revision
			FROM external_deployment_admissions WHERE id=$1 AND nonce_id=$2 AND nonce_generation=$3 AND state IN ('nonce_ready','evidence_claimed') FOR KEY SHARE`, admission.AdmissionID, admission.Nonce.ID, admission.NonceGeneration).Scan(&snapshotID, &snapshotRef, &snapshotSHA, &receiptDigest, &proofDigest, &claimRevision)
		if err != nil || snapshotID != admission.ServiceSnapshot.SnapshotID || snapshotRef != admission.ServiceSnapshot.SnapshotRef || snapshotSHA != admission.ServiceSnapshot.SnapshotSHA256 || receiptDigest != admission.ServiceSnapshot.ReceiptDigest || proofDigest != admission.ServiceSnapshot.ProofDigest || claimRevision != admission.ServiceSnapshot.ClaimRevision {
			return nil, ErrExternalDeploymentAdmissionUnavailable
		}
	}

	var nonceID string
	err = tx.QueryRow(ctx, `UPDATE external_deployment_nonces SET consumed_at=now(), state='claimed', claimed_at=COALESCE(claimed_at, now()), revision=revision+1
		WHERE id=$1 AND nonce_sha256=$2 AND app=$3 AND environment=$4 AND ci_repository=$5 AND ci_run_id=$6 AND ci_run_attempt=$7 AND consumed_at IS NULL AND expires_at > now()
			AND ($8='' OR (admission_id=$8 AND registration_generation=$9))
			AND (($8='' AND state='ready') OR ($8<>'' AND state='claimed'))
		RETURNING id`, admission.Nonce.ID, admission.Nonce.NonceSHA256, admission.Nonce.App, admission.Nonce.Environment, admission.Nonce.CIRepository, admission.Nonce.CIRunID, admission.Nonce.CIRunAttempt, admission.AdmissionID, admission.NonceGeneration).Scan(&nonceID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another matching request may have committed while this transaction was
		// waiting on the nonce row. In read-committed mode this fresh query sees
		// the durable terminal receipt and returns only an exact replay.
		var replayID, replayDigest string
		replayErr := tx.QueryRow(ctx, `SELECT id, COALESCE(metadata->>'requestDigest','') FROM operations WHERE metadata->>'idempotencyKey'=$1`, admission.IdempotencyKey).Scan(&replayID, &replayDigest)
		if replayErr == nil && replayDigest == admission.RequestDigest {
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			op, err := db.GetOperation(ctx, replayID)
			if err != nil {
				return nil, err
			}
			return &ExternalDeploymentAdmissionResult{Operation: op, Replayed: true}, nil
		}
		if replayErr == nil {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		return nil, ErrExternalDeploymentNonceConsumed
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deployments (id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, admission.Deployment.ID, admission.Deployment.App, admission.Deployment.CommitSHA, admission.Deployment.ImageTag, admission.Deployment.Environment, admission.Deployment.SagaID, admission.Deployment.Status, admission.Deployment.SourceKind, admission.Deployment.SourceRef, admission.Deployment.SourceDirty, changes, admission.Deployment.StartedAt, admission.Deployment.FinishedAt); err != nil {
		return nil, err
	}
	for _, region := range admission.Regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions (deployment_id, region, nomad_region, status, desired_weight, active_weight, eval_id, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,now())`, admission.Deployment.ID, region.Region, region.NomadRegion, region.Status, region.DesiredWeight, region.ActiveWeight, region.EvalID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),$16)`, admission.Operation.ID, admission.Operation.Kind, admission.Operation.App, admission.Operation.SagaID, admission.Operation.Ref, admission.Operation.Status, admission.Operation.Risk, admission.Operation.Source, admission.Operation.Message, payload, metadata, admission.Operation.Attempts, admission.Operation.MaxAttempts, admission.Operation.NextAttemptAt, admission.Operation.StartedAt, admission.Operation.FinishedAt); err != nil {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		return nil, err
	}
	if err := completeExternalDeploymentAdmission(ctx, tx, admission, admission.Operation.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ExternalDeploymentAdmissionResult{Operation: admission.Operation}, nil
}

func validExternalDeploymentCheckpointRef(ref ExternalDeploymentCheckpointRef) bool {
	for _, value := range []string{ref.Phase, ref.CheckpointID, ref.AttemptID, ref.EvidenceRef} {
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	if len(ref.EvidenceSHA256) != 64 || strings.ContainsAny(ref.EvidenceSHA256, "\x00\r\n") {
		return false
	}
	for _, c := range ref.EvidenceSHA256 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func completeExternalDeploymentAdmission(ctx context.Context, tx pgx.Tx, admission ExternalDeploymentAdmission, operationID string) error {
	if admission.AdmissionID == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE external_deployment_admissions
		SET state='committed', operation_id=$2, cleanup_intent_sha256=$7, failure_code='', updated_at=now(), completed_at=NULL
		WHERE id=$1 AND idempotency_key=$3 AND request_digest=$4 AND nonce_id=$5 AND nonce_generation=$6
			AND state='evidence_claimed' AND (cleanup_intent_sha256='' OR cleanup_intent_sha256=$7)`, admission.AdmissionID, operationID, admission.IdempotencyKey, admission.RequestDigest, admission.Nonce.ID, admission.NonceGeneration, admission.ServiceSnapshot.CleanupIntentSHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var existingOperationID string
		err := tx.QueryRow(ctx, `SELECT operation_id FROM external_deployment_admissions
			WHERE id=$1 AND idempotency_key=$2 AND request_digest=$3 AND nonce_id=$4 AND nonce_generation=$5 AND state IN ('committed','cleanup_pending','complete')`, admission.AdmissionID, admission.IdempotencyKey, admission.RequestDigest, admission.Nonce.ID, admission.NonceGeneration).Scan(&existingOperationID)
		if err != nil || existingOperationID != operationID {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrExternalDeploymentAdmissionUnavailable
			}
			return err
		}
	}
	for _, checkpoint := range admission.CheckpointRefs {
		if err := upsertExternalDeploymentCheckpointRef(ctx, tx, "external_deployment_admission_checkpoints", admission.AdmissionID, checkpoint); err != nil {
			return err
		}
		if err := upsertExternalDeploymentCheckpointRef(ctx, tx, "fleet_runner_checkpoint_refs", admission.AdmissionID, checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// upsertExternalDeploymentCheckpointRef accepts a replay only when every
// immutable evidence pointer matches. DO NOTHING would silently accept a
// conflicting checkpoint and make a corrupted retry look successful.
func upsertExternalDeploymentCheckpointRef(ctx context.Context, tx pgx.Tx, table, admissionID string, checkpoint ExternalDeploymentCheckpointRef) error {
	if table != "external_deployment_admission_checkpoints" && table != "fleet_runner_checkpoint_refs" {
		return ErrExternalDeploymentCheckpointConflict
	}
	var phase string
	err := tx.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %s
		(admission_id, phase, checkpoint_id, attempt_id, evidence_ref, evidence_sha256)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (admission_id, phase) DO UPDATE SET phase=EXCLUDED.phase
		WHERE %s.checkpoint_id=EXCLUDED.checkpoint_id
			AND %s.attempt_id=EXCLUDED.attempt_id
			AND %s.evidence_ref=EXCLUDED.evidence_ref
			AND %s.evidence_sha256=EXCLUDED.evidence_sha256
		RETURNING phase`, table, table, table, table, table), admissionID, checkpoint.Phase, checkpoint.CheckpointID, checkpoint.AttemptID, checkpoint.EvidenceRef, checkpoint.EvidenceSHA256).Scan(&phase)
	if err == nil && phase == checkpoint.Phase {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExternalDeploymentCheckpointConflict
	}
	if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
		return ErrExternalDeploymentCheckpointConflict
	}
	return err
}
