package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// The etcd catalog remains private while its public admission and executor
// routes are built. MySQL recovery fences still live in PostgreSQL and cannot
// be inferred from this record.
type v3DatabaseCatalogRecord struct {
	Revision int64           `json:"revision"`
	Digest   string          `json:"digest"`
	Catalog  json.RawMessage `json:"catalog"`
	Actor    string          `json:"actor"`
}

func (s *V3OperationStore) databaseCatalogActiveKey() string {
	return s.prefix + "/v3/database-catalog/active"
}
func (s *V3OperationStore) databaseCatalogRevisionKey(revision int64) string {
	return fmt.Sprintf("%s/v3/database-catalog/revisions/%020d", s.prefix, revision)
}
func (s *V3OperationStore) databaseCatalogRetirementKey(retired database.RetiredResource) string {
	return s.prefix + "/v3/database-catalog/retired/" + string(retired.Kind) + "/" + retired.ID
}

func decodeV3DatabaseCatalog(encoded []byte, expected int64) (store.DatabaseCatalogRevision, error) {
	var record v3DatabaseCatalogRecord
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Revision != expected || record.Actor == "" {
		return store.DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d is malformed", expected)
	}
	digest := sha256.Sum256(record.Catalog)
	if hex.EncodeToString(digest[:]) != record.Digest {
		return store.DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d failed integrity verification", expected)
	}
	var catalog database.Catalog
	decoder = json.NewDecoder(bytes.NewReader(record.Catalog))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF || database.ValidateCatalog(catalog) != nil {
		return store.DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d is invalid", expected)
	}
	return store.DatabaseCatalogRevision{Revision: expected, Digest: record.Digest, Catalog: catalog}, nil
}

