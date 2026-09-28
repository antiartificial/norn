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
	var intentJSON, receiptsJSON []byte
	var digest, phase string
	var revision int64
	err := db.Pool.QueryRow(ctx, `SELECT intent,intent_sha256,phase,revision,receipts FROM database_cutover_journals
		WHERE operation_id=$1 AND retired_at IS NULL`, operationID).Scan(&intentJSON, &digest, &phase, &revision, &receiptsJSON)
	if err != nil {
		return cutover.Journal{}, "", err
	}
	var j cutover.Journal
	if err := json.Unmarshal(intentJSON, &j.Intent); err != nil {
		return cutover.Journal{}, "", errDatabaseCutoverJournalConflict
	}
	if err := json.Unmarshal(receiptsJSON, &j.Receipts); err != nil || revision < 1 || j.Intent.OperationID != operationID {
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
	return j, digest, nil
}

// advanceDatabaseCutoverJournal uses a row lock and revision CAS. Its receipt
// digest must be verified by a future coordinator against retained evidence;
// merely recording a digest cannot prove the source fence or target restore.
func (db *DB) advanceDatabaseCutoverJournal(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256 string) (cutover.Journal, error) {
	if db == nil || db.Pool == nil || expectedRevision == 0 || expectedRevision >= 1<<63 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return cutover.Journal{}, err
	}
	defer tx.Rollback(context.Background())
	var intentJSON, receiptsJSON []byte
	var digest, phase string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT intent,intent_sha256,phase,revision,receipts FROM database_cutover_journals
		WHERE operation_id=$1 AND retired_at IS NULL FOR UPDATE`, operationID).Scan(&intentJSON, &digest, &phase, &revision, &receiptsJSON); err != nil {
		return cutover.Journal{}, err
	}
	var current cutover.Journal
	if json.Unmarshal(intentJSON, &current.Intent) != nil || json.Unmarshal(receiptsJSON, &current.Receipts) != nil || revision < 1 || current.Intent.OperationID != operationID {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	canonical, err := json.Marshal(current.Intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	actual := sha256.Sum256(canonical)
	if hex.EncodeToString(actual[:]) != digest {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	current.Phase, current.Revision = cutover.Phase(phase), uint64(revision)
	nextJournal, err := current.Advance(expectedRevision, next, receiptSHA256)
	if err != nil {
		return cutover.Journal{}, fmt.Errorf("%w: %v", errDatabaseCutoverJournalConflict, err)
	}
	receipts, err := json.Marshal(nextJournal.Receipts)
	if err != nil {
		return cutover.Journal{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE database_cutover_journals SET phase=$2,revision=$3,receipts=$4,updated_at=clock_timestamp()
		WHERE operation_id=$1 AND revision=$5 AND intent_sha256=$6 AND retired_at IS NULL`,
		operationID, nextJournal.Phase, int64(nextJournal.Revision), receipts, int64(expectedRevision), digest)
	if err != nil || tag.RowsAffected() != 1 {
		return cutover.Journal{}, errDatabaseCutoverJournalConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return cutover.Journal{}, err
	}
	return nextJournal, nil
}
