package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"norn/v2/api/model"
)

const privateInvocationKind = "app.function-invoke"

// AcceptPrivateInvocation joins private material to signed operation
// acceptance in one PostgreSQL transaction. Key material is supplied
// explicitly and never inferred from audit signing credentials.
func (s *PGOperationStore) AcceptPrivateInvocation(ctx context.Context, input OperationAcceptance, material PrivateInvocationInput, keys *PrivateInvocationKeyRing) (AcceptedOperation, error) {
	if s == nil || keys == nil || input.Identity.Kind != privateInvocationKind || input.Operation.Kind != privateInvocationKind || input.Operation.ID == "" || input.Operation.App == "" {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "private function invocation acceptance is incomplete"}
	}
	if input.Deployment != nil || len(input.Regions) != 0 {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "function invocation cannot accept a deployment"}
	}
	if err := validatePrivateInvocationPublicInput(input, material); err != nil {
		return AcceptedOperation{}, err
	}
	if prior, err := s.ResolveIdentity(ctx, input.Identity); err == nil {
		return s.resolvePrivateInvocationReplay(ctx, input, material, keys, prior)
	} else if !errors.Is(err, ErrAcceptanceNotFound) {
		return AcceptedOperation{}, err
	}
	if input.Operation.Payload == nil {
		input.Operation.Payload = map[string]interface{}{}
	}
	// Never mutate the caller's map; retries may reuse the same request object.
	payload := make(map[string]interface{}, len(input.Operation.Payload)+3)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"privateRecordId", "privateMaterialDigest", "privateKeyId"} {
		if _, exists := payload[field]; exists {
			return AcceptedOperation{}, &AcceptanceValidationError{Reason: "private invocation payload contains a reserved field"}
		}
	}
	binding := PrivateInvocationBinding{Authority: input.Identity.Authority, OperationID: input.Operation.ID, App: input.Operation.App, Process: privateInvocationProcess(payload)}
	sealed, err := keys.Seal(binding, material)
	if err != nil {
		return AcceptedOperation{}, err
	}
	digest, err := PrivateInvocationDigest(sealed)
	if err != nil {
		return AcceptedOperation{}, err
	}
	payload["privateRecordId"], payload["privateMaterialDigest"], payload["privateKeyId"] = input.Operation.ID, digest, sealed.KeyID
	input.Operation.Payload = payload
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return AcceptedOperation{}, err
	}
	accepted, err := s.acceptWithPrivateInvocation(ctx, input, &sealed)
	if err == nil {
		return accepted, nil
	}
	// A concurrent winner may have encrypted the identical request with a
	// different random data key. Resolve by identity and compare the decrypted
	// material and logical public fields, never by ciphertext digest.
	if !errors.Is(err, ErrAcceptanceConflict) && !errors.Is(err, ErrAcceptanceIndeterminate) {
		return AcceptedOperation{}, err
	}
	prior, resolveErr := s.ResolveIdentity(ctx, input.Identity)
	if resolveErr != nil {
		return AcceptedOperation{}, errors.Join(err, resolveErr)
	}
	delete(payload, "privateRecordId")
	delete(payload, "privateMaterialDigest")
	delete(payload, "privateKeyId")
	input.Operation.Payload = payload
	return s.resolvePrivateInvocationReplay(ctx, input, material, keys, prior)
}

func validatePrivateInvocationPublicInput(input OperationAcceptance, material PrivateInvocationInput) error {
	if hasPrivateInvocationField(input.Operation.Payload) || hasPrivateInvocationField(input.Operation.Metadata) || hasPrivateInvocationField(input.Semantics) {
		return &AcceptanceValidationError{Reason: "private function request fields cannot enter public operation material"}
	}
	publicBytes, err := json.Marshal(input)
	if err != nil {
		return &AcceptanceValidationError{Reason: "public function intent cannot be encoded"}
	}
	for _, value := range []string{material.Body, material.Method, material.Path} {
		if value == "" {
			continue
		}
		quoted, _ := json.Marshal(value)
		if bytes.Contains(publicBytes, quoted) {
			return &AcceptanceValidationError{Reason: "private function request value cannot enter public operation material"}
		}
	}
	return nil
}

func hasPrivateInvocationField(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, nested := range typed {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "body", "requestbody", "nornrequestbody", "method", "requestmethod", "nornrequestmethod", "path", "requestpath", "nornrequestpath", "privaterecordid", "privatematerialdigest", "privatekeyid":
				return true
			}
			if hasPrivateInvocationField(nested) {
				return true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if hasPrivateInvocationField(nested) {
				return true
			}
		}
	}
	return false
}

func privateInvocationProcess(payload map[string]interface{}) string {
	value, _ := payload["process"].(string)
	return value
}

