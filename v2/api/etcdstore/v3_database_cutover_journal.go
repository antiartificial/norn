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
	"strconv"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/cutover"
	"norn/v2/api/model"
	"norn/v2/api/store"
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

// prepareDatabaseCutoverJournal is an unclaimed storage fixture used only by
// package tests. Production callers must use PrepareClaimedDatabaseCutoverJournal.
// Neither path can switch a consumer generation.
func (s *V3OperationStore) prepareDatabaseCutoverJournal(ctx context.Context, intent cutover.Intent) (cutover.Journal, error) {
	return s.prepareDatabaseCutoverJournalFenced(ctx, intent, nil)
}

// PrepareClaimedDatabaseCutoverJournal commits the initial journal only while
// signed acceptance, operation owner lease and app lock all remain current.
// It records no source fence or target activation.
func (s *V3OperationStore) PrepareClaimedDatabaseCutoverJournal(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent cutover.Intent) (cutover.Journal, error) {
	if intent.OperationID != claim.OperationID() {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	fences, err := s.claimedCutoverComparisons(ctx, claim, lock, intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	catalogFences, err := s.cutoverCatalogComparisons(ctx, intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	fences = append(fences, catalogFences...)
	return s.prepareDatabaseCutoverJournalFenced(ctx, intent, fences)
}

// Read the active pointer and immutable revision, then pin both values in
// the same transaction that creates the journal. A concurrent catalog switch
// fails the transaction instead of journaling an obsolete binding.
func (s *V3OperationStore) cutoverCatalogComparisons(ctx context.Context, intent cutover.Intent) ([]clientv3.Cmp, error) {
	active, err := s.kv.Get(ctx, s.databaseCatalogActiveKey())
	if err != nil {
		return nil, err
	}
	if len(active.Kvs) != 1 {
		return nil, errEtcdCutoverJournalConflict
	}
	revision, err := strconv.ParseInt(string(active.Kvs[0].Value), 10, 64)
	if err != nil || revision != intent.CatalogRevision {
		return nil, errEtcdCutoverJournalConflict
	}
	revisionKey := s.databaseCatalogRevisionKey(revision)
	record, err := s.kv.Get(ctx, revisionKey)
	if err != nil {
		return nil, err
	}
	if len(record.Kvs) != 1 {
		return nil, errEtcdCutoverJournalConflict
	}
	catalog, err := decodeV3DatabaseCatalog(record.Kvs[0].Value, revision)
	if err != nil {
		return nil, errEtcdCutoverJournalConflict
	}
	if err := cutover.VerifyCatalog(intent, catalog.Revision, catalog.Digest, catalog.Catalog); err != nil {
		return nil, err
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.databaseCatalogActiveKey()), "=", active.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(revisionKey), "=", record.Kvs[0].ModRevision),
	}, nil
}

func (s *V3OperationStore) prepareDatabaseCutoverJournalFenced(ctx context.Context, intent cutover.Intent, fences []clientv3.Cmp) (cutover.Journal, error) {
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
	compares := append([]clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(opKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(resourceKey), "=", 0),
	}, fences...)
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(opKey, string(encoded)), clientv3.OpPut(resourceKey, intent.OperationID)).Commit()
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
	if len(fences) != 0 {
		confirmed, err := s.kv.Txn(ctx).If(fences...).Then().Commit()
		if err != nil {
			return cutover.Journal{}, err
		}
		if !confirmed.Succeeded {
			return cutover.Journal{}, store.ErrOperationOwnershipLost
		}
	}
	return saved, nil
}

