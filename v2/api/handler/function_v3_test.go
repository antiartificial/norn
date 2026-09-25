package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type functionV3Store struct {
	authority  string
	prior      store.AcceptedOperation
	resolveErr error
	accepted   store.OperationAcceptance
	material   store.PrivateInvocationInput
	accepts    int
	opened     store.PrivateInvocationInput
	openErr    error
}

func (s *functionV3Store) Authority(context.Context) (string, error) { return s.authority, nil }
func (s *functionV3Store) Accept(context.Context, store.OperationAcceptance) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, errors.New("unexpected ordinary acceptance")
}
func (s *functionV3Store) Resolve(context.Context, store.OperationRequestIdentity, store.RequestFingerprint) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, errors.New("unexpected fingerprint resolution")
}
func (s *functionV3Store) ResolveIdentity(context.Context, store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	return s.prior, s.resolveErr
}
func (s *functionV3Store) AcceptPrivateInvocation(_ context.Context, acceptance store.OperationAcceptance, material store.PrivateInvocationInput, _ *store.PrivateInvocationKeyRing) (store.AcceptedOperation, error) {
	s.accepts++
	s.accepted, s.material = acceptance, material
	return store.AcceptedOperation{Operation: acceptance.Operation}, nil
}
func (s *functionV3Store) OpenPrivateInvocation(context.Context, model.Operation, *store.PrivateInvocationKeyRing) (store.PrivateInvocationInput, error) {
	return s.opened, s.openErr
}
func (s *functionV3Store) RequiredPrivateInvocationKeys(context.Context) ([]string, error) {
	return nil, nil
}

type functionV3Resolver struct {
	binding FunctionInvocationResolution
	calls   int
	process string
}

func (r *functionV3Resolver) ResolveFunctionInvocation(_ context.Context, _ string, process string) (FunctionInvocationResolution, error) {
	r.calls++
	r.process = process
	return r.binding, nil
}

func functionV3Keys(t *testing.T) *store.PrivateInvocationKeyRing {
	t.Helper()
	keys, err := store.NewPrivateInvocationKeyRing("current", map[string][]byte{"current": make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func functionV3HTTPTestRequest(t *testing.T, key, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v3/apps/widgets/functions/invocations", strings.NewReader(`{"process":"resize","body":"`+body+`","method":"POST","path":"/resize"}`))
	req.Header.Set("Idempotency-Key", key)
	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAPIWrite}})
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{ReceiptID: "receipt-1", RequestID: "request-1", Actor: verifiedOperationActor{Issuer: "issuer", Subject: "subject", Source: "test", Scopes: []string{ScopeAPIWrite}}})
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "widgets")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func functionV3Binding() FunctionInvocationResolution {
	return FunctionInvocationResolution{Process: "resize", SpecDigest: "sha256:" + strings.Repeat("a", 64), ImageReference: "registry.example/widgets@sha256:" + strings.Repeat("b", 64), DatabaseTarget: "none", DatabaseRevision: "none"}
}

func TestFunctionV3AdmissionSealsPrivateRequestAndAcceptsPublicReceipt(t *testing.T) {
	privateCanary := "private-canary-body"
	s := &functionV3Store{authority: "authority-1", resolveErr: &store.AcceptanceNotFoundError{}}
	resolver := &functionV3Resolver{binding: functionV3Binding()}
	h := FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{OperationStore: s, IdentityResolver: s, PrivateStore: s, PrivateKeys: functionV3Keys(t), InvocationResolver: resolver})
	rec := httptest.NewRecorder()
	h(rec, functionV3HTTPTestRequest(t, "function-key", privateCanary))
	if rec.Code != http.StatusAccepted || s.accepts != 1 || resolver.calls != 1 {
		t.Fatalf("status=%d accepts=%d resolver=%d body=%s", rec.Code, s.accepts, resolver.calls, rec.Body.String())
	}
	if s.material.Body != privateCanary || s.material.Method != "POST" || s.material.Path != "/resize" {
		t.Fatalf("private material=%+v", s.material)
	}
	public, err := store.CanonicalOperationRequest(s.accepted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), privateCanary) || strings.Contains(string(public), "POST") || strings.Contains(string(public), "/resize") {
		t.Fatalf("private request leaked into public acceptance: %s", public)
	}
	if s.accepted.Semantics["specDigest"] != functionV3Binding().SpecDigest || s.accepted.Operation.Payload["imageReference"] != functionV3Binding().ImageReference || s.accepted.Operation.Payload["databaseTarget"] != "none" {
		t.Fatalf("public binding=%+v", s.accepted.Operation.Payload)
	}
	if rec.Header().Get("Location") != "/api/v1/operations/"+s.accepted.Operation.ID || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), privateCanary) {
		t.Fatalf("headers=%v body=%s", rec.Header(), rec.Body.String())
	}
}

