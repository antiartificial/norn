package pipeline

import (
	"context"
	"errors"
	"fmt"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type ClaimedPostgresCatalogStore interface {
	ActivatePostgresDatabaseCatalogClaimed(context.Context, store.OperationClaim, store.AppOperationLock, int64, database.Catalog, string, map[string]interface{}) (store.DatabaseCatalogRevision, error)
}

// EtcdCatalogExecutor executes only signed PostgreSQL catalog activations.
// It cannot run through the unfenced worker entry point.
type EtcdCatalogExecutor struct{ Catalog ClaimedPostgresCatalogStore }

func (e *EtcdCatalogExecutor) ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*OperationResult, error) {
	return nil, fmt.Errorf("etcd catalog activation requires a worker app lock")
}

func (e *EtcdCatalogExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*OperationResult, error) {
	if e == nil || e.Catalog == nil || op == nil || op.Kind != CatalogActivationKind || op.Ref != catalogActivationResource || claim.OperationID() != op.ID || lock == nil || lock.Fence() == "" {
		return nil, fmt.Errorf("claimed etcd catalog activation is unavailable")
	}
	expected, catalog, digest, err := catalogActivationPayload(op.Payload)
	if err != nil {
		return nil, err
	}
	actor, _ := op.Payload["requestedBy"].(string)
	if actor == "" {
		return nil, fmt.Errorf("catalog activation payload has no accepted actor")
	}
	metadata := map[string]interface{}{"expectedRevision": expected, "catalogDigest": digest}
	activated, err := e.Catalog.ActivatePostgresDatabaseCatalogClaimed(ctx, claim, lock, expected, catalog, actor, metadata)
	var resolverErr *database.ResolverError
	refused := errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) || errors.Is(err, store.ErrDatabaseCatalogRetiredIdentity) || errors.As(err, &resolverErr)
	if err != nil && !refused {
		return nil, err
	}
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "catalog activation refused: " + err.Error(), Metadata: metadata}, nil
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("database catalog revision %d active", activated.Revision),
		Metadata: map[string]interface{}{"expectedRevision": expected, "revision": activated.Revision, "catalogDigest": digest, "storedDigest": activated.Digest},
		finished: true, appLockFenced: true}, nil
}
