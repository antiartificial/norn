package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

var functionV3Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var functionV3Image = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)

// functionV3RecoveryAttempts permits a successor to recover a fenced function
// claim after an API-worker crash. The variable and job effect ledgers record
// an attempted write before the remote call, so later claims can only recover
// an exact remote identity; they cannot submit a second one-shot job. Once the
// bounded budget is exhausted, generic operation recovery leaves the receipt
// in manual review rather than guessing about an unproved remote state.
const functionV3RecoveryAttempts = 3

// FunctionInvocationResolution is the complete public execution binding. The
// resolver owns reading deployed state and must return the exact pinned spec,
// immutable image, and canonical database delivery binding selected for this
// operation. Request material deliberately has no field here.
type FunctionInvocationResolution struct {
	Process          string
	SpecDigest       string
	ImageReference   string
	DatabaseTarget   string
	DatabaseRevision string
}

// FunctionInvocationResolver resolves the public, immutable invocation
// binding. It is injected because HTTP admission must not infer it from the
// legacy synchronous function handler.
type FunctionInvocationResolver interface {
	ResolveFunctionInvocation(context.Context, string, string) (FunctionInvocationResolution, error)
}

// FunctionV3AdmissionConfig is the narrow optional capability required to
// expose the v3 function admission endpoint. Startup and routing deliberately
// remain outside this seam.
type FunctionV3AdmissionConfig struct {
	OperationStore     store.OperationStore
	IdentityResolver   store.OperationIdentityResolver
	PrivateStore       store.PrivateInvocationStore
	PrivateKeys        *store.PrivateInvocationKeyRing
	InvocationResolver FunctionInvocationResolver
}

type functionV3Request struct {
	Process string `json:"process"`
	Body    string `json:"body"`
	Method  string `json:"method"`
	Path    string `json:"path"`
}

