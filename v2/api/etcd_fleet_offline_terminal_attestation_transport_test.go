package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

const (
	terminalPlanID      = "11111111-1111-4111-8111-111111111111"
	terminalAttemptID   = "22222222-2222-4222-8222-222222222222"
	terminalGrantID     = "33333333-3333-4333-8333-333333333333"
	terminalChallengeID = "44444444-4444-4444-8444-444444444444"
)

type terminalTransportStoreFixture struct {
	grant             offline.EffectGrant
	challenge         offline.TerminalChallengeV1
	evidence          offline.TerminalEvidence
	challengeErr      error
	issued, submitted int
	submittedRaw      []byte
}

func (f *terminalTransportStoreFixture) GetOfflineEffectGrant(context.Context, string) (offline.EffectGrant, error) {
	return f.grant, nil
}
func (f *terminalTransportStoreFixture) GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error) {
	return f.evidence, nil
}
func (f *terminalTransportStoreFixture) GetOfflineTerminalChallenge(context.Context, string) (offline.TerminalChallengeV1, error) {
	return f.challenge, f.challengeErr
}
func (f *terminalTransportStoreFixture) IssueOfflineTerminalChallenge(_ context.Context, plan, attempt, grant, verifier, observer string) (offline.TerminalChallengeV1, error) {
	f.issued++
	if plan != terminalPlanID || attempt != terminalAttemptID || grant != terminalGrantID || verifier != "verifier" || observer != "observer" {
		panic("caller-controlled challenge input")
	}
	return f.challenge, nil
}
func (f *terminalTransportStoreFixture) SubmitOfflineTerminalAttestation(_ context.Context, plan, attempt, challenge string, raw []byte) (offline.TerminalEvidence, error) {
	f.submitted++
	if plan != terminalPlanID || attempt != terminalAttemptID || challenge != terminalChallengeID {
		panic("mismatched submission IDs")
	}
	f.submittedRaw = append([]byte(nil), raw...)
	return f.evidence, nil
}

type terminalTransportIdentity struct{ offlineTestIdentity }

func (terminalTransportIdentity) ActiveAccessDevice(_ context.Context, deviceID string) (*store.AccessDevice, error) {
	if deviceID == "verifier-device" || deviceID == "observer-device" {
		return &store.AccessDevice{ID: deviceID}, nil
	}
	return nil, etcdstore.ErrNotFound
}

func terminalTransportTestRouter(t *testing.T, fixture *terminalTransportStoreFixture) (http.Handler, **handler.AccessPrincipal) {
	return terminalTransportTestRouterWithTrust(t, fixture, offlineTerminalTransportTrust{runnerSubject: "runner", verifierSubject: "verifier", observerSubject: "observer",
		verifierDeviceID: "verifier-device", observerDeviceID: "observer-device"})
}

func terminalTransportTestRouterWithTrust(t *testing.T, fixture *terminalTransportStoreFixture, trust offlineTerminalTransportTrust) (http.Handler, **handler.AccessPrincipal) {
	t.Helper()
	var principal *handler.AccessPrincipal
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal != nil {
				r = handler.WithAccessPrincipal(r, principal)
			}
			next.ServeHTTP(w, r)
		})
	}
	router := chi.NewRouter()
	registerEtcdFleetOfflineTerminalAttestationRoutesWithAuth(router, auth, auth, terminalTransportIdentity{}, fixture, trust)
	return router, &principal
}

func TestOfflineTerminalTransportPinsFleetPlanSourceAndTarget(t *testing.T) {
	source := strings.Repeat("a", 40)
	trust := offlineTerminalTransportTrust{runnerSubject: "runner", verifierSubject: "verifier", observerSubject: "observer",
		verifierDeviceID: "verifier-device", observerDeviceID: "observer-device", targetID: "target-1", sourceCommit: source}
	fixture := &terminalTransportStoreFixture{grant: offline.EffectGrant{PlanID: terminalPlanID, AttemptID: terminalAttemptID,
		ID: terminalGrantID, Phase: "consumed", TargetID: "target-1", SourceCommit: source},
		challenge: offline.TerminalChallengeV1{PlanID: terminalPlanID, AttemptID: terminalAttemptID,
			GrantID: terminalGrantID, ID: terminalChallengeID, TargetID: "target-1", SourceCommit: source,
			VerifierSubject: "verifier", ObserverSubject: "observer"}}
	router, principal := terminalTransportTestRouterWithTrust(t, fixture, trust)
	observer := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "observer", TokenID: "o-token", DeviceID: "observer-device", Scopes: []string{handler.ScopeFleetObserve}}
	verifier := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "verifier", TokenID: "v-token", DeviceID: "verifier-device", Scopes: []string{handler.ScopeFleetVerify}}
	base := "/api/v1/fleet/offline/attempts/" + terminalPlanID + "/" + terminalAttemptID
	*principal = &observer
	if got := terminalRequest(router, http.MethodGet, base+"/terminal-observer/grant", "").Code; got != http.StatusOK {
		t.Fatalf("valid grant: %d", got)
	}
	fixture.grant.SourceCommit = strings.Repeat("b", 40)
	if got := terminalRequest(router, http.MethodGet, base+"/terminal-observer/grant", "").Code; got != http.StatusConflict {
		t.Fatalf("wrong source exposed: %d", got)
	}
	fixture.grant.SourceCommit = source
	fixture.grant.TargetID = "target-2"
	*principal = &verifier
	if got := terminalRequest(router, http.MethodPost, base+"/terminal-challenges", `{}`).Code; got != http.StatusConflict || fixture.issued != 0 {
		t.Fatalf("wrong target issued challenge: %d", got)
	}
	fixture.grant.TargetID = "target-1"
	fixture.challenge.SourceCommit = strings.Repeat("b", 40)
	if got := terminalRequest(router, http.MethodPost, base+"/terminal-challenges", `{}`).Code; got != http.StatusConflict || fixture.issued != 1 {
		t.Fatalf("wrong source challenge accepted: %d", got)
	}
}

func terminalRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	return response
}

func TestOfflineTerminalChallengeTransportBindsVerifierAndRecoversLostResponse(t *testing.T) {
	fixture := &terminalTransportStoreFixture{
		grant:     offline.EffectGrant{PlanID: terminalPlanID, AttemptID: terminalAttemptID, ID: terminalGrantID, Phase: "consumed"},
		challenge: offline.TerminalChallengeV1{PlanID: terminalPlanID, AttemptID: terminalAttemptID, GrantID: terminalGrantID, ID: terminalChallengeID, VerifierSubject: "verifier", ObserverSubject: "observer"},
	}
	router, principal := terminalTransportTestRouter(t, fixture)
	base := "/api/v1/fleet/offline/attempts/" + terminalPlanID + "/" + terminalAttemptID + "/terminal-challenges"
	verifier := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "verifier", TokenID: "v-token", DeviceID: "verifier-device", Scopes: []string{handler.ScopeFleetVerify}}
	observer := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "observer", TokenID: "o-token", DeviceID: "observer-device", Scopes: []string{handler.ScopeFleetObserve}}
	*principal = &observer
	if got := terminalRequest(router, http.MethodPost, base, `{}`).Code; got != http.StatusForbidden {
		t.Fatalf("observer issued challenge: %d", got)
	}
	*principal = &verifier
	for _, body := range []string{`{"grantId":"` + terminalGrantID + `"}`, `{ "claim":true }`, strings.Repeat(" ", 65)} {
		if got := terminalRequest(router, http.MethodPost, base, body).Code; got != http.StatusBadRequest {
			t.Fatalf("accepted caller fields/body size: %d", got)
		}
	}
	if fixture.issued != 0 {
		t.Fatal("invalid request reached challenge CAS")
	}
	first := terminalRequest(router, http.MethodPost, base, `{}`)
	if first.Code != http.StatusAccepted || first.Header().Get("Cache-Control") != "private, no-store" || fixture.issued != 1 {
		t.Fatalf("issue: %d %s", first.Code, first.Body.String())
	}
	readback := terminalRequest(router, http.MethodGet, base, "")
	if readback.Code != http.StatusOK || !strings.Contains(readback.Body.String(), terminalChallengeID) {
		t.Fatalf("lost response readback: %d %s", readback.Code, readback.Body.String())
	}
	wrongAttempt := terminalRequest(router, http.MethodGet, strings.Replace(base, terminalAttemptID, terminalGrantID, 1), "")
	if wrongAttempt.Code != http.StatusConflict {
		t.Fatalf("wrong attempt readback: %d", wrongAttempt.Code)
	}
	fixture.challengeErr = etcdstore.ErrNotFound
	missing := terminalRequest(router, http.MethodGet, base, "")
	var problem struct {
		Type   string `json:"type"`
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if json.Unmarshal(missing.Body.Bytes(), &problem) != nil || missing.Code != http.StatusNotFound || missing.Header().Get("Content-Type") != "application/problem+json" || missing.Header().Get("Cache-Control") != "private, no-store" || problem.Code != "offline_terminal_not_found" || problem.Status != 404 || problem.Type != "https://norn.dev/problems/offline_terminal_not_found" {
		t.Fatalf("noncanonical missing response: %d %s", missing.Code, missing.Body.String())
	}
}

