package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// VerifyClaimedDeployment reloads the signed acceptance and immutable
// deployment aggregate for a claimed app.deploy. The returned operation is
// the store projection; the caller must still hold and renew its claim and
// app lock before reserving any external effect.
func (s *V3OperationStore) VerifyClaimedDeployment(ctx context.Context, claimed model.Operation) (store.AcceptedOperation, error) {
	if s == nil || s.kv == nil || s.signer == nil || claimed.ID == "" || claimed.Kind != "app.deploy" || claimed.App == "" {
		return store.AcceptedOperation{}, fmt.Errorf("deployment signed acceptance is unavailable")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claimed.ID))
	if err != nil || len(index.Kvs) != 1 {
		return store.AcceptedOperation{}, fmt.Errorf("deployment acceptance index is unavailable")
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if key != s.acceptanceKey(loaded.record.Identity) || loaded.record.Identity.Kind != "app.deploy" ||
		loaded.record.Identity.Resource != "app/"+claimed.App || loaded.record.Accepted.Operation.ID != claimed.ID ||
		loaded.record.Accepted.Intent.OperationID != claimed.ID {
		return store.AcceptedOperation{}, fmt.Errorf("deployment acceptance index differs from signed operation")
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if accepted.Operation.ID != claimed.ID || accepted.Operation.Kind != claimed.Kind || accepted.Operation.App != claimed.App ||
		accepted.Deployment == nil || accepted.Deployment.ID == "" || accepted.Deployment.App != claimed.App || len(accepted.Regions) == 0 {
		return store.AcceptedOperation{}, fmt.Errorf("claimed deployment differs from signed aggregate")
	}
	want, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	got, err := json.Marshal(claimed.Payload)
	if err != nil || !bytes.Equal(want, got) {
		return store.AcceptedOperation{}, fmt.Errorf("claimed deployment payload differs from signed acceptance")
	}
	return accepted, nil
}
