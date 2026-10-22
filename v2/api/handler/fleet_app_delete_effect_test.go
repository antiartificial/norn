package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/fleet/appdelete"
)

type appDeleteStoreFake struct {
	err   error
	calls int
}

func (s *appDeleteStoreFake) ConsumeAppDeleteCoPermit(_ context.Context, _ appdelete.Request, _ string) error {
	s.calls++
	return s.err
}

func TestAppDeleteEffectHandlerRequiresAttemptBindingAndReturnsExactGrant(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	request := appdelete.Request{SchemaVersion: appdelete.RequestSchema, PlanID: planID, AttemptID: attemptID,
		WriterGeneration: 9, WriterHolder: uuid.NewString(), Target: "app-03", DropletID: "610327215", ProviderAccount: uuid.NewString(),
		AuthorizationSHA256: strings.Repeat("a", 64), WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: appdelete.ExactDeleteRequestSHA256("610327215")}
	principal := AccessPrincipal{Subject: "fleet-attempt:" + attemptID, TokenID: "token", Source: AccessPrincipalSourceManagedToken,
		Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
	store := &appDeleteStoreFake{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-effect", AppDeleteEffectHandler(store))
	raw, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/attempts/"+attemptID+"/app-delete-effect", bytes.NewReader(raw))
	httpRequest = WithAccessPrincipal(httpRequest, &principal)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK || store.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, store.calls, recorder.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &got)
	if len(got) != 12 || got["schemaVersion"] != appdelete.GrantSchema || got["authorizesEffect"] != true {
		t.Fatalf("unexpected grant: %v", got)
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("response may be cached")
	}

	wrong := principal
	wrong.Subject = "fleet-attempt:" + uuid.NewString()
	httpRequest = httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/attempts/"+attemptID+"/app-delete-effect", bytes.NewReader(raw))
	httpRequest = WithAccessPrincipal(httpRequest, &wrong)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusForbidden || store.calls != 1 {
		t.Fatalf("wrong attempt reached store: status=%d calls=%d", recorder.Code, store.calls)
	}
}

func TestAppDeleteEffectHandlerClosesReplayAndAmbiguousFailure(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	request := appdelete.Request{SchemaVersion: appdelete.RequestSchema, PlanID: planID, AttemptID: attemptID,
		WriterGeneration: 9, WriterHolder: uuid.NewString(), Target: "app-03", DropletID: "610327215", ProviderAccount: uuid.NewString(),
		AuthorizationSHA256: strings.Repeat("a", 64), WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: appdelete.ExactDeleteRequestSHA256("610327215")}
	principal := AccessPrincipal{Subject: "fleet-attempt:" + attemptID, TokenID: "token", Source: AccessPrincipalSourceManagedToken,
		Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
	store := &appDeleteStoreFake{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-effect", AppDeleteEffectHandler(store))
	raw, _ := json.Marshal(request)
	call := func() *httptest.ResponseRecorder {
		r := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/attempts/"+attemptID+"/app-delete-effect", bytes.NewReader(raw)), &principal)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	store.err = appdelete.ErrRefused
	if got := call(); got.Code != http.StatusConflict || strings.Contains(got.Body.String(), "authorizesEffect") {
		t.Fatalf("replay: %d %s", got.Code, got.Body.String())
	}
	store.err = errors.New("etcd commit reply lost")
	if got := call(); got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "authorizesEffect") {
		t.Fatalf("ambiguous failure: %d %s", got.Code, got.Body.String())
	}
	if store.calls != 2 {
		t.Fatalf("store calls=%d", store.calls)
	}
}
