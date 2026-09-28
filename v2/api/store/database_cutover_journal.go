package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/cutover"
)

var errDatabaseCutoverJournalConflict = errors.New("database cutover journal revision or identity conflict")

const DatabaseCutoverOperationKind = "database.cutover"

// PrepareClaimedDatabaseCutoverJournal binds the first private PG journal row
// to a verified signed operation and a currently held claim. It records no
// source fence, target restore or consumer authority.
func (db *DB) PrepareClaimedDatabaseCutoverJournal(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, intent cutover.Intent) (cutover.Journal, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil || intent.OperationID != claim.OperationID() {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	digest, err := cutover.IntentSHA256(intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return cutover.Journal{}, err
	}
	if accepted.Operation.Kind != DatabaseCutoverOperationKind || accepted.Operation.ID != intent.OperationID || accepted.Operation.App != intent.App || accepted.Operation.Ref != intent.CandidateRelease || accepted.Operation.MaxAttempts != 1 || accepted.Deployment != nil || accepted.Intent.OperationID != intent.OperationID || accepted.Operation.Payload["cutoverIntentSha256"] != digest {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return cutover.Journal{}, err
	}
	defer tx.Rollback(context.Background())
	// Match catalog activation's lock order and hold the catalog fixed until
	// the claimed journal row is committed.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
		return cutover.Journal{}, err
	}
	activeCatalog, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if err := cutover.VerifyCatalog(intent, activeCatalog.Revision, activeCatalog.Digest, activeCatalog.Catalog); err != nil {
		return cutover.Journal{}, err
	}
	var held bool
	err = tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND app=$3 AND ref=$4
		AND payload->>'cutoverIntentSha256'=$5 AND acceptance_required=true AND status='running'
		AND locked_by=$6 AND lock_generation=$7 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), DatabaseCutoverOperationKind, intent.App, intent.CandidateRelease, digest, claim.OwnerID(), claim.Generation()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return cutover.Journal{}, ownershipLost(claim)
	}
	if err != nil || !held {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO database_cutover_journals
		(operation_id,app,logical_database,intent,intent_sha256)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		intent.OperationID, intent.App, intent.LogicalDatabase, encoded, digest); err != nil {
		return cutover.Journal{}, err
	}
	var savedIntent, receipts, evidenceReferences []byte
	var savedDigest, phase string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT intent,intent_sha256,phase,revision,receipts,evidence_references FROM database_cutover_journals
		WHERE operation_id=$1 FOR UPDATE`, intent.OperationID).Scan(&savedIntent, &savedDigest, &phase, &revision, &receipts, &evidenceReferences); err != nil {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	var stored cutover.Intent
	if json.Unmarshal(savedIntent, &stored) != nil || stored != intent || savedDigest != digest || phase != string(cutover.PhasePrepare) || revision != 1 || string(receipts) != "{}" || string(evidenceReferences) != "{}" {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return cutover.Journal{}, err
	}
	return cutover.New(intent)
}

// prepareDatabaseCutoverJournal is deliberately private until accepted
// operation, app-lock and source-writer fencing are joined to this storage
// path. It does no external effect and grants no consumer authority.
func (db *DB) prepareDatabaseCutoverJournal(ctx context.Context, intent cutover.Intent) (cutover.Journal, error) {
	_, err := cutover.New(intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	if db == nil || db.Pool == nil {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	digest := sha256.Sum256(encoded)
	_, err = db.Pool.Exec(ctx, `INSERT INTO database_cutover_journals
		(operation_id,app,logical_database,intent,intent_sha256)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		intent.OperationID, intent.App, intent.LogicalDatabase, encoded, hex.EncodeToString(digest[:]))
	if err != nil {
		return cutover.Journal{}, err
	}
	saved, savedDigest, err := db.loadDatabaseCutoverJournal(ctx, intent.OperationID)
	if err != nil || savedDigest != hex.EncodeToString(digest[:]) || saved.Intent != intent || saved.Phase != cutover.PhasePrepare || saved.Revision != 1 || len(saved.Receipts) != 0 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	return saved, nil
}

