package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (s *V3OperationStore) privateInvocationAcceptanceIndexKey(operationID string) string {
	return s.prefix + "/v3/private-invocation-acceptance-index/" + operationID
}

// VerifyClaimedPrivateInvocation follows the immutable acceptance index
// written in the same etcd transaction as the encrypted request. It verifies
// the retained signature and accepted domain without depending on replay TTL.
func (s *V3OperationStore) VerifyClaimedPrivateInvocation(ctx context.Context, claimed model.Operation) (store.VerifiedPrivateInvocation, error) {
	if s == nil || s.kv == nil || s.signer == nil || claimed.ID == "" || claimed.Kind != store.PrivateInvocationOperationKind {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("function invocation signed acceptance is unavailable")
	}
	index, err := s.kv.Get(ctx, s.privateInvocationAcceptanceIndexKey(claimed.ID))
	if err != nil || len(index.Kvs) != 1 {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("function invocation acceptance index is unavailable")
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return store.VerifiedPrivateInvocation{}, err
	}
	record, accepted := loaded.record, loaded.record.Accepted
	if key != s.acceptanceKey(record.Identity) || record.Identity.Kind != store.PrivateInvocationOperationKind || record.Identity.Resource != claimed.App || accepted.Operation.ID != claimed.ID || accepted.Intent.OperationID != claimed.ID {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("function invocation acceptance index differs from signed operation")
	}
	if err := s.signer.Verify(ctx, accepted.Intent.Signature, accepted.Intent.CanonicalBytes); err != nil {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("function invocation acceptance signature is invalid")
	}
	persisted, _, err := s.load(ctx, claimed.ID)
	if err != nil {
		return store.VerifiedPrivateInvocation{}, err
	}
	if err := store.VerifyAcceptanceEvidence(store.AcceptanceEvidence{Identity: record.Identity, IdentityFingerprint: accepted.Intent.Fingerprint, IdentityOperationID: accepted.Operation.ID, Intent: accepted.Intent, Operation: persisted.Operation}); err != nil {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("function invocation accepted domain is invalid")
	}
	if persisted.Operation.ID != claimed.ID || persisted.Operation.Kind != claimed.Kind || persisted.Operation.App != claimed.App {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("claimed function invocation differs from signed acceptance")
	}
	want, err := json.Marshal(persisted.Operation.Payload)
	if err != nil {
		return store.VerifiedPrivateInvocation{}, err
	}
	got, err := json.Marshal(claimed.Payload)
	if err != nil || !bytes.Equal(want, got) {
		return store.VerifiedPrivateInvocation{}, fmt.Errorf("claimed function invocation payload differs from signed acceptance")
	}
	return store.VerifiedPrivateInvocation{Authority: record.Identity.Authority, Operation: persisted.Operation}, nil
}