func TestOfflineTerminalObserverRoutesRequireDistinctDeviceScopeAndExactRecords(t *testing.T) {
	fixture := &terminalTransportStoreFixture{
		grant:     offline.EffectGrant{PlanID: terminalPlanID, AttemptID: terminalAttemptID, ID: terminalGrantID, Phase: "consumed"},
		evidence:  offline.TerminalEvidence{PlanID: terminalPlanID, AttemptID: terminalAttemptID, GrantID: terminalGrantID},
		challenge: offline.TerminalChallengeV1{PlanID: terminalPlanID, AttemptID: terminalAttemptID, GrantID: terminalGrantID, ID: terminalChallengeID, VerifierSubject: "verifier", ObserverSubject: "observer"},
	}
	router, principal := terminalTransportTestRouter(t, fixture)
	base := "/api/v1/fleet/offline/attempts/" + terminalPlanID + "/" + terminalAttemptID + "/terminal-observer/"
	observer := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "observer", TokenID: "o-token", DeviceID: "observer-device", Scopes: []string{handler.ScopeFleetObserve}}
	for _, mutation := range []func(*handler.AccessPrincipal){
		func(p *handler.AccessPrincipal) { p.Subject = "verifier" },
		func(p *handler.AccessPrincipal) { p.Scopes = []string{handler.ScopeFleetVerify} },
		func(p *handler.AccessPrincipal) { p.Scopes = []string{handler.ScopeFleetObserve, handler.ScopeAPIRead} },
		func(p *handler.AccessPrincipal) { p.DeviceID = "inactive" },
		func(p *handler.AccessPrincipal) { p.DeviceID = "verifier-device" },
		func(p *handler.AccessPrincipal) { p.CI = &handler.CIIdentity{} },
	} {
		candidate := observer
		mutation(&candidate)
		*principal = &candidate
		if got := terminalRequest(router, http.MethodGet, base+"grant", "").Code; got != http.StatusForbidden {
			t.Fatalf("wrong observer identity accepted: %d", got)
		}
	}
	*principal = &observer
	for _, suffix := range []string{"grant", "evidence", "challenge"} {
		response := terminalRequest(router, http.MethodGet, base+suffix, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"authorizesEffect":false`) {
			t.Fatalf("%s: %d %s", suffix, response.Code, response.Body.String())
		}
	}
	fixture.grant.Phase = "issued"
	if got := terminalRequest(router, http.MethodGet, base+"grant", "").Code; got != http.StatusConflict {
		t.Fatalf("unconsumed grant exposed: %d", got)
	}
	fixture.grant.Phase = "consumed"
	fixture.evidence.AttemptID = terminalGrantID
	if got := terminalRequest(router, http.MethodGet, base+"evidence", "").Code; got != http.StatusConflict {
		t.Fatalf("wrong terminal exposed: %d", got)
	}
}

func TestOfflineTerminalTransportRejectsSharedVerifierObserverDevice(t *testing.T) {
	trust := offlineTerminalTransportTrust{runnerSubject: "runner", verifierSubject: "verifier", observerSubject: "observer",
		verifierDeviceID: "shared-device", observerDeviceID: "shared-device"}
	if trust.valid() {
		t.Fatal("verifier and observer may not share one device")
	}
}

func TestOfflineTerminalAttestationTransportRejectsNoncanonicalAndMismatchedEnvelope(t *testing.T) {
	fixture := &terminalTransportStoreFixture{evidence: offline.TerminalEvidence{PlanID: terminalPlanID, AttemptID: terminalAttemptID, GrantID: terminalGrantID}}
	router, principal := terminalTransportTestRouter(t, fixture)
	observer := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "observer", TokenID: "o-token", DeviceID: "observer-device", Scopes: []string{handler.ScopeFleetObserve}}
	*principal = &observer
	path := "/api/v1/fleet/offline/attempts/" + terminalPlanID + "/" + terminalAttemptID + "/terminal-challenges/" + terminalChallengeID + "/attestation"
	for _, body := range []string{`{}`, `{"unknown":true}`, strings.Repeat("x", 32<<10+1)} {
		if got := terminalRequest(router, http.MethodPut, path, body).Code; got != http.StatusBadRequest {
			t.Fatalf("invalid envelope accepted: %d", got)
		}
	}
	wrong := offline.SignedTerminalAttestationV1{Attestation: offline.TerminalAttestationV1{PlanID: terminalPlanID, AttemptID: terminalAttemptID, ChallengeID: terminalChallengeID, GrantID: terminalGrantID, ObserverSubject: "verifier"}}
	raw, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if got := terminalRequest(router, http.MethodPut, path, string(raw)).Code; got != http.StatusBadRequest {
		t.Fatalf("wrong observer in envelope accepted: %d", got)
	}
	if fixture.submitted != 0 {
		t.Fatal("invalid envelope reached authority")
	}
	valid := wrong
	valid.Attestation.ObserverSubject = "observer"
	raw, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	response := terminalRequest(router, http.MethodPut, path, string(raw))
	if response.Code != http.StatusOK || fixture.submitted != 1 || string(fixture.submittedRaw) != string(raw) {
		t.Fatalf("valid shape not passed to authority: %d %s", response.Code, response.Body.String())
	}
	wrongPath := strings.Replace(path, terminalChallengeID, terminalGrantID, 1)
	if got := terminalRequest(router, http.MethodPut, wrongPath, string(raw)).Code; got != http.StatusBadRequest {
		t.Fatalf("wrong challenge path accepted: %d", got)
	}
}
