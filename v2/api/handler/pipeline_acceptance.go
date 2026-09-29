package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"

	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func (h *Handler) pipelineEnqueueRequest(w http.ResponseWriter, r *http.Request, key string, semantics map[string]interface{}) (pipeline.EnqueueRequest, bool) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
		return pipeline.EnqueueRequest{}, false
	}
	requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
	if !ok || requestContext.ReceiptID == "" || h.operationStore == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return pipeline.EnqueueRequest{}, false
	}
	if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
		WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "the authenticated credential does not establish a stable operation actor")
		return pipeline.EnqueueRequest{}, false
	}
	authority, err := h.operationStore.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return pipeline.EnqueueRequest{}, false
	}
	return pipeline.EnqueueRequest{
		Authority: authority,
		Actor:     store.OperationActor{Issuer: requestContext.Actor.Issuer, Subject: requestContext.Actor.Subject},
		Key:       key,
		Audit: store.AcceptanceAuditContext{
			RequestReceiptID: requestContext.ReceiptID, RequestID: requestContext.RequestID,
			CredentialID: requestContext.Actor.CredentialID, DeviceID: requestContext.Actor.DeviceID,
			Source: requestContext.Actor.Source, Scopes: append([]string(nil), requestContext.Actor.Scopes...),
		},
		Semantics: semantics,
	}, true
}

func (h *Handler) systemPipelineEnqueueRequest(r *http.Request, actorSubject, key, source string, semantics map[string]interface{}) (pipeline.EnqueueRequest, error) {
	if h == nil || h.operationStore == nil {
		return pipeline.EnqueueRequest{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	authority, err := h.operationStore.Authority(r.Context())
	if err != nil {
		return pipeline.EnqueueRequest{}, err
	}
	return pipeline.EnqueueRequest{
		Authority: authority,
		Actor:     store.OperationActor{Issuer: authority + "/system", Subject: actorSubject},
		Key:       key,
		Audit:     store.AcceptanceAuditContext{Source: source},
		Semantics: semantics,
	}, nil
}

func (h *Handler) resolveRollbackReplay(ctx context.Context, request pipeline.EnqueueRequest, app string, regions []string, sourceDeploymentID string) (store.AcceptedOperation, bool, error) {
	accepted, err := h.pipeline.ResolveEnqueue(ctx, request, "app.rollback", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, false, nil
	}
	if err != nil {
		return store.AcceptedOperation{}, false, err
	}
	if !acceptedRequestMatches(accepted, request) {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.rollback", Resource: app}}
	}
	wantRegions := append([]string(nil), regions...)
	gotRegions := acceptanceStringSlice(accepted.Operation.Payload["regions"])
	sort.Strings(wantRegions)
	sort.Strings(gotRegions)
	gotSource, _ := accepted.Operation.Payload["sourceDeploymentId"].(string)
	if accepted.Operation.Kind != "app.rollback" || accepted.Operation.App != app || !slices.Equal(wantRegions, gotRegions) || (sourceDeploymentID != "" && gotSource != sourceDeploymentID) {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.rollback", Resource: app}}
	}
	return accepted, true, nil
}

// acceptedRequestMatches compares only the stable producer inputs covered by
// the verified signed request. Dynamic targets selected by the control plane
// live in the immutable accepted operation payload, not in this comparison.
func acceptedRequestMatches(accepted store.AcceptedOperation, request pipeline.EnqueueRequest) bool {
	var canonical struct {
		Admission           json.RawMessage `json:"admission"`
		FleetReconciliation json.RawMessage `json:"fleetReconciliation"`
		FleetRunnerAttempt  json.RawMessage `json:"fleetRunnerAttempt"`
		Semantics           json.RawMessage `json:"semantics"`
	}
	if err := json.Unmarshal(accepted.Intent.RequestCanonicalBytes, &canonical); err != nil {
		return false
	}
	wantAdmission, err := json.Marshal(request.Admission)
	if err != nil || !jsonStructurallyEqual(canonical.Admission, wantAdmission) {
		return false
	}
	wantFleetReconciliation, err := json.Marshal(request.FleetReconciliation)
	if err != nil || !jsonStructurallyEqual(canonical.FleetReconciliation, wantFleetReconciliation) {
		return false
	}
	var fleetRunnerAttempt interface{} = request.FleetRunnerAttempt
	if request.FleetRunnerAttempt != nil {
		copy := *request.FleetRunnerAttempt
		copy.AttemptID = ""
		copy.ExpectedPredecessorID = ""
		fleetRunnerAttempt = &copy
	}
	wantFleetRunnerAttempt, err := json.Marshal(fleetRunnerAttempt)
	if err != nil || !jsonStructurallyEqual(canonical.FleetRunnerAttempt, wantFleetRunnerAttempt) {
		return false
	}
	wantSemantics, err := json.Marshal(request.Semantics)
	return err == nil && jsonStructurallyEqual(canonical.Semantics, wantSemantics)
}

func jsonStructurallyEqual(left, right []byte) bool {
	decode := func(raw []byte) (interface{}, bool) {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte("null")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value interface{}
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		return value, true
	}
	a, ok := decode(left)
	if !ok {
		return false
	}
	b, ok := decode(right)
	return ok && reflect.DeepEqual(a, b)
}

func acceptanceStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
