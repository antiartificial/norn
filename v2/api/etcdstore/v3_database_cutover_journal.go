package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/cutover"
)

var errEtcdCutoverJournalConflict = errors.New("database cutover journal revision or identity conflict")

type etcdCutoverJournalRecord struct {
	SchemaVersion string          `json:"schemaVersion"`
	IntentSHA256  string          `json:"intentSha256"`
	Journal       cutover.Journal `json:"journal"`
}

func (s *V3OperationStore) cutoverOperationKey(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return s.prefix + "/v3/database-cutover/operations/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) cutoverActiveResourceKey(app, logical string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + logical))
	return s.prefix + "/v3/database-cutover/active/" + hex.EncodeToString(sum[:])
}

func encodeEtcdCutoverJournal(j cutover.Journal) ([]byte, error) {
	intent, err := json.Marshal(j.Intent)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(intent)
	return json.Marshal(etcdCutoverJournalRecord{SchemaVersion: "norn.database-cutover-journal/v1", IntentSHA256: hex.EncodeToString(digest[:]), Journal: j})
}

func decodeEtcdCutoverJournal(encoded []byte, operationID string) (cutover.Journal, error) {
	var record etcdCutoverJournalRecord
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.SchemaVersion != "norn.database-cutover-journal/v1" || record.Journal.Intent.OperationID != operationID || record.Journal.Validate() != nil {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	intent, err := json.Marshal(record.Journal.Intent)
	if err != nil {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	digest := sha256.Sum256(intent)
	if hex.EncodeToString(digest[:]) != record.IntentSHA256 {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	return record.Journal, nil
}

// prepareDatabaseCutoverJournal is private until claimed acceptance, app lock
// and catalog identity are checked in the same transaction. These keys grant
// no runtime authority and cannot switch a consumer generation.
func (s *V3OperationStore) prepareDatabaseCutoverJournal(ctx context.Context, intent cutover.Intent) (cutover.Journal, error) {
	j, err := cutover.New(intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	if s == nil || s.kv == nil {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	encoded, err := encodeEtcdCutoverJournal(j)
	if err != nil {
		return cutover.Journal{}, err
	}
	opKey := s.cutoverOperationKey(intent.OperationID)
	resourceKey := s.cutoverActiveResourceKey(intent.App, intent.LogicalDatabase)
	result, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.CreateRevision(opKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(resourceKey), "=", 0),
	).Then(clientv3.OpPut(opKey, string(encoded)), clientv3.OpPut(resourceKey, intent.OperationID)).Commit()
	if err != nil {
		return cutover.Journal{}, err
	}
	if result.Succeeded {
		return j, nil
	}
	saved, _, err := s.loadDatabaseCutoverJournal(ctx, intent.OperationID)
	if err != nil || saved.Intent != intent || saved.Phase != cutover.PhasePrepare || saved.Revision != 1 || len(saved.Receipts) != 0 {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	return saved, nil
}

func (s *V3OperationStore) loadDatabaseCutoverJournal(ctx context.Context, operationID string) (cutover.Journal, int64, error) {
	if s == nil || s.kv == nil || operationID == "" {
		return cutover.Journal{}, 0, errEtcdCutoverJournalConflict
	}
	response, err := s.kv.Get(ctx, s.cutoverOperationKey(operationID))
	if err != nil {
		return cutover.Journal{}, 0, err
	}
	if len(response.Kvs) != 1 {
		return cutover.Journal{}, 0, errEtcdCutoverJournalConflict
	}
	j, err := decodeEtcdCutoverJournal(response.Kvs[0].Value, operationID)
	if err != nil {
		return cutover.Journal{}, 0, err
	}
	active, err := s.kv.Get(ctx, s.cutoverActiveResourceKey(j.Intent.App, j.Intent.LogicalDatabase))
	if err != nil {
		return cutover.Journal{}, 0, err
	}
	if len(active.Kvs) != 1 || string(active.Kvs[0].Value) != operationID {
		return cutover.Journal{}, 0, errEtcdCutoverJournalConflict
	}
	return j, response.Kvs[0].ModRevision, nil
}

// advanceDatabaseCutoverJournal uses etcd CAS on both the journal revision
// and active resource owner. A future coordinator must verify the receipt's
// external evidence before requesting this private storage transition.
func (s *V3OperationStore) advanceDatabaseCutoverJournal(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256 string) (cutover.Journal, error) {
	current, modRevision, err := s.loadDatabaseCutoverJournal(ctx, operationID)
	if err != nil {
		return cutover.Journal{}, err
	}
	nextJournal, err := current.Advance(expectedRevision, next, receiptSHA256)
	if err != nil {
		return cutover.Journal{}, fmt.Errorf("%w: %v", errEtcdCutoverJournalConflict, err)
	}
	encoded, err := encodeEtcdCutoverJournal(nextJournal)
	if err != nil {
		return cutover.Journal{}, err
	}
	resourceKey := s.cutoverActiveResourceKey(current.Intent.App, current.Intent.LogicalDatabase)
	result, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.cutoverOperationKey(operationID)), "=", modRevision),
		clientv3.Compare(clientv3.Value(resourceKey), "=", operationID),
	).Then(clientv3.OpPut(s.cutoverOperationKey(operationID), string(encoded))).Commit()
	if err != nil {
		return cutover.Journal{}, err
	}
	if !result.Succeeded {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	return nextJournal, nil
}
