package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"norn/v2/api/model"
)

// VerifiedPrivateInvocation contains only the accepted public operation and
// its control authority. The request body remains in the encrypted record.
type VerifiedPrivateInvocation struct {
	Authority string
	Operation model.Operation
}

// VerifyClaimedPrivateInvocation reloads the acceptance by operation ID and
// verifies its retained signature and immutable domain before a function
// worker may decrypt material or reserve a Nomad effect. Replay expiry does
// not remove the proof needed by an unfinished accepted operation.
func (s *PGOperationStore) VerifyClaimedPrivateInvocation(ctx context.Context, claimed model.Operation) (VerifiedPrivateInvocation, error) {
	if s == nil || s.db == nil || s.db.Pool == nil || claimed.ID == "" || claimed.Kind != PrivateInvocationOperationKind {
		return VerifiedPrivateInvocation{}, fmt.Errorf("function invocation signed acceptance is unavailable")
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT authority::text, actor_issuer, actor_subject, kind, resource, request_key
		FROM operation_request_identities WHERE operation_id=$1
	`, claimed.ID)
	if err != nil {
		return VerifiedPrivateInvocation{}, err
	}
	defer rows.Close()
	var identity OperationRequestIdentity
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return VerifiedPrivateInvocation{}, err
		}
		return VerifiedPrivateInvocation{}, fmt.Errorf("function invocation acceptance identity is missing")
	}
	if err := rows.Scan(&identity.Authority, &identity.Actor.Issuer, &identity.Actor.Subject, &identity.Kind, &identity.Resource, &identity.Key); err != nil {
		return VerifiedPrivateInvocation{}, err
	}
	if rows.Next() || rows.Err() != nil || identity.Kind != PrivateInvocationOperationKind || identity.Resource != claimed.App {
		return VerifiedPrivateInvocation{}, fmt.Errorf("function invocation acceptance identity is ambiguous")
	}
	rows.Close()
	record, err := s.loadAcceptance(ctx, identity)
	if err != nil {
		return VerifiedPrivateInvocation{}, err
	}
	if err := s.verifyLoadedAcceptance(ctx, identity, record); err != nil {
		return VerifiedPrivateInvocation{}, err
	}
	accepted := record.operation
	if accepted.ID != claimed.ID || accepted.Kind != claimed.Kind || accepted.App != claimed.App {
		return VerifiedPrivateInvocation{}, fmt.Errorf("claimed function invocation differs from signed acceptance")
	}
	acceptedPayload, err := json.Marshal(accepted.Payload)
	if err != nil {
		return VerifiedPrivateInvocation{}, err
	}
	claimedPayload, err := json.Marshal(claimed.Payload)
	if err != nil || !bytes.Equal(acceptedPayload, claimedPayload) {
		return VerifiedPrivateInvocation{}, fmt.Errorf("claimed function invocation payload differs from signed acceptance")
	}
	return VerifiedPrivateInvocation{Authority: identity.Authority, Operation: accepted}, nil
}
