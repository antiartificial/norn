package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// Catalog activation is an ordinary durable operation: it is accepted through
// signed, idempotent operation acceptance (with the request's mutation-audit
// receipt), executed by the operation worker, and applied with the store's
// expected-revision compare-and-set. There is no direct configuration write.

const (
	CatalogActivationKind     = "database.catalog-activate"
	catalogActivationResource = "database-catalog"
)

// CatalogActivationError is a refusal detected before acceptance.
type CatalogActivationError struct{ Reason string }

func (e *CatalogActivationError) Error() string { return "database catalog activation: " + e.Reason }

// QueueCatalogActivation validates a catalog and its transition from the
// active revision, then accepts the activation. The exact catalog bytes and
// their digest are part of the signed request, so an identical retry replays
// the original receipt and a different catalog under the same key conflicts.
func (p *Pipeline) QueueCatalogActivation(ctx context.Context, catalog database.Catalog, expectedRevision int64, request EnqueueRequest) (store.AcceptedOperation, error) {
	if p == nil || p.DB == nil {
		return store.AcceptedOperation{}, fmt.Errorf("catalog activation is unavailable")
	}
	if expectedRevision < 0 {
		return store.AcceptedOperation{}, &CatalogActivationError{Reason: "expectedRevision must be zero or a positive revision"}
	}
	if err := database.ValidateCatalog(catalog); err != nil {
		return store.AcceptedOperation{}, &CatalogActivationError{Reason: err.Error()}
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	digest := sha256.Sum256(encoded)
	payload := map[string]interface{}{
		"expectedRevision": strconv.FormatInt(expectedRevision, 10),
		"catalog":          string(encoded),
		"catalogDigest":    "sha256:" + hex.EncodeToString(digest[:]),
		"requestedBy":      request.Actor.Issuer + "/" + request.Actor.Subject,
	}
	operation := model.Operation{
		ID: uuid.NewString(), Kind: CatalogActivationKind, Ref: catalogActivationResource, SagaID: uuid.NewString(),
		Status: model.OperationQueued, Risk: "database target routing change", Source: "control-api",
		Message: "queued database catalog activation", Payload: payload, Metadata: map[string]interface{}{},
		StartedAt: time.Now().UTC(), MaxAttempts: 3,
	}
	// A retry of an accepted request replays before fresh validation, so a
	// later catalog revision cannot turn it into a spurious refusal.
	if _, err := p.ResolveEnqueue(ctx, request, CatalogActivationKind, catalogActivationResource); err == nil {
		return p.acceptOperation(ctx, request, operation, nil, nil)
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, err
	}
	active, err := p.DB.ActiveDatabaseCatalog(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if expectedRevision != 0 {
			return store.AcceptedOperation{}, &CatalogActivationError{Reason: "no catalog is active; expectedRevision must be 0"}
		}
	case err != nil:
		return store.AcceptedOperation{}, &CatalogActivationError{Reason: "the active catalog could not be read"}
	default:
		if active.Revision != expectedRevision {
			return store.AcceptedOperation{}, &CatalogActivationError{Reason: fmt.Sprintf("expectedRevision %d is not the active revision %d", expectedRevision, active.Revision)}
		}
		if err := database.ValidateTransition(active.Catalog, catalog); err != nil {
			return store.AcceptedOperation{}, &CatalogActivationError{Reason: err.Error()}
		}
	}
	return p.acceptOperation(ctx, request, operation, nil, nil)
}

// executeCatalogActivation applies an accepted activation under its live
// claim. Success is committed together with the operation's terminal record
// (see store.ActivateDatabaseCatalogClaimed), so no later claim ever has to
// infer whether an equal-looking active revision was its own.
func (p *Pipeline) executeCatalogActivation(ctx context.Context, op *model.Operation, claim store.OperationClaim) (*OperationResult, error) {
	expected, catalog, digest, err := catalogActivationPayload(op.Payload)
	if err != nil {
		return nil, err
	}
	actor, _ := op.Payload["requestedBy"].(string)
	if actor == "" {
		return nil, fmt.Errorf("catalog activation payload has no accepted actor")
	}
	metadata := map[string]interface{}{"expectedRevision": expected, "catalogDigest": digest}
	activated, err := p.DB.ActivateDatabaseCatalogClaimed(ctx, claim, expected, catalog, actor, metadata)
	var resolverErr *database.ResolverError
	refused := errors.Is(err, store.ErrDatabaseCatalogRevisionConflict) || errors.Is(err, store.ErrDatabaseCatalogRetiredIdentity) || errors.As(err, &resolverErr)
	if err != nil && !refused {
		// No live claim (or an infrastructure failure): nothing changed, and
		// this executor has no authority to report an outcome.
		return nil, err
	}
	if err != nil {
		// A refusal changed nothing; the worker records it through the claim.
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "catalog activation refused: " + err.Error(), Metadata: metadata}, nil
	}
	// The store already finished the operation in the activation's own
	// transaction; the worker must not finish it again.
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("database catalog revision %d active", activated.Revision),
		Metadata: map[string]interface{}{"expectedRevision": expected, "revision": activated.Revision, "catalogDigest": digest, "storedDigest": activated.Digest}, finished: true}, nil
}

func catalogActivationPayload(payload map[string]interface{}) (int64, database.Catalog, string, error) {
	rawExpected, _ := payload["expectedRevision"].(string)
	rawCatalog, _ := payload["catalog"].(string)
	digest, _ := payload["catalogDigest"].(string)
	expected, err := strconv.ParseInt(rawExpected, 10, 64)
	if err != nil || expected < 0 || rawCatalog == "" {
		return 0, database.Catalog{}, "", fmt.Errorf("catalog activation payload is malformed")
	}
	sum := sha256.Sum256([]byte(rawCatalog))
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return 0, database.Catalog{}, "", fmt.Errorf("catalog activation payload digest does not match")
	}
	var catalog database.Catalog
	decoder := json.NewDecoder(bytes.NewReader([]byte(rawCatalog)))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&catalog) != nil || decoder.Decode(&trailing) != io.EOF {
		return 0, database.Catalog{}, "", fmt.Errorf("catalog activation payload is malformed")
	}
	return expected, catalog, digest, nil
}
