package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type etcdCatalogActivationRequest struct {
	ExpectedRevision *int64            `json:"expectedRevision"`
	Catalog          *database.Catalog `json:"catalog"`
}

func etcdFleetCatalogActivation(operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.TokenID == "" || principal.CI != nil || !principal.Allows(handler.ScopePlatformOperate) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "catalog activation requires a non-CI managed platform:operate principal")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
			return
		}
		var request etcdCatalogActivationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ExpectedRevision == nil || request.Catalog == nil || *request.ExpectedRevision < 0 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_catalog_activation", "expectedRevision and catalog are required as one valid JSON object")
			return
		}
		encoded, err := json.Marshal(request.Catalog)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_catalog_activation", "catalog could not be encoded")
			return
		}
		digest := sha256.Sum256(encoded)
		digestHex := hex.EncodeToString(digest[:])
		authority, err := operations.Authority(r.Context())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "authority_unavailable", "control authority is unavailable")
			return
		}
		actor := store.OperationActor{Issuer: "norn://managed-token", Subject: principal.TokenID}
		identity := store.OperationRequestIdentity{Authority: authority, Actor: actor, Kind: pipeline.CatalogActivationKind, Resource: "database-catalog", Key: key}
		accepted, err := operations.ResolveIdentity(r.Context(), identity)
		if err == nil {
			if !etcdCatalogReplayMatches(accepted, *request.ExpectedRevision, string(encoded), actor.Issuer+"/"+actor.Subject) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different catalog activation")
				return
			}
			accepted.Operation.AttachReceipt()
			w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
			writeEtcdSourceJSON(w, http.StatusOK, accepted.Operation)
			return
		}
		if !errors.Is(err, store.ErrAcceptanceNotFound) {
			writeEtcdFleetAcceptanceError(w, r, err)
			return
		}
		if err := database.ValidateCatalog(*request.Catalog); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_catalog_activation", err.Error())
			return
		}
		for _, service := range request.Catalog.Services {
			if service.Engine != database.EnginePostgreSQL {
				handler.WriteControlProblem(w, r, http.StatusConflict, "catalog_activation_unsupported", "the etcd catalog currently supports PostgreSQL services only")
				return
			}
		}
		active, err := operations.ActiveDatabaseCatalog(r.Context())
		switch {
		case errors.Is(err, etcdstore.ErrNotFound):
			if *request.ExpectedRevision != 0 {
				handler.WriteControlProblem(w, r, http.StatusConflict, "catalog_activation_refused", "no catalog is active; expectedRevision must be 0")
				return
			}
		case err != nil:
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "database_catalog_unreadable", "the active database catalog could not be verified")
			return
		default:
			if active.Revision != *request.ExpectedRevision {
				handler.WriteControlProblem(w, r, http.StatusConflict, "catalog_activation_refused", "expectedRevision is not the active revision")
				return
			}
			if err := database.ValidateTransition(active.Catalog, *request.Catalog); err != nil {
				handler.WriteControlProblem(w, r, http.StatusConflict, "catalog_activation_refused", err.Error())
				return
			}
		}
		now := time.Now().UTC()
		operation := model.Operation{ID: uuid.NewString(), Kind: pipeline.CatalogActivationKind, Ref: "database-catalog", SagaID: uuid.NewString(),
			Status: model.OperationQueued, Risk: "database target routing change", Source: "etcd-control-api", Message: "queued database catalog activation",
			Payload: map[string]interface{}{"expectedRevision": strconv.FormatInt(*request.ExpectedRevision, 10), "catalog": string(encoded),
				"catalogDigest": "sha256:" + digestHex, "requestedBy": actor.Issuer + "/" + actor.Subject},
			Metadata: map[string]interface{}{}, StartedAt: now, MaxAttempts: 3}
		acceptance := store.OperationAcceptance{Identity: identity, Operation: operation,
			Audit: store.AcceptanceAuditContext{RequestReceiptID: uuid.NewString(), RequestID: middleware.GetReqID(r.Context()),
				CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-normal-fleet", Scopes: principal.Scopes},
			Semantics: map[string]interface{}{"expectedRevision": strconv.FormatInt(*request.ExpectedRevision, 10), "catalogDigest": "sha256:" + digestHex}}
		acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "operation_acceptance_failed", "failed to fingerprint catalog activation")
			return
		}
		accepted, err = operations.Accept(r.Context(), acceptance)
		if err != nil {
			writeEtcdFleetAcceptanceError(w, r, err)
			return
		}
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		status := http.StatusAccepted
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, accepted.Operation)
	}
}

func etcdCatalogReplayMatches(accepted store.AcceptedOperation, expected int64, encoded, actor string) bool {
	if accepted.Operation.Kind != pipeline.CatalogActivationKind || accepted.Operation.Ref != "database-catalog" {
		return false
	}
	payload := accepted.Operation.Payload
	return payload["expectedRevision"] == strconv.FormatInt(expected, 10) && payload["catalog"] == encoded && payload["requestedBy"] == actor
}
