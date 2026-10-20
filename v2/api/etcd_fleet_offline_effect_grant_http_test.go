package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineGrantHTTPStore struct {
	grant                   offline.EffectGrant
	issue, consume, recover int
}

func (s *offlineGrantHTTPStore) IssueOfflineEffectGrant(_ context.Context, planID, attemptID string,
	p etcdstore.OfflineRunnerProof, now time.Time) (offline.EffectGrant, error) {
	s.issue++
	s.grant = offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: uuid.NewString(),
		PlanID: planID, AttemptID: attemptID, RunnerSubject: p.Subject, RunnerTokenID: p.TokenID,
		RunnerDeviceID: p.DeviceID, Phase: "issued", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	return s.grant, nil
}

func (s *offlineGrantHTTPStore) ConsumeOfflineEffectGrant(_ context.Context, planID, attemptID, grantID string,
	_ etcdstore.OfflineRunnerProof, now time.Time) (offline.EffectGrant, error) {
	s.consume++
	if s.grant.PlanID != planID || s.grant.AttemptID != attemptID || s.grant.ID != grantID {
		return offline.EffectGrant{}, offline.ErrEffectGrantMismatch
	}
	s.grant.Phase, s.grant.ConsumedAt = "consumed", &now
	return s.grant, nil
}

func (s *offlineGrantHTTPStore) RecoverOfflineEffectGrant(_ context.Context, planID, attemptID string,
	_ etcdstore.OfflineRunnerProof, _ time.Time) (offline.EffectGrant, error) {
	s.recover++
	if s.grant.PlanID != planID || s.grant.AttemptID != attemptID {
		return offline.EffectGrant{}, etcdstore.ErrNotFound
	}
	return s.grant, nil
}

func offlineGrantHTTPRouter(store *offlineGrantHTTPStore, devices offlineEffectIdentityStore) http.Handler {
	r := chi.NewRouter()
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants"
	r.With(offlineEffectNoStore).Post(base, etcdOfflineEffectGrantIssue(store, devices))
	r.With(offlineEffectNoStore).Get(base, etcdOfflineEffectGrantRecover(store, devices, false))
	r.With(offlineEffectNoStore).Get(base+"/{grantID}", etcdOfflineEffectGrantRecover(store, devices, true))
	r.With(offlineEffectNoStore).Post(base+"/{grantID}/consume", etcdOfflineEffectGrantConsume(store, devices))
	return r
}

func grantHTTPRequest(router http.Handler, principal handler.AccessPrincipal, method, path, body string) *httptest.ResponseRecorder {
	req := handler.WithAccessPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)), &principal)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestOfflineEffectGrantHTTPStrictLifecycleAndNoStore(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	base := "/api/v1/fleet/offline/attempts/" + planID + "/" + attemptID + "/effect-grants"
	store := &offlineGrantHTTPStore{}
	devices := offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}
	router := offlineGrantHTTPRouter(store, devices)
	p := offlinePrincipal()

	for _, body := range []string{"", "null", `{"claim":true}`, "{} {}"} {
		response := grantHTTPRequest(router, p, http.MethodPost, base, body)
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "private, no-store" ||
			response.Header().Get("Pragma") != "no-cache" || store.issue != 0 {
			t.Fatalf("malformed body accepted: status=%d cache=%q issue=%d body=%q", response.Code, response.Header().Get("Cache-Control"), store.issue, body)
		}
	}
	issued := grantHTTPRequest(router, p, http.MethodPost, base, "{}")
	if issued.Code != http.StatusOK || store.issue != 1 ||
		!strings.Contains(issued.Body.String(), `"authorizesEffect":false`) {
		t.Fatalf("issue=%d calls=%d body=%s", issued.Code, store.issue, issued.Body)
	}
	read := grantHTTPRequest(router, p, http.MethodGet, base, "")
	if read.Code != http.StatusOK || store.recover != 1 || !strings.Contains(read.Body.String(), store.grant.ID) {
		t.Fatalf("recovery=%d calls=%d body=%s", read.Code, store.recover, read.Body)
	}
	wrong := grantHTTPRequest(router, p, http.MethodGet, base+"/"+uuid.NewString(), "")
	if wrong.Code != http.StatusNotFound || wrong.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("wrong grant recovery=%d cache=%q", wrong.Code, wrong.Header().Get("Cache-Control"))
	}
	consumed := grantHTTPRequest(router, p, http.MethodPost, base+"/"+store.grant.ID+"/consume", "{}")
	if consumed.Code != http.StatusOK || store.consume != 1 ||
		!strings.Contains(consumed.Body.String(), `"phase":"consumed"`) ||
		!strings.Contains(consumed.Body.String(), `"authorizesEffect":false`) {
		t.Fatalf("consume=%d calls=%d body=%s", consumed.Code, store.consume, consumed.Body)
	}
}

func TestOfflineEffectGrantHTTPRejectsOtherCredentialBeforeStore(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	base := "/api/v1/fleet/offline/attempts/" + planID + "/" + attemptID + "/effect-grants"
	store := &offlineGrantHTTPStore{}
	devices := offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}
	router := offlineGrantHTTPRouter(store, devices)
	p := offlinePrincipal()
	p.TokenID = "replacement-token"
	response := grantHTTPRequest(router, p, http.MethodPost, base, "{}")
	if response.Code != http.StatusPreconditionFailed || store.issue != 0 ||
		response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("replacement credential reached store: status=%d issue=%d", response.Code, store.issue)
	}
}

func TestOfflineEffectGrantHTTPNoStoreOnPrincipalRefusal(t *testing.T) {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	base := "/api/v1/fleet/offline/attempts/" + planID + "/" + attemptID + "/effect-grants"
	router := offlineGrantHTTPRouter(&offlineGrantHTTPStore{}, offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform})
	request := httptest.NewRequest(http.MethodGet, base, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("Cache-Control") != "private, no-store" ||
		response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("principal refusal cache policy: status=%d cache=%q pragma=%q", response.Code,
			response.Header().Get("Cache-Control"), response.Header().Get("Pragma"))
	}
}
