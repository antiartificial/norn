package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineEvidenceReadFixture struct {
	attempt string
	err     error
	calls   []string
}

func (f *offlineEvidenceReadFixture) GetOfflineDeleteCommitment(context.Context, string) (offline.DeleteCommitment, []byte, error) {
	f.calls = append(f.calls, "commitment")
	value := offline.DeleteCommitment{AttemptID: f.attempt}
	raw, _ := json.Marshal(value)
	return value, raw, f.err
}

func (f *offlineEvidenceReadFixture) GetOfflineDeleteDispatchReservation(context.Context, string) ([]byte, error) {
	f.calls = append(f.calls, "reservation")
	raw, _ := json.Marshal(map[string]string{"attemptId": f.attempt})
	return raw, f.err
}

func (f *offlineEvidenceReadFixture) GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error) {
	f.calls = append(f.calls, "terminal")
	return offline.TerminalEvidence{AttemptID: f.attempt}, f.err
}

func TestOfflineEvidenceReadsRequireVerifierAndExactAttempt(t *testing.T) {
	const planID = "11111111-1111-4111-8111-111111111111"
	const attemptID = "22222222-2222-4222-8222-222222222222"
	fixture := &offlineEvidenceReadFixture{attempt: attemptID}
	identities := offlineTestIdentity{device: &store.AccessDevice{ID: "verifier-device"}}
	trust := &offlineFleetTrust{runnerSubject: "offline-runner", verifierSubject: "offline-verifier"}
	verifier := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "offline-verifier", TokenID: "verifier-token", DeviceID: "verifier-device", Scopes: []string{handler.ScopeFleetVerify}}
	var injected *handler.AccessPrincipal
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if injected != nil {
				r = handler.WithAccessPrincipal(r, injected)
			}
			next.ServeHTTP(w, r)
		})
	}
	router := chi.NewRouter()
	registerEtcdFleetOfflineEvidenceReadRoutesWithAuth(router, auth, identities, fixture, trust)
	paths := []string{"delete-commitment", "delete-dispatch-reservation", "terminal-evidence"}
	for _, suffix := range paths {
		path := "/api/v1/fleet/offline/attempts/" + planID + "/" + attemptID + "/" + suffix
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s unauthenticated = %d", suffix, response.Code)
		}
		injected = &verifier
		response = httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(response.Body.String(), `"authorizesEffect":false`) {
			t.Fatalf("%s verifier response = %d %s", suffix, response.Code, response.Body.String())
		}
		injected = nil
	}
	if len(fixture.calls) != 3 {
		t.Fatalf("read calls = %v", fixture.calls)
	}

	injected = &verifier
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/offline/attempts/"+planID+"/33333333-3333-4333-8333-333333333333/delete-commitment", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("wrong attempt = %d", response.Code)
	}

	runner := verifier
	runner.Subject, runner.Scopes = "offline-runner", []string{handler.ScopeFleetOperate}
	injected = &runner
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/offline/attempts/"+planID+"/"+attemptID+"/terminal-evidence", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("runner terminal read = %d", response.Code)
	}
}

func TestOfflineEvidenceReadRoutesHaveNoWriteMethod(t *testing.T) {
	router := chi.NewRouter()
	fixture := &offlineEvidenceReadFixture{attempt: "22222222-2222-4222-8222-222222222222"}
	registerEtcdFleetOfflineEvidenceReadRoutesWithAuth(router, func(next http.Handler) http.Handler { return next }, offlineTestIdentity{}, fixture, &offlineFleetTrust{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/attempts/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/terminal-evidence", strings.NewReader(`{"claim":true}`)))
	if response.Code != http.StatusMethodNotAllowed || len(fixture.calls) != 0 {
		t.Fatalf("write route exposed: %d calls=%v", response.Code, fixture.calls)
	}
}