func (s *PGOperationStore) resolvePrivateInvocationReplay(ctx context.Context, input OperationAcceptance, material PrivateInvocationInput, keys *PrivateInvocationKeyRing, prior AcceptedOperation) (AcceptedOperation, error) {
	if prior.Operation.Kind != privateInvocationKind || prior.Operation.App != input.Operation.App {
		return AcceptedOperation{}, &AcceptanceConflictError{Identity: input.Identity}
	}
	stored, err := s.OpenPrivateInvocation(ctx, prior.Operation, keys)
	if err != nil {
		return AcceptedOperation{}, err
	}
	want, _ := json.Marshal(material)
	got, _ := json.Marshal(stored)
	wantHash, gotHash := sha256.Sum256(want), sha256.Sum256(got)
	if subtle.ConstantTimeCompare(wantHash[:], gotHash[:]) != 1 {
		return AcceptedOperation{}, &AcceptanceConflictError{Identity: input.Identity}
	}
	payload := make(map[string]interface{}, len(input.Operation.Payload)+3)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"privateRecordId", "privateMaterialDigest", "privateKeyId"} {
		if _, exists := payload[field]; exists {
			return AcceptedOperation{}, &AcceptanceConflictError{Identity: input.Identity}
		}
		payload[field] = prior.Operation.Payload[field]
	}
	input.Operation.Payload = payload
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return AcceptedOperation{}, err
	}
	if !sameFingerprint(input.Fingerprint, prior.Intent.Fingerprint) {
		return AcceptedOperation{}, &AcceptanceConflictError{Identity: input.Identity}
	}
	return prior, nil
}

func insertPrivateInvocation(ctx context.Context, tx pgx.Tx, acceptance OperationAcceptance, envelope PrivateInvocationEnvelope) error {
	if acceptance.Identity.Kind != privateInvocationKind || acceptance.Operation.Kind != privateInvocationKind {
		return &AcceptanceValidationError{Reason: "private material requires a function invocation"}
	}
	digest, err := PrivateInvocationDigest(envelope)
	if err != nil {
		return err
	}
	payload := acceptance.Operation.Payload
	if payload["privateRecordId"] != acceptance.Operation.ID || payload["privateMaterialDigest"] != digest || payload["privateKeyId"] != envelope.KeyID {
		return &AcceptanceValidationError{Reason: "private material does not match signed operation"}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO private_invocation_material(operation_id,key_id,ciphertext_digest,envelope) VALUES($1,$2,$3,$4)`, acceptance.Operation.ID, envelope.KeyID, digest, encoded)
	return err
}

// OpenPrivateInvocation verifies the exact ciphertext named by the signed
// operation, then authenticates the record against its authority and owner.
func (s *PGOperationStore) OpenPrivateInvocation(ctx context.Context, operation model.Operation, keys *PrivateInvocationKeyRing) (PrivateInvocationInput, error) {
	if s == nil || s.db == nil || s.db.Pool == nil || keys == nil || operation.Kind != privateInvocationKind || operation.ID == "" {
		return PrivateInvocationInput{}, errors.New("private invocation is unavailable")
	}
	var keyID, digest string
	var encoded []byte
	if err := s.db.Pool.QueryRow(ctx, `SELECT key_id,ciphertext_digest,envelope FROM private_invocation_material WHERE operation_id=$1`, operation.ID).Scan(&keyID, &digest, &encoded); err != nil {
		return PrivateInvocationInput{}, err
	}
	if operation.Payload["privateRecordId"] != operation.ID || operation.Payload["privateMaterialDigest"] != digest || operation.Payload["privateKeyId"] != keyID {
		return PrivateInvocationInput{}, errors.New("private invocation record differs from signed operation")
	}
	var envelope PrivateInvocationEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return PrivateInvocationInput{}, errors.New("private invocation envelope is invalid")
	}
	actual, err := PrivateInvocationDigest(envelope)
	if err != nil || actual != digest || envelope.KeyID != keyID {
		return PrivateInvocationInput{}, errors.New("private invocation envelope digest is invalid")
	}
	authority, err := s.Authority(ctx)
	if err != nil {
		return PrivateInvocationInput{}, err
	}
	binding := PrivateInvocationBinding{Authority: authority, OperationID: operation.ID, App: operation.App, Process: privateInvocationProcess(operation.Payload)}
	result, err := keys.Open(binding, envelope)
	if err != nil {
		return PrivateInvocationInput{}, err
	}
	return result, nil
}

// RequiredPrivateInvocationKeys returns the key IDs a restore must supply
// before it can execute or reconcile any retained invocation. A later
// retention contract may narrow this to protected unresolved records.
func (s *PGOperationStore) RequiredPrivateInvocationKeys(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil || s.db.Pool == nil {
		return nil, errors.New("private invocation store is unavailable")
	}
	rows, err := s.db.Pool.Query(ctx, `SELECT DISTINCT key_id FROM private_invocation_material ORDER BY key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