func (s *V3OperationStore) claimedCutoverComparisons(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent cutover.Intent) ([]clientv3.Cmp, error) {
	if s == nil || s.kv == nil || s.signer == nil || claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() < 1 || lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || intent.OperationID != claim.OperationID() {
		return nil, store.ErrOperationOwnershipLost
	}
	digest, err := cutover.IntentSHA256(intent)
	if err != nil {
		return nil, err
	}
	operation, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil {
		return nil, err
	}
	if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) || operation.Generation != claim.Generation() || operation.Operation.Status != model.OperationRunning || operation.Operation.LockedBy != claim.OwnerID() || operation.Operation.Kind != store.DatabaseCutoverOperationKind || operation.Operation.App != intent.App || operation.Operation.Ref != intent.CandidateRelease || operation.Operation.MaxAttempts != 1 || operation.Operation.Payload["cutoverIntentSha256"] != digest {
		return nil, store.ErrOperationOwnershipLost
	}
	indexKey := s.operationAcceptanceIndexKey(claim.OperationID())
	index, err := s.kv.Get(ctx, indexKey)
	if err != nil || len(index.Kvs) != 1 {
		return nil, errEtcdCutoverJournalConflict
	}
	acceptanceKey := string(index.Kvs[0].Value)
	accepted, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil {
		return nil, errEtcdCutoverJournalConflict
	}
	identity, evidence := accepted.record.Identity, accepted.record.Accepted
	if acceptanceKey != s.acceptanceKey(identity) || identity.Kind != store.DatabaseCutoverOperationKind || identity.Resource != "app/"+intent.App+"/database/"+intent.LogicalDatabase || evidence.Operation.ID != claim.OperationID() || evidence.Operation.Kind != store.DatabaseCutoverOperationKind || evidence.Operation.App != intent.App || evidence.Operation.Ref != intent.CandidateRelease || evidence.Operation.MaxAttempts != 1 || evidence.Operation.Payload["cutoverIntentSha256"] != digest || evidence.Intent.OperationID != claim.OperationID() || evidence.Deployment != nil {
		return nil, errEtcdCutoverJournalConflict
	}
	if s.signer.Verify(ctx, evidence.Intent.Signature, evidence.Intent.CanonicalBytes) != nil || store.VerifyAcceptanceEvidence(store.AcceptanceEvidence{Identity: identity, IdentityFingerprint: evidence.Intent.Fingerprint, IdentityOperationID: evidence.Operation.ID, Intent: evidence.Intent, Operation: operation.Operation}) != nil {
		return nil, errEtcdCutoverJournalConflict
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.ModRevision(indexKey), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", accepted.revision),
		clientv3.Compare(clientv3.Value(s.appLockKey(intent.App)), "=", lock.Fence()),
	}, nil
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
	return s.advanceDatabaseCutoverJournalFenced(ctx, operationID, expectedRevision, next, receiptSHA256, nil, "")
}

// AdvanceClaimedDatabaseCutoverJournal binds each private etcd phase write to
// the same signed intent, live operation owner and app lock as preparation.
func (s *V3OperationStore) AdvanceClaimedDatabaseCutoverJournal(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, expectedRevision uint64, next cutover.Phase, receiptSHA256 string) (cutover.Journal, error) {
	current, _, err := s.loadDatabaseCutoverJournal(ctx, claim.OperationID())
	if err != nil {
		return cutover.Journal{}, err
	}
	fences, err := s.claimedCutoverComparisons(ctx, claim, lock, current.Intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	if next == cutover.PhaseQuiesce || next == cutover.PhaseFinalSync {
		catalogFences, err := s.cutoverCatalogComparisons(ctx, current.Intent)
		if err != nil {
			return cutover.Journal{}, err
		}
		fences = append(fences, catalogFences...)
	}
	digest, err := cutover.IntentSHA256(current.Intent)
	if err != nil {
		return cutover.Journal{}, err
	}
	return s.advanceDatabaseCutoverJournalFenced(ctx, claim.OperationID(), expectedRevision, next, receiptSHA256, fences, digest)
}

func (s *V3OperationStore) advanceDatabaseCutoverJournalFenced(ctx context.Context, operationID string, expectedRevision uint64, next cutover.Phase, receiptSHA256 string, fences []clientv3.Cmp, expectedIntentDigest string) (cutover.Journal, error) {
	current, modRevision, err := s.loadDatabaseCutoverJournal(ctx, operationID)
	if err != nil {
		return cutover.Journal{}, err
	}
	if expectedIntentDigest != "" {
		actual, err := cutover.IntentSHA256(current.Intent)
		if err != nil || actual != expectedIntentDigest {
			return cutover.Journal{}, errEtcdCutoverJournalConflict
		}
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
	compares := append([]clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.cutoverOperationKey(operationID)), "=", modRevision),
		clientv3.Compare(clientv3.Value(resourceKey), "=", operationID),
	}, fences...)
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.cutoverOperationKey(operationID), string(encoded))).Commit()
	if err != nil {
		return cutover.Journal{}, err
	}
	if !result.Succeeded {
		return cutover.Journal{}, errEtcdCutoverJournalConflict
	}
	return nextJournal, nil
}