func TestFunctionV3AdmissionReplaysBeforeCurrentBindingResolution(t *testing.T) {
	privateCanary := "private-canary-body"
	s := &functionV3Store{authority: "authority-1", prior: store.AcceptedOperation{Operation: model.Operation{ID: "prior-op", Kind: store.PrivateInvocationOperationKind, App: "widgets", Payload: map[string]interface{}{"process": "resize"}}}, opened: store.PrivateInvocationInput{Body: privateCanary, Method: "POST", Path: "/resize"}}
	resolver := &functionV3Resolver{binding: functionV3Binding()}
	h := FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{OperationStore: s, IdentityResolver: s, PrivateStore: s, PrivateKeys: functionV3Keys(t), InvocationResolver: resolver})
	rec := httptest.NewRecorder()
	h(rec, functionV3HTTPTestRequest(t, "same-key", privateCanary))
	if rec.Code != http.StatusOK || s.accepts != 0 || resolver.calls != 0 || !strings.Contains(rec.Body.String(), "prior-op") {
		t.Fatalf("status=%d accepts=%d resolver=%d body=%s", rec.Code, s.accepts, resolver.calls, rec.Body.String())
	}
}

func TestFunctionV3AdmissionRejectsChangedPrivateReplayWithoutResolvingCurrentState(t *testing.T) {
	s := &functionV3Store{authority: "authority-1", prior: store.AcceptedOperation{Operation: model.Operation{ID: "prior-op", Kind: store.PrivateInvocationOperationKind, App: "widgets", Payload: map[string]interface{}{"process": "resize"}}}, opened: store.PrivateInvocationInput{Body: "original", Method: "POST", Path: "/resize"}}
	resolver := &functionV3Resolver{binding: functionV3Binding()}
	h := FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{OperationStore: s, IdentityResolver: s, PrivateStore: s, PrivateKeys: functionV3Keys(t), InvocationResolver: resolver})
	rec := httptest.NewRecorder()
	h(rec, functionV3HTTPTestRequest(t, "same-key", "changed"))
	if rec.Code != http.StatusConflict || s.accepts != 0 || resolver.calls != 0 || !strings.Contains(rec.Body.String(), "idempotency_key_reused") || strings.Contains(rec.Body.String(), "original") {
		t.Fatalf("status=%d accepts=%d resolver=%d body=%s", rec.Code, s.accepts, resolver.calls, rec.Body.String())
	}
}

func TestFunctionV3AdmissionCapabilityIsOptionalAndFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{})(rec, functionV3HTTPTestRequest(t, "function-key", "private"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "function_invocation_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFunctionV3AdmissionResolverSelectsLegacyDefaultProcess(t *testing.T) {
	s := &functionV3Store{authority: "authority-1", resolveErr: &store.AcceptanceNotFoundError{}}
	resolver := &functionV3Resolver{binding: functionV3Binding()}
	h := FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{OperationStore: s, IdentityResolver: s, PrivateStore: s, PrivateKeys: functionV3Keys(t), InvocationResolver: resolver})
	req := functionV3HTTPTestRequest(t, "default-process", "private")
	req.Body = io.NopCloser(strings.NewReader(`{"body":"private","method":"POST","path":"/resize"}`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusAccepted || resolver.process != "" || s.accepted.Operation.Payload["process"] != "resize" {
		t.Fatalf("status=%d resolver process=%q payload=%+v", rec.Code, resolver.process, s.accepted.Operation.Payload)
	}
}

func TestFunctionV3AdmissionRejectsMalformedTrailingJSON(t *testing.T) {
	s := &functionV3Store{authority: "authority-1", resolveErr: &store.AcceptanceNotFoundError{}}
	resolver := &functionV3Resolver{binding: functionV3Binding()}
	h := FunctionV3AdmissionHandler(FunctionV3AdmissionConfig{OperationStore: s, IdentityResolver: s, PrivateStore: s, PrivateKeys: functionV3Keys(t), InvocationResolver: resolver})
	req := functionV3HTTPTestRequest(t, "bad-json", "private")
	req.Body = io.NopCloser(strings.NewReader(`{"process":"resize"} {`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest || s.accepts != 0 || resolver.calls != 0 {
		t.Fatalf("status=%d accepts=%d resolver=%d body=%s", rec.Code, s.accepts, resolver.calls, rec.Body.String())
	}
}
