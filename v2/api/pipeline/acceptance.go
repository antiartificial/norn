package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// EnqueueRequest carries verified producer identity and audit evidence into the
// durable operation acceptance boundary. Pipeline methods set kind/resource
// from the domain object; callers cannot substitute them.
type EnqueueRequest struct {
	Authority           string
	Actor               store.OperationActor
	Key                 string
	Audit               store.AcceptanceAuditContext
	Semantics           map[string]interface{}
	Admission           store.OperationAdmissionPolicy
	FleetReconciliation *store.FleetReconciliationAdmission
	FleetRunnerAttempt  *store.FleetRunnerAttemptAdmission
}

func (p *Pipeline) SetOperationStore(operationStore store.OperationStore) {
	if p != nil {
		p.OperationStore = operationStore
	}
}

func (p *Pipeline) acceptOperation(ctx context.Context, request EnqueueRequest, operation model.Operation, deployment *model.Deployment, regions []model.ResolvedRegion) (store.AcceptedOperation, error) {
	if p == nil || p.OperationStore == nil {
		return store.AcceptedOperation{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	if operation.Kind == "app.snapshot" && (p.SnapshotEffects == nil || !p.SnapshotEffects.available()) {
		return store.AcceptedOperation{}, &SnapshotExecutionUnavailableError{}
	}
	request.Authority = strings.TrimSpace(request.Authority)
	request.Key = strings.TrimSpace(request.Key)
	if request.Authority == "" || request.Actor.Issuer == "" || request.Actor.Subject == "" || request.Key == "" {
		return store.AcceptedOperation{}, fmt.Errorf("operation enqueue identity is incomplete")
	}
	resource := operation.App
	if resource == "" {
		resource = operation.Ref
	}
	if resource == "" {
		return store.AcceptedOperation{}, fmt.Errorf("operation enqueue resource is empty")
	}
	// With a database profile configured, database-consuming work records
	// its complete target identity inside the signed request.
	operation, err := p.bindDatabaseTargetForRequest(ctx, request, resource, operation)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: request.Authority, Actor: request.Actor, Kind: operation.Kind, Resource: resource, Key: request.Key},
		Operation: operation, Deployment: deployment, Regions: regions, Audit: request.Audit,
		Admission: request.Admission, Semantics: request.Semantics,
		FleetReconciliation: request.FleetReconciliation,
		FleetRunnerAttempt:  request.FleetRunnerAttempt,
	}
	fingerprint, err := store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance.Fingerprint = fingerprint
	return p.OperationStore.Accept(ctx, acceptance)
}

func (p *Pipeline) QueueOperation(ctx context.Context, operation model.Operation, request EnqueueRequest) (store.AcceptedOperation, error) {
	return p.acceptOperation(ctx, request, operation, nil, nil)
}

func (p *Pipeline) ResolveEnqueue(ctx context.Context, request EnqueueRequest, kind, resource string) (store.AcceptedOperation, error) {
	if p == nil || p.OperationStore == nil {
		return store.AcceptedOperation{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	resolver, ok := p.OperationStore.(store.OperationIdentityResolver)
	if !ok {
		return store.AcceptedOperation{}, fmt.Errorf("operation identity resolution is unavailable")
	}
	return resolver.ResolveIdentity(ctx, store.OperationRequestIdentity{Authority: request.Authority, Actor: request.Actor, Kind: kind, Resource: resource, Key: request.Key})
}

func (p *Pipeline) systemEnqueueRequest(ctx context.Context, subject, key, source string, semantics map[string]interface{}) (EnqueueRequest, error) {
	if p == nil || p.OperationStore == nil {
		return EnqueueRequest{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	authority, err := p.OperationStore.Authority(ctx)
	if err != nil {
		return EnqueueRequest{}, err
	}
	return EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/system", Subject: subject}, Key: key, Audit: store.AcceptanceAuditContext{Source: source}, Semantics: semantics}, nil
}

// DerivedChildRequest gives every member of a multi-app action a stable child
// slot while binding group membership/ref in the fingerprint Semantics.
func DerivedChildRequest(parent EnqueueRequest, namespace, child string, semantics map[string]interface{}) EnqueueRequest {
	digest := sha256.Sum256([]byte(parent.Key + "\x00" + namespace + "\x00" + child))
	parent.Key = namespace + ":" + hex.EncodeToString(digest[:])
	parent.Semantics = semantics
	return parent
}
