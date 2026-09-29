package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type indeterminateCatalogStore struct{}

func (indeterminateCatalogStore) ActivatePostgresDatabaseCatalogClaimed(context.Context, store.OperationClaim, store.AppOperationLock, int64, database.Catalog, string, map[string]interface{}) (store.DatabaseCatalogRevision, error) {
	return store.DatabaseCatalogRevision{}, store.ErrDatabaseCatalogCommitIndeterminate
}

func TestEtcdCatalogExecutorDefersIndeterminateCommit(t *testing.T) {
	claim, err := store.NewOperationClaim("catalog-operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	lock := store.NewFencedAppOperationLock(context.Background(), "catalog-lock", func() {})
	defer lock.Release()
	catalog, _ := json.Marshal(database.Catalog{APIVersion: database.APIVersion})
	op := &model.Operation{ID: claim.OperationID(), Kind: CatalogActivationKind, Ref: "database-catalog",
		Payload: map[string]interface{}{"expectedRevision": "0", "catalog": string(catalog), "catalogDigest": "sha256:invalid", "requestedBy": "operator"}}
	// A valid digest is needed to reach the catalog store's uncertain commit.
	digest := sha256.Sum256(catalog)
	op.Payload["catalogDigest"] = "sha256:" + hex.EncodeToString(digest[:])
	executor := &EtcdCatalogExecutor{Catalog: indeterminateCatalogStore{}}
	result, err := executor.ExecuteOperationWithAppLock(context.Background(), op, claim, lock)
	if result != nil || !effect.IsDeferred(err) || !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("uncertain activation was not deferred: result=%+v err=%v", result, err)
	}
}