// FunctionV3AdmissionHandler admits one durable function invocation. It is
// intentionally separate from InvokeFunction: callers must explicitly wire
// this handler only after private-invocation capability startup succeeds.
func FunctionV3AdmissionHandler(cfg FunctionV3AdmissionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		if _, ok := requireControlScope(w, r, ScopeAPIWrite); !ok {
			return
		}
		if cfg.OperationStore == nil || cfg.IdentityResolver == nil || cfg.PrivateStore == nil || cfg.PrivateKeys == nil || cfg.InvocationResolver == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "function_invocation_unavailable", "private function invocation admission is unavailable")
			return
		}

		var req functionV3Request
		if err := decodeFunctionV3Request(r, &req); err != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		req.Process = strings.TrimSpace(req.Process)
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
			return
		}
		requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
		if !ok || requestContext.ReceiptID == "" {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
			return
		}
		if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
			WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "the authenticated credential does not establish a stable operation actor")
			return
		}
		authority, err := cfg.OperationStore.Authority(r.Context())
		if err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
			return
		}
		app := chi.URLParam(r, "id")
		if !validAppIDRe.MatchString(app) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_id", "app ID is invalid")
			return
		}
		identity := store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: requestContext.Actor.Issuer, Subject: requestContext.Actor.Subject}, Kind: store.PrivateInvocationOperationKind, Resource: app, Key: key}
		private := store.PrivateInvocationInput{Body: req.Body, Method: req.Method, Path: req.Path}

		// Resolve replay before looking at current deployment state. A retry is
		// bound to the accepted record, even when later deployment state drifted.
		if prior, err := cfg.IdentityResolver.ResolveIdentity(r.Context(), identity); err == nil {
			matches, replayErr := functionV3ReplayMatches(r.Context(), cfg.PrivateStore, cfg.PrivateKeys, prior, app, req.Process, private)
			if replayErr != nil {
				WriteControlProblem(w, r, http.StatusServiceUnavailable, "function_invocation_unavailable", "private function invocation admission is unavailable")
				return
			}
			if !matches {
				WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different operation request")
				return
			}
			w.Header().Set("Location", "/api/v1/operations/"+prior.Operation.ID)
			writeJSON(w, prior.Operation)
			return
		} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
			writeOperationAcceptanceError(w, r, err)
			return
		}

		binding, err := cfg.InvocationResolver.ResolveFunctionInvocation(r.Context(), app, req.Process)
		if err != nil || !validFunctionV3Binding(binding) || (req.Process != "" && binding.Process != req.Process) {
			WriteControlProblem(w, r, http.StatusConflict, "function_binding_unavailable", "an immutable function execution binding is unavailable")
			return
		}
		payload := map[string]interface{}{"process": binding.Process, "specDigest": binding.SpecDigest, "imageReference": binding.ImageReference, "databaseTarget": binding.DatabaseTarget, "databaseRevision": binding.DatabaseRevision}
		now := time.Now().UTC()
		acceptance := store.OperationAcceptance{
			Identity:  identity,
			Operation: model.Operation{ID: uuid.NewString(), Kind: store.PrivateInvocationOperationKind, App: app, Status: model.OperationQueued, Risk: "function execution", Source: "control-api", Message: "queued app.function-invoke", Payload: payload, Metadata: map[string]interface{}{}, StartedAt: now, MaxAttempts: functionV3RecoveryAttempts},
			Audit:     store.AcceptanceAuditContext{RequestReceiptID: requestContext.ReceiptID, RequestID: requestContext.RequestID, CredentialID: requestContext.Actor.CredentialID, DeviceID: requestContext.Actor.DeviceID, Source: requestContext.Actor.Source, Scopes: append([]string{}, requestContext.Actor.Scopes...)},
			// Semantics must remain public. Private request values are compared by
			// PrivateInvocationStore during atomic replay, never fingerprinted here.
			Semantics: map[string]interface{}{"process": binding.Process, "specDigest": binding.SpecDigest, "imageReference": binding.ImageReference, "databaseTarget": binding.DatabaseTarget, "databaseRevision": binding.DatabaseRevision},
		}
		accepted, err := cfg.PrivateStore.AcceptPrivateInvocation(r.Context(), acceptance, private, cfg.PrivateKeys)
		if err != nil {
			writeOperationAcceptanceError(w, r, err)
			return
		}
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		if accepted.Replayed {
			writeJSON(w, accepted.Operation)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
	}
}

func decodeFunctionV3Request(r *http.Request, target *functionV3Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxControlJSONBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxControlJSONBody {
		return errors.New("request exceeds configured size")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("request must contain one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func validFunctionV3Binding(binding FunctionInvocationResolution) bool {
	return strings.TrimSpace(binding.Process) != "" && functionV3Digest.MatchString(binding.SpecDigest) && functionV3Image.MatchString(binding.ImageReference) && strings.TrimSpace(binding.DatabaseTarget) != "" && strings.TrimSpace(binding.DatabaseRevision) != ""
}

func functionV3ReplayMatches(ctx context.Context, privateStore store.PrivateInvocationStore, keys *store.PrivateInvocationKeyRing, prior store.AcceptedOperation, app, process string, want store.PrivateInvocationInput) (bool, error) {
	if prior.Operation.Kind != store.PrivateInvocationOperationKind || prior.Operation.App != app || (process != "" && store.PrivateInvocationProcess(prior.Operation.Payload) != process) {
		return false, nil
	}
	got, err := privateStore.OpenPrivateInvocation(ctx, prior.Operation, keys)
	if err != nil {
		return false, err
	}
	wantBytes, wantErr := json.Marshal(want)
	gotBytes, gotErr := json.Marshal(got)
	if wantErr != nil || gotErr != nil {
		return false, errors.New("private invocation request comparison failed")
	}
	wantSum, gotSum := sha256.Sum256(wantBytes), sha256.Sum256(gotBytes)
	return subtle.ConstantTimeCompare(wantSum[:], gotSum[:]) == 1, nil
}
