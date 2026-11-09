package etcdstore

import (
	"context"
	"fmt"

	"norn/v2/api/store"
)

// ResolveAcceptedOperationByID returns an operation only after resolving and
// verifying its signed V3 acceptance record. Mutable operation projections
// alone are not provenance for release qualification issuance.
func (s *V3OperationStore) ResolveAcceptedOperationByID(ctx context.Context, operationID string) (store.AcceptedOperation, error) {
	if s == nil || s.kv == nil || operationID == "" {
		return store.AcceptedOperation{}, fmt.Errorf("accepted operation lookup is incomplete")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(operationID))
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if len(index.Kvs) != 1 || string(index.Kvs[0].Value) == "" {
		return store.AcceptedOperation{}, ErrNotFound
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if loaded.record.Accepted.Operation.ID != operationID || s.acceptanceKey(loaded.record.Identity) != key {
		return store.AcceptedOperation{}, fmt.Errorf("operation acceptance index differs from signed identity")
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if accepted.Operation.ID != operationID {
		return store.AcceptedOperation{}, fmt.Errorf("resolved signed operation differs from requested identity")
	}
	return accepted, nil
}