// ActiveDatabaseCatalog reads one etcd snapshot containing the active pointer
// and immutable revision. Neither a missing nor a corrupt revision is treated
// as an empty catalog.
func (s *V3OperationStore) ActiveDatabaseCatalog(ctx context.Context) (store.DatabaseCatalogRevision, error) {
	if s == nil || s.kv == nil {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	active, err := s.kv.Get(ctx, s.databaseCatalogActiveKey())
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	if len(active.Kvs) == 0 {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	var revision int64
	if revision, err = strconv.ParseInt(string(active.Kvs[0].Value), 10, 64); err != nil || revision < 1 {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	return s.DatabaseCatalogRevision(ctx, revision)
}

func (s *V3OperationStore) DatabaseCatalogRevision(ctx context.Context, revision int64) (store.DatabaseCatalogRevision, error) {
	if s == nil || s.kv == nil || revision < 1 {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	response, err := s.kv.Get(ctx, s.databaseCatalogRevisionKey(revision))
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	if len(response.Kvs) != 1 {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	return decodeV3DatabaseCatalog(response.Kvs[0].Value, revision)
}

// ActivatePostgresDatabaseCatalog is a private, PostgreSQL-only bootstrap CAS.
// Normal activation uses the claimed method and its atomic operation receipt.
func (s *V3OperationStore) ActivatePostgresDatabaseCatalog(ctx context.Context, expectedCurrent int64, next database.Catalog, actor string) (store.DatabaseCatalogRevision, error) {
	return s.activatePostgresDatabaseCatalog(ctx, nil, expectedCurrent, next, actor, nil)
}

// ActivatePostgresDatabaseCatalogClaimed commits the PostgreSQL-only catalog
// revision and terminal operation receipt atomically. A lapsed or replaced
// lease cannot activate routing, including when the expected revision matches.
func (s *V3OperationStore) ActivatePostgresDatabaseCatalogClaimed(ctx context.Context, claim store.OperationClaim, expectedCurrent int64, next database.Catalog, actor string, metadata map[string]interface{}) (store.DatabaseCatalogRevision, error) {
	if claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() < 1 {
		return store.DatabaseCatalogRevision{}, store.ErrOperationOwnershipLost
	}
	return s.activatePostgresDatabaseCatalog(ctx, &claim, expectedCurrent, next, actor, metadata)
}

func (s *V3OperationStore) activatePostgresDatabaseCatalog(ctx context.Context, claim *store.OperationClaim, expectedCurrent int64, next database.Catalog, actor string, metadata map[string]interface{}) (store.DatabaseCatalogRevision, error) {
	if s == nil || s.kv == nil || expectedCurrent < 0 || strings.TrimSpace(actor) == "" {
		return store.DatabaseCatalogRevision{}, fmt.Errorf("database catalog activation requires store, actor, and expected revision")
	}
	if err := database.ValidateCatalog(next); err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	for _, service := range next.Services {
		if service.Engine != database.EnginePostgreSQL {
			return store.DatabaseCatalogRevision{}, fmt.Errorf("etcd catalog activation currently supports PostgreSQL only")
		}
	}
	active, err := s.kv.Get(ctx, s.databaseCatalogActiveKey())
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	actual := int64(0)
	activeModRevision := int64(0)
	if len(active.Kvs) != 0 {
		activeModRevision = active.Kvs[0].ModRevision
		if actual, err = strconv.ParseInt(string(active.Kvs[0].Value), 10, 64); err != nil || actual < 1 {
			return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
		}
	}
	if actual != expectedCurrent {
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	if actual > 0 {
		previous, err := s.DatabaseCatalogRevision(ctx, actual)
		if err != nil {
			return store.DatabaseCatalogRevision{}, err
		}
		if err := database.ValidateTransition(previous.Catalog, next); err != nil {
			return store.DatabaseCatalogRevision{}, err
		}
	}
	retirements, err := s.kv.Get(ctx, s.prefix+"/v3/database-catalog/retired/", clientv3.WithPrefix())
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	listed := make(map[database.RetiredResource]bool, len(next.Retired))
	live := make(map[database.RetiredResource]bool)
	previouslyRetired := make(map[database.RetiredResource]bool, len(retirements.Kvs))
	for _, item := range next.Retired {
		listed[item] = true
	}
	for _, item := range next.Services {
		live[database.RetiredResource{Kind: database.RetiredService, ID: item.ID}] = true
	}
	for _, item := range next.Bindings {
		live[database.RetiredResource{Kind: database.RetiredBinding, ID: item.ID}] = true
	}
	for _, item := range next.Profiles {
		if item.LegacyPostgres != nil {
			live[database.RetiredResource{Kind: database.RetiredBinding, ID: item.LegacyPostgres.MappingID}] = true
		}
	}
	for _, item := range retirements.Kvs {
		parts := strings.SplitN(strings.TrimPrefix(string(item.Key), s.prefix+"/v3/database-catalog/retired/"), "/", 2)
		if len(parts) != 2 {
			return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRetiredIdentity
		}
		identity := database.RetiredResource{Kind: database.RetiredKind(parts[0]), ID: parts[1]}
		previouslyRetired[identity] = true
		if live[identity] || !listed[identity] {
			return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRetiredIdentity
		}
	}
	encodedCatalog, err := json.Marshal(next)
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	digest := sha256.Sum256(encodedCatalog)
	revision := actual + 1
	record := v3DatabaseCatalogRecord{Revision: revision, Digest: hex.EncodeToString(digest[:]), Catalog: encodedCatalog, Actor: actor}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.databaseCatalogActiveKey()), "=", activeModRevision),
		clientv3.Compare(clientv3.CreateRevision(s.databaseCatalogRevisionKey(revision)), "=", 0),
	}
	ops := []clientv3.Op{clientv3.OpPut(s.databaseCatalogRevisionKey(revision), string(encodedRecord)), clientv3.OpPut(s.databaseCatalogActiveKey(), fmt.Sprint(revision))}
	for _, item := range next.Retired {
		if previouslyRetired[item] {
			continue
		}
		key := s.databaseCatalogRetirementKey(item)
		compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key), "=", 0))
		ops = append(ops, clientv3.OpPut(key, fmt.Sprint(revision)))
	}
	if claim != nil {
		operation, operationRevision, err := s.load(ctx, claim.OperationID())
		if err != nil {
			return store.DatabaseCatalogRevision{}, store.ErrOperationOwnershipLost
		}
		owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
		if err != nil {
			return store.DatabaseCatalogRevision{}, err
		}
		if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) ||
			operation.Generation != claim.Generation() || operation.Operation.Status != model.OperationRunning || operation.Operation.LockedBy != claim.OwnerID() ||
			operation.Operation.Kind != "database.catalog-activate" || operation.Operation.Ref != "database-catalog" {
			return store.DatabaseCatalogRevision{}, store.ErrOperationOwnershipLost
		}
		if operation.Operation.Metadata == nil {
			operation.Operation.Metadata = make(map[string]interface{})
		}
		for key, value := range metadata {
			operation.Operation.Metadata[key] = value
		}
		operation.Operation.Metadata["revision"] = revision
		operation.Operation.Metadata["storedDigest"] = record.Digest
		finishedAt := time.Now().UTC()
		operation.Operation.Status = model.OperationSucceeded
		operation.Operation.Message = fmt.Sprintf("database catalog revision %d active", revision)
		operation.Operation.LockedBy = ""
		operation.Operation.LockedUntil = nil
		operation.Operation.UpdatedAt = finishedAt
		operation.Operation.FinishedAt = &finishedAt
		encodedOperation, err := json.Marshal(operation)
		if err != nil {
			return store.DatabaseCatalogRevision{}, err
		}
		compares = append(compares,
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())))
		ops = append(ops, clientv3.OpPut(s.opKey(claim.OperationID()), string(encodedOperation)),
			clientv3.OpDelete(s.ownerKey(claim.OperationID())), clientv3.OpDelete(s.runningKey(claim.OperationID())))
	}
	txn, err := s.kv.Txn(ctx).If(compares...).Then(ops...).Commit()
	if err != nil {
		return store.DatabaseCatalogRevision{}, err
	}
	if !txn.Succeeded {
		if claim != nil {
			return store.DatabaseCatalogRevision{}, store.ErrOperationOwnershipLost
		}
		return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogRevisionConflict
	}
	return store.DatabaseCatalogRevision{Revision: revision, Digest: record.Digest, Catalog: next}, nil
}