func (db *DB) loadDatabaseCutoverJournal(ctx context.Context, operationID string) (cutover.Journal, string, error) {
	if db == nil || db.Pool == nil || operationID == "" {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	var intentJSON, receiptsJSON, evidenceReferencesJSON []byte
	var digest, phase string
	var revision int64
	err := db.Pool.QueryRow(ctx, `SELECT intent,intent_sha256,phase,revision,receipts,evidence_references FROM database_cutover_journals
		WHERE operation_id=$1 AND retired_at IS NULL`, operationID).Scan(&intentJSON, &digest, &phase, &revision, &receiptsJSON, &evidenceReferencesJSON)
	if err != nil {
		return cutover.Journal{}, "", err
	}
	var j cutover.Journal
	if err := json.Unmarshal(intentJSON, &j.Intent); err != nil {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	if err := json.Unmarshal(receiptsJSON, &j.Receipts); err != nil || json.Unmarshal(evidenceReferencesJSON, &j.EvidenceReferences) != nil || revision < 1 || j.Intent.OperationID != operationID {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	canonical, err := json.Marshal(j.Intent)
	if err != nil {
		return cutover.Journal{}, "", err
	}
	actual := sha256.Sum256(canonical)
	if hex.EncodeToString(actual[:]) != digest {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	if _, err := cutover.New(j.Intent); err != nil {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	j.Phase, j.Revision = cutover.Phase(phase), uint64(revision)
	if j.Validate() != nil {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	return j, digest, nil
}

// advanceDatabaseCutoverJournal uses a row lock and revision CAS. Its receipt
// digest must be verified by a future coordinator against retained evidence;
// merely recording a digest cannot prove the source fence or target restore.
func (db *DB) advanceDatabaseCutoverJournal(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256 string) (cutover.Journal, error) {
	return db.advanceDatabaseCutoverJournalGuarded(ctx, operationID, expectedRevision, next, receiptSHA256, "", nil)
}

// AdvanceClaimedDatabaseCutoverJournal records one phase only while the
// signed operation and its current claim still identify this exact journal.
// External receipts remain unverified until a coordinator supplies a proof
// verifier; the method grants no cutover or consumer authority.
func (db *DB) AdvanceClaimedDatabaseCutoverJournal(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, expectedRevision uint64, evidence cutover.PhaseEvidenceReference) (cutover.Journal, error) {
	if db == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return cutover.Journal{}, err
	}
	digest, _ := accepted.Operation.Payload["cutoverIntentSha256"].(string)
	if accepted.Operation.Kind != DatabaseCutoverOperationKind || accepted.Operation.ID != claim.OperationID() || accepted.Operation.App == "" || accepted.Operation.Ref == "" || accepted.Operation.MaxAttempts != 1 || accepted.Deployment != nil || accepted.Intent.OperationID != claim.OperationID() || len(digest) != 64 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	return db.advanceDatabaseCutoverJournalGuardedWithEvidence(ctx, claim.OperationID(), expectedRevision, evidence, digest, func(tx pgx.Tx) error {
		var held bool
		err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND app=$3 AND ref=$4
			AND payload->>'cutoverIntentSha256'=$5 AND acceptance_required=true AND status='running'
			AND locked_by=$6 AND lock_generation=$7 AND locked_until>clock_timestamp() FOR UPDATE`,
			claim.OperationID(), DatabaseCutoverOperationKind, accepted.Operation.App, accepted.Operation.Ref, digest, claim.OwnerID(), claim.Generation()).Scan(&held)
		if errors.Is(err, pgx.ErrNoRows) {
			return ownershipLost(claim)
		}
		if err != nil || !held {
			return errDatabaseCutoverJournalConflict
		}
		return nil
	})
}

func (db *DB) advanceDatabaseCutoverJournalGuardedWithEvidence(ctx context.Context, operationID string, expectedRevision uint64, evidence cutover.PhaseEvidenceReference, expectedIntentDigest string, fence func(pgx.Tx) error) (cutover.Journal, error) {
	return db.advanceDatabaseCutoverJournalTransaction(ctx, operationID, expectedRevision, evidence.NextPhase, "", &evidence, expectedIntentDigest, fence)
}

func (db *DB) advanceDatabaseCutoverJournalGuarded(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256, expectedIntentDigest string, fence func(pgx.Tx) error) (cutover.Journal, error) {
	return db.advanceDatabaseCutoverJournalTransaction(ctx, operationID, expectedRevision, next, receiptSHA256, nil, expectedIntentDigest, fence)
}

func (db *DB) advanceDatabaseCutoverJournalTransaction(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256 string, evidence *cutover.PhaseEvidenceReference, expectedIntentDigest string, fence func(pgx.Tx) error) (cutover.Journal, error) {
	if db == nil || db.Pool == nil || expectedRevision == 0 || expectedRevision >= 1<<63 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return cutover.Journal{}, err
	}
	defer tx.Rollback(context.Background())
	var activeCatalog DatabaseCatalogRevision
	if expectedIntentDigest != "" && (next == cutover.PhaseQuiesce || next == cutover.PhaseFinalSync) {
		// Keep catalog activation ahead of operation/journal row locks.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
			return cutover.Journal{}, err
		}
		activeCatalog, err = loadActiveDatabaseCatalog(ctx, tx)
		if err != nil {
			return cutover.Journal{}, errDatabaseCutoverJournalConflict
		}
	}
	if fence != nil {
		if err := fence(tx); err != nil {
			return cutover.Journal{}, err
		}
	}
	var intentJSON, receiptsJSON, evidenceReferencesJSON []byte
	var digest, phase string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT intent,intent_sha256,phase,revision,receipts,evidence_references FROM database_cutover_journals
		WHERE operation_id=$1 AND retired_at IS NULL FOR UPDATE`, operationID).Scan(&intentJSON, &digest, &phase, &revision, &receiptsJSON, &evidenceReferencesJSON); err != nil {
		return cutover.Journal{}, err
	}
	var current cutover.Journal
	if json.Unmarshal(intentJSON, &current.Intent) != nil || json.Unmarshal(receiptsJSON, &current.Receipts) != nil || json.Unmarshal(evidenceReferencesJSON, &current.EvidenceReferences) != nil || revision < 1 || current.Intent.OperationID != operationID {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	canonical, err := json.Marshal(current.Intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	actual := sha256.Sum256(canonical)
	if hex.EncodeToString(actual[:]) != digest || (expectedIntentDigest != "" && digest != expectedIntentDigest) {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	current.Phase, current.Revision = cutover.Phase(phase), uint64(revision)
	if current.Validate() != nil {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if activeCatalog.Revision != 0 {
		if err := cutover.VerifyCatalog(current.Intent, activeCatalog.Revision, activeCatalog.Digest, activeCatalog.Catalog); err != nil {
			return cutover.Journal{}, err
		}
	}
	var nextJournal cutover.Journal
	if evidence != nil {
		if evidence.FromRevision != expectedRevision {
			return cutover.Journal{}, errDatabaseCutoverJournalConflict
		}
		nextJournal, err = current.AdvanceWithEvidence(*evidence)
	} else {
		nextJournal, err = current.Advance(expectedRevision, next, receiptSHA256)
	}
	if err != nil {
		return cutover.Journal{}, fmt.Errorf("%w: %v", errDatabaseCutoverJournalConflict, err)
	}
	receipts, err := json.Marshal(nextJournal.Receipts)
	if err != nil {
		return cutover.Journal{}, err
	}
	references, err := json.Marshal(nextJournal.EvidenceReferences)
	if err != nil {
		return cutover.Journal{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE database_cutover_journals SET phase=$2,revision=$3,receipts=$4,evidence_references=$5,updated_at=clock_timestamp()
		WHERE operation_id=$1 AND revision=$6 AND intent_sha256=$7 AND retired_at IS NULL`,
		operationID, nextJournal.Phase, int64(nextJournal.Revision), receipts, references, int64(expectedRevision), digest)
	if err != nil || tag.RowsAffected() != 1 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return cutover.Journal{}, err
	}
	return nextJournal, nil
}
