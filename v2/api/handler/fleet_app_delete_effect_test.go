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
	err       error
	calls     int
	receipt   appdelete.TerminalReceipt
	admission appdelete.App04Admission
}

type appDeleteSnapshotFake struct {
	calls int
	value appdelete.Snapshot
}

func (s *appDeleteSnapshotFake) GetAppDeleteSnapshot(_ context.Context, planID, attemptID string) (appdelete.Snapshot, error) {
	s.calls++
	if s.value.PlanID != planID || s.value.AttemptID != attemptID {
		return appdelete.Snapshot{}, appdelete.ErrNotFound
	}
	return s.value, nil
}

func TestAppDeleteSnapshotVerifierOnly(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	s := &appDeleteSnapshotFake{value: appdelete.Snapshot{SchemaVersion: appdelete.SnapshotSchema, PlanID: planID, AttemptID: attemptID, EtcdRevision: 17}}
	router := chi.NewRouter()
	router.Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-snapshot", AppDeleteSnapshotHandler(s))
	path := "/api/v1/fleet/plans/" + planID + "/attempts/" + attemptID + "/app-delete-snapshot"
	call := func(p AccessPrincipal) *httptest.ResponseRecorder {
		req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, nil), &p)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	bad := AccessPrincipal{Subject: "fleet-attempt:" + attemptID, TokenID: "a", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
	if got := call(bad); got.Code != http.StatusForbidden || s.calls != 0 {
		t.Fatalf("attempt token read=%d calls=%d", got.Code, s.calls)
	}
	good := AccessPrincipal{Subject: appDeleteVerifierSubject, TokenID: "v", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetAppDeleteVerify}, ExpiresAt: time.Now().Add(time.Hour)}
	if got := call(good); got.Code != http.StatusOK || s.calls != 1 || !strings.Contains(got.Body.String(), `"etcdRevision":17`) {
		t.Fatalf("verifier read=%d calls=%d body=%s", got.Code, s.calls, got.Body.String())
	}
}

func (s *appDeleteStoreFake) RecordApp04DeleteAdmission(_ context.Context, a appdelete.App04Admission, _ string) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.admission = a
	return nil
}
func (s *appDeleteStoreFake) GetApp04DeleteAdmission(_ context.Context, _ string) (appdelete.App04Admission, error) {
	s.calls++
	if s.err != nil {
		return appdelete.App04Admission{}, s.err
	}
	return s.admission, nil
}

