package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/savedplaneffect"
)

type savedPlanEffectFake struct{ consumeCalls, recoveryCalls, projectionCreates int }

func (f *savedPlanEffectFake) RecordSavedPlanEffectPlanBinding(context.Context, savedplaneffect.PlanBinding, string) error {
	return nil
}
func (f *savedPlanEffectFake) GetSavedPlanEffectPlanBinding(context.Context, string) (savedplaneffect.PlanBinding, error) {
	return savedplaneffect.PlanBinding{}, nil
}
func (f *savedPlanEffectFake) RecordSavedPlanEffectDispatchBinding(context.Context, savedplaneffect.DispatchBinding, string) error {
	return nil
}
func (f *savedPlanEffectFake) GetSavedPlanEffectDispatchBinding(context.Context, string) (savedplaneffect.DispatchBinding, error) {
	return savedplaneffect.DispatchBinding{}, nil
}
func (f *savedPlanEffectFake) CreateSavedPlanEffectPreparation(context.Context, string, string, time.Time) (savedplaneffect.SignedPreparationProjection, error) {
	f.projectionCreates++
	return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
}
func (f *savedPlanEffectFake) GetSavedPlanEffectPreparation(context.Context, string, string) (savedplaneffect.SignedPreparationProjection, error) {
	return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
}

func TestSavedPlanProjectionCreateIsVerifierOnlyAndHasNoCallerFields(t *testing.T) {
	plan := uuid.NewString()
	fake := &savedPlanEffectFake{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/saved-plan-effect-preparation", SavedPlanEffectPreparationHandler(fake))
	good := AccessPrincipal{Subject: savedPlanEffectVerifierSubject, TokenID: "verifier", Scopes: []string{ScopeFleetSavedPlanEffectVerify}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	path := "/api/v1/fleet/plans/" + plan + "/saved-plan-effect-preparation"
	bad := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"node":"app-03"}`)), &good)
	badOut := httptest.NewRecorder()
	router.ServeHTTP(badOut, bad)
	if badOut.Code != http.StatusBadRequest || fake.projectionCreates != 0 {
		t.Fatalf("caller material=%d calls=%d", badOut.Code, fake.projectionCreates)
	}
	request := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)), &good)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, request)
	// The fake refuses the state-derived create, but it proves the handler
	// accepted only the empty body and did not turn it into apply authority.
	if out.Code != http.StatusConflict || fake.projectionCreates != 1 || strings.Contains(out.Body.String(), `"authorizesApply":true`) {
		t.Fatalf("empty create=%d %s", out.Code, out.Body.String())
	}
}
func (f *savedPlanEffectFake) ListSavedPlanEffectProjectionCandidates(context.Context, string) ([]etcdstore.SavedPlanEffectProjectionCandidate, error) {
	return nil, nil
}
func (f *savedPlanEffectFake) RecordSavedPlanEffectClaim(context.Context, savedplaneffect.Claim, string, time.Time) error {
	return nil
}
func (f *savedPlanEffectFake) ConsumeSavedPlanEffectApply(_ context.Context, plan, attempt, principal string, now time.Time) (savedplaneffect.ApplyGrant, error) {
	f.consumeCalls++
	return savedplaneffect.ApplyGrant{SchemaVersion: savedplaneffect.GrantSchema, ID: uuid.NewString(), ClaimID: uuid.NewString(), ClaimSHA256: strings.Repeat("a", 64), PlanID: plan, AttemptID: attempt, Command: savedplaneffect.ApplyCommand, AuthorizesApply: true, ConsumedAt: now, ExpiresAt: now.Add(time.Second)}, nil
}
func (f *savedPlanEffectFake) GetSavedPlanEffectRecovery(_ context.Context, plan, attempt, principal string) (etcdstore.SavedPlanEffectRecovery, error) {
	f.recoveryCalls++
	return etcdstore.SavedPlanEffectRecovery{Claim: savedplaneffect.Claim{PlanID: plan, AttemptID: attempt}, AuthorizesApply: false}, nil
}
func (f *savedPlanEffectFake) GetSavedPlanEffectClaim(_ context.Context, plan, attempt, principal string) (etcdstore.SavedPlanEffectClaimRead, error) {
	return etcdstore.SavedPlanEffectClaimRead{Claim: savedplaneffect.Claim{PlanID: plan, AttemptID: attempt}, ClaimSHA256: strings.Repeat("a", 64), AuthorizesApply: false}, nil
}
func (f *savedPlanEffectFake) AuthorizeSavedPlanRefreshOnly(context.Context, string, string, string, string, time.Time) (savedplaneffect.RefreshOnlyAuthorization, error) {
	return savedplaneffect.RefreshOnlyAuthorization{}, nil
}

func TestSavedPlanEffectApplyIsExactAttemptBoundAndRecoveryIsNonAuthorizing(t *testing.T) {
	plan, attempt := uuid.NewString(), uuid.NewString()
	fake := &savedPlanEffectFake{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/saved-plan-effect-grant", SavedPlanEffectApplyHandler(fake))
	router.Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/saved-plan-effect-recovery", SavedPlanEffectRecoveryHandler(fake))
	call := func(method, path string, p AccessPrincipal, body string) *httptest.ResponseRecorder {
		req := WithAccessPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)), &p)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	good := AccessPrincipal{Subject: "fleet-attempt:" + attempt, TokenID: "attempt-token", Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	path := "/api/v1/fleet/plans/" + plan + "/attempts/" + attempt
	wrong := good
	wrong.Subject = "fleet-attempt:" + uuid.NewString()
	if got := call(http.MethodPost, path+"/saved-plan-effect-grant", wrong, "{}"); got.Code != http.StatusForbidden || fake.consumeCalls != 0 {
		t.Fatalf("wrong attempt=%d calls=%d", got.Code, fake.consumeCalls)
	}
	if got := call(http.MethodPost, path+"/saved-plan-effect-grant", good, "{}"); got.Code != http.StatusOK || fake.consumeCalls != 1 || !strings.Contains(got.Body.String(), `"authorizesApply":true`) {
		t.Fatalf("grant=%d %s", got.Code, got.Body.String())
	}
	if got := call(http.MethodGet, path+"/saved-plan-effect-recovery", good, ""); got.Code != http.StatusOK || fake.recoveryCalls != 1 || !strings.Contains(got.Body.String(), `"authorizesApply":false`) {
		t.Fatalf("recovery=%d %s", got.Code, got.Body.String())
	}
}

func TestSavedPlanRefreshOnlyCannotCarryApply(t *testing.T) {
	plan, attempt := uuid.NewString(), uuid.NewString()
	fake := &savedPlanEffectFake{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/saved-plan-refresh-only", SavedPlanRefreshOnlyHandler(fake))
	p := AccessPrincipal{Subject: savedPlanEffectVerifierSubject, TokenID: "verifier", Scopes: []string{ScopeFleetSavedPlanEffectVerify}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	body, _ := json.Marshal(map[string]string{"providerAbsenceSha256": strings.Repeat("a", 64)})
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan+"/attempts/"+attempt+"/saved-plan-refresh-only", strings.NewReader(string(body))), &p)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"authorizesApply":false`) {
		t.Fatalf("refresh=%d %s", out.Code, out.Body.String())
	}
}
