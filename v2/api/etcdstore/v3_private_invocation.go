package etcdstore

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// AcceptPrivateInvocation atomically stores a signed operation acceptance and
// an envelope-encrypted function request. The public signed payload carries
// only the private record ID, ciphertext digest, and encryption key ID.
func (s *V3OperationStore) AcceptPrivateInvocation(ctx context.Context, input store.OperationAcceptance, material store.PrivateInvocationInput, keys *store.PrivateInvocationKeyRing) (store.AcceptedOperation, error) {
	if s == nil || s.signer == nil || keys == nil || input.Identity.Kind != store.PrivateInvocationOperationKind || input.Operation.Kind != store.PrivateInvocationOperationKind || input.Operation.ID == "" || input.Operation.App == "" {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "private function invocation acceptance is incomplete"}
	}
	if input.Deployment != nil || len(input.Regions) != 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "function invocation cannot accept a deployment"}
	}
	if err := store.ValidatePrivateInvocationPublicInput(input, material); err != nil {
		return store.AcceptedOperation{}, err
	}
	if prior, err := s.ResolveIdentity(ctx, input.Identity); err == nil {
		return s.resolvePrivateInvocationReplay(ctx, input, material, keys, prior)
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, err
	}
	if input.Operation.Payload == nil {
		input.Operation.Payload = map[string]interface{}{}
	}
	payload := make(map[string]interface{}, len(input.Operation.Payload)+3)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"privateRecordId", "privateMaterialDigest", "privateKeyId"} {
		if _, exists := payload[field]; exists {
			return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "private invocation payload contains a reserved field"}
		}
	}
	binding := store.PrivateInvocationBinding{Authority: input.Identity.Authority, OperationID: input.Operation.ID, App: input.Operation.App, Process: store.PrivateInvocationProcess(payload)}
	sealed, err := keys.Seal(binding, material)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	digest, err := store.PrivateInvocationDigest(sealed)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	payload["privateRecordId"], payload["privateMaterialDigest"], payload["privateKeyId"] = input.Operation.ID, digest, sealed.KeyID
	input.Operation.Payload = payload
	input.Fingerprint, err = store.CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	key := s.acceptanceKey(acceptance.Identity)
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, acceptedAt)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
	acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	operationRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	privateRecord, err := json.Marshal(v3PrivateInvocation{KeyID: sealed.KeyID, CiphertextDigest: digest, Envelope: sealed})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	privateKey := s.privateInvocationKey(acceptance.Operation.ID)
	acceptanceIndex := s.privateInvocationAcceptanceIndexKey(acceptance.Operation.ID)
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(privateKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(acceptanceIndex), "=", 0),
	).Then(
		clientv3.OpPut(key, string(acceptanceRecord)),
		clientv3.OpPut(acceptanceIndex, key),
		clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)),
		clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, acceptedAt, acceptance.Operation.ID), acceptance.Operation.ID),
		clientv3.OpPut(privateKey, string(privateRecord)),
	).Commit()
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if txn.Succeeded {
		return accepted, nil
	}
	prior, resolveErr := s.ResolveIdentity(ctx, input.Identity)
	if resolveErr != nil {
		return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: errors.Join(errors.New("private invocation acceptance transaction did not commit"), resolveErr)}
	}
	delete(payload, "privateRecordId")
	delete(payload, "privateMaterialDigest")
	delete(payload, "privateKeyId")
	input.Operation.Payload = payload
	return s.resolvePrivateInvocationReplay(ctx, input, material, keys, prior)
}

func (s *V3OperationStore) resolvePrivateInvocationReplay(ctx context.Context, input store.OperationAcceptance, material store.PrivateInvocationInput, keys *store.PrivateInvocationKeyRing, prior store.AcceptedOperation) (store.AcceptedOperation, error) {
	if prior.Operation.Kind != store.PrivateInvocationOperationKind || prior.Operation.App != input.Operation.App {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	stored, err := s.OpenPrivateInvocation(ctx, prior.Operation, keys)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	want, _ := json.Marshal(material)
	got, _ := json.Marshal(stored)
	wantHash, gotHash := sha256.Sum256(want), sha256.Sum256(got)
	if subtle.ConstantTimeCompare(wantHash[:], gotHash[:]) != 1 {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	payload := make(map[string]interface{}, len(input.Operation.Payload)+3)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"privateRecordId", "privateMaterialDigest", "privateKeyId"} {
		if _, exists := payload[field]; exists {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		payload[field] = prior.Operation.Payload[field]
	}
	input.Operation.Payload = payload
	input.Fingerprint, err = store.CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if input.Fingerprint != prior.Intent.Fingerprint {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	return prior, nil
}

// OpenPrivateInvocation verifies that the encrypted record is the exact one
// named by signed public evidence before decrypting it with its bound AAD.
func (s *V3OperationStore) OpenPrivateInvocation(ctx context.Context, operation model.Operation, keys *store.PrivateInvocationKeyRing) (store.PrivateInvocationInput, error) {
	if s == nil || s.kv == nil || keys == nil || operation.Kind != store.PrivateInvocationOperationKind || operation.ID == "" {
		return store.PrivateInvocationInput{}, errors.New("private invocation is unavailable")
	}
	response, err := s.kv.Get(ctx, s.privateInvocationKey(operation.ID))
	if err != nil {
		return store.PrivateInvocationInput{}, err
	}
	if len(response.Kvs) != 1 {
		return store.PrivateInvocationInput{}, errors.New("private invocation record is unavailable")
	}
	var record v3PrivateInvocation
	if err := decodeV3Record(response.Kvs[0].Value, &record); err != nil {
		return store.PrivateInvocationInput{}, errors.New("private invocation envelope is invalid")
	}
	if operation.Payload["privateRecordId"] != operation.ID || operation.Payload["privateMaterialDigest"] != record.CiphertextDigest || operation.Payload["privateKeyId"] != record.KeyID {
		return store.PrivateInvocationInput{}, errors.New("private invocation record differs from signed operation")
	}
	digest, err := store.PrivateInvocationDigest(record.Envelope)
	if err != nil || digest != record.CiphertextDigest || record.Envelope.KeyID != record.KeyID {
		return store.PrivateInvocationInput{}, errors.New("private invocation envelope digest is invalid")
	}
	binding := store.PrivateInvocationBinding{Authority: s.authority, OperationID: operation.ID, App: operation.App, Process: store.PrivateInvocationProcess(operation.Payload)}
	return keys.Open(binding, record.Envelope)
}

// RequiredPrivateInvocationKeys returns every retained envelope key ID so a
// caller can fail restore or startup before attempting an invocation.
func (s *V3OperationStore) RequiredPrivateInvocationKeys(ctx context.Context) ([]string, error) {
	if s == nil || s.kv == nil {
		return nil, errors.New("private invocation store is unavailable")
	}
	response, err := s.kv.Get(ctx, s.privateInvocationPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(response.Kvs))
	for _, item := range response.Kvs {
		var record v3PrivateInvocation
		if err := decodeV3Record(item.Value, &record); err != nil || record.KeyID == "" {
			return nil, fmt.Errorf("decode private invocation record %q", string(item.Key))
		}
		ids[record.KeyID] = struct{}{}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