func TestApp04AdmissionHandlerVerifierOnlyAndReadback(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	r := terminalFixture(planID, attemptID, "app-04", "4242")
	a := appdelete.App04Admission{SchemaVersion: appdelete.App04AdmissionSchema, PlanID: planID, AttemptID: attemptID,
		PlanSHA256: strings.Repeat("c", 64), SourceCommit: strings.Repeat("d", 40), TargetID: "work-staging",
		TargetFenceGeneration: 3, AuthorityEpoch: 1, Target: r.Target, DropletID: r.DropletID,
		ProviderAccount: r.ProviderAccount, SavedPlanSHA256: strings.Repeat("e", 64),
		WriterGeneration: r.WriterGeneration, WriterHolder: r.WriterHolder,
		AuthorizationSHA256: r.AuthorizationSHA256, WorkloadReceiptSHA256: r.WorkloadReceiptSHA256,
		DeleteRequestSHA256: r.DeleteRequestSHA256}
	raw, _ := json.Marshal(a)
	store := &appDeleteStoreFake{}
	router := chi.NewRouter()
	route := "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app04-delete-admission"
	path := "/api/v1/fleet/plans/" + planID + "/attempts/" + attemptID + "/app04-delete-admission"
	router.Post(route, App04DeleteAdmissionHandler(store))
	router.Get(route, App04DeleteAdmissionHandler(store))
	verifier := AccessPrincipal{Subject: appDeleteVerifierSubject, TokenID: "v", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetAppDeleteVerify}, ExpiresAt: time.Now().Add(time.Hour)}
	call := func(method string, p AccessPrincipal) *httptest.ResponseRecorder {
		req := WithAccessPrincipal(httptest.NewRequest(method, path, bytes.NewReader(raw)), &p)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	wrong := verifier
	wrong.Subject = "fleet-attempt:" + attemptID
	wrong.Scopes = []string{ScopeFleetAttempt}
	if got := call(http.MethodPost, wrong); got.Code != http.StatusForbidden || store.calls != 0 {
		t.Fatalf("wrong principal=%d", got.Code)
	}
	if got := call(http.MethodPost, verifier); got.Code != http.StatusCreated {
		t.Fatalf("post=%d %s", got.Code, got.Body.String())
	}
	if got := call(http.MethodGet, verifier); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"target":"app-04"`) {
		t.Fatalf("get=%d %s", got.Code, got.Body.String())
	}
}

func (s *appDeleteStoreFake) ConsumeAppDeleteCoPermit(_ context.Context, _ appdelete.Request, _ string) error {
	s.calls++
	return s.err
}
func (s *appDeleteStoreFake) RecordAppDeleteTerminal(_ context.Context, r appdelete.TerminalRequest, _ string) (appdelete.TerminalReceipt, error) {
	s.calls++
	if s.err != nil {
		return appdelete.TerminalReceipt{}, s.err
	}
	s.receipt = appdelete.ReceiptFromTerminal(r)
	return s.receipt, nil
}
func (s *appDeleteStoreFake) GetAppDeleteTerminal(_ context.Context, _ string) (appdelete.TerminalReceipt, error) {
	s.calls++
	if s.err != nil {
		return appdelete.TerminalReceipt{}, s.err
	}
	return s.receipt, nil
}

func terminalFixture(planID, attemptID, target, droplet string) appdelete.TerminalRequest {
	return appdelete.TerminalRequest{SchemaVersion: appdelete.TerminalRequestSchema, PlanID: planID, AttemptID: attemptID, WriterGeneration: 9, WriterHolder: uuid.NewString(), Target: target, DropletID: droplet, ProviderAccount: uuid.NewString(), AuthorizationSHA256: strings.Repeat("a", 64), WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: appdelete.ExactDeleteRequestSHA256(droplet), RefreshDispatchSHA256: strings.Repeat("c", 64), StateSHA256: strings.Repeat("d", 64), ProviderAbsenceSHA256: strings.Repeat("e", 64), WorkloadSHA256: strings.Repeat("f", 64), PublicPilotSHA256: strings.Repeat("1", 64), ObservationSHA256: strings.Repeat("2", 64)}
}

func TestAppDeleteTerminalRequiresVerifierAndSupportsUnknownOutcomeReadback(t *testing.T) {
	for _, target := range []struct{ name, id string }{{"app-04", "4242"}, {"app-03", "610327215"}} {
		t.Run(target.name, func(t *testing.T) {
			planID, attemptID := uuid.NewString(), uuid.NewString()
			body := terminalFixture(planID, attemptID, target.name, target.id)
			raw, _ := json.Marshal(body)
			store := &appDeleteStoreFake{}
			router := chi.NewRouter()
			path := "/api/v1/fleet/plans/" + planID + "/attempts/" + attemptID + "/app-delete-terminal"
			router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-terminal", AppDeleteTerminalHandler(store))
			router.Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-terminal", AppDeleteTerminalHandler(store))
			verifier := AccessPrincipal{Subject: appDeleteVerifierSubject, TokenID: "v", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetAppDeleteVerify}, ExpiresAt: time.Now().Add(time.Hour)}
			call := func(method string, p AccessPrincipal) *httptest.ResponseRecorder {
				req := WithAccessPrincipal(httptest.NewRequest(method, path, bytes.NewReader(raw)), &p)
				out := httptest.NewRecorder()
				router.ServeHTTP(out, req)
				return out
			}
			attempt := verifier
			attempt.Subject = "fleet-attempt:" + attemptID
			attempt.Scopes = []string{ScopeFleetAttempt}
			if got := call(http.MethodPost, attempt); got.Code != http.StatusForbidden || store.calls != 0 {
				t.Fatalf("attempt scope crossed boundary: %d", got.Code)
			}
			if got := call(http.MethodPost, verifier); got.Code != http.StatusCreated || store.calls != 1 {
				t.Fatalf("post=%d %s", got.Code, got.Body.String())
			}
			store.err = errors.New("lost response")
			if got := call(http.MethodPost, verifier); got.Code != http.StatusServiceUnavailable {
				t.Fatalf("uncertain=%d", got.Code)
			}
			store.err = nil
			if got := call(http.MethodGet, verifier); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"terminalEvidenceRecorded":true`) {
				t.Fatalf("readback=%d %s", got.Code, got.Body.String())
			}
		})
	}
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
