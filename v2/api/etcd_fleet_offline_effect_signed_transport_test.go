package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

const (
	effectTransportPlan      = "11111111-1111-4111-8111-111111111111"
	effectTransportAttempt   = "22222222-2222-4222-8222-222222222222"
	effectTransportChallenge = "33333333-3333-4333-8333-333333333333"
	effectTransportGrant     = "44444444-4444-4444-8444-444444444444"
	effectTransportTarget    = "target-1"
)

var effectTransportSource = strings.Repeat("a", 40)

type signedEffectTransportFixture struct {
	challenge                            offline.EffectChallengeV1
	accepted                             etcdstore.AcceptedOfflineEffectAttestation
	grant                                offline.EffectGrant
	issued, submitted, granted, consumed int
	submittedRaw                         []byte
}

func (f *signedEffectTransportFixture) GetOfflineEffectChallenge(context.Context, string, string) (offline.EffectChallengeV1, error) {
	return f.challenge, nil
}
func (f *signedEffectTransportFixture) IssueOfflineEffectChallenge(_ context.Context, plan, attempt, purpose, observer string) (offline.EffectChallengeV1, error) {
	f.issued++
	if plan != effectTransportPlan || attempt != effectTransportAttempt || purpose != offline.EffectIssuePurpose || observer != "observer" {
		panic("challenge input escaped pin")
	}
	return f.challenge, nil
}
func (f *signedEffectTransportFixture) ReadAcceptedOfflineEffectAttestation(context.Context, string, string, string) (etcdstore.AcceptedOfflineEffectAttestation, error) {
	return f.accepted, nil
}
func (f *signedEffectTransportFixture) SubmitOfflineEffectAttestation(_ context.Context, plan, attempt, purpose, challenge string, raw []byte) (etcdstore.AcceptedOfflineEffectAttestation, error) {
	f.submitted++
	if plan != effectTransportPlan || attempt != effectTransportAttempt || purpose != offline.EffectIssuePurpose || challenge != effectTransportChallenge {
		panic("observer input escaped pin")
	}
	f.submittedRaw = append([]byte(nil), raw...)
	return f.accepted, nil
}
func (f *signedEffectTransportFixture) GetOfflineSignedEffectGrant(context.Context, string, string, string) (offline.EffectGrant, error) {
	return f.grant, nil
}
func (f *signedEffectTransportFixture) IssueOfflineEffectGrantFromAccepted(_ context.Context, plan, attempt, challenge string, id etcdstore.OfflineRunnerIdentity) (offline.EffectGrant, error) {
	f.granted++
	if plan != effectTransportPlan || attempt != effectTransportAttempt || challenge != effectTransportChallenge || id.Subject != "runner" || id.TokenID != "runner-token" || id.DeviceID != "runner-device" {
		panic("runner identity escaped pin")
	}
	return f.grant, nil
}
func (f *signedEffectTransportFixture) GetOfflineSignedConsumedGrant(context.Context, string, string, string) (offline.EffectGrant, error) {
	return f.grant, nil
}
func (f *signedEffectTransportFixture) ConsumeOfflineEffectGrantFromAccepted(_ context.Context, plan, attempt, grant, challenge string, id etcdstore.OfflineRunnerIdentity) (offline.EffectGrant, error) {
	f.consumed++
	if plan != effectTransportPlan || attempt != effectTransportAttempt || grant != effectTransportGrant || challenge != effectTransportChallenge || id.Subject != "runner" || id.DeviceID != "runner-device" {
		panic("consume identity escaped pin")
	}
	return f.grant, nil
}

type signedEffectTransportIdentity struct{ offlineTestIdentity }

func (signedEffectTransportIdentity) ActiveAccessDevice(_ context.Context, deviceID string) (*store.AccessDevice, error) {
	if deviceID == "runner-device" || deviceID == "verifier-device" || deviceID == "observer-device" {
		return &store.AccessDevice{ID: deviceID}, nil
	}
	return nil, etcdstore.ErrNotFound
}
func effectTransportFixture() (*signedEffectTransportFixture, offlineTerminalTransportTrust) {
	trust := offlineTerminalTransportTrust{targetID: effectTransportTarget, sourceCommit: effectTransportSource, expiresAt: time.Now().Add(time.Hour),
		runnerSubject: "runner", runnerDeviceID: "runner-device", verifierSubject: "verifier", verifierDeviceID: "verifier-device",
		observerSubject: "observer", observerDeviceID: "observer-device"}
	c := offline.EffectChallengeV1{SchemaVersion: offline.EffectChallengeSchemaV1, Purpose: offline.EffectIssuePurpose,
		ID: effectTransportChallenge, PlanID: effectTransportPlan, AttemptID: effectTransportAttempt, TargetID: effectTransportTarget,
		SourceCommit: effectTransportSource, RunnerSubject: "runner", ObserverSubject: "observer"}
	f := &signedEffectTransportFixture{challenge: c, accepted: etcdstore.AcceptedOfflineEffectAttestation{PlanID: effectTransportPlan,
		AttemptID: effectTransportAttempt, Purpose: offline.EffectIssuePurpose, ChallengeID: effectTransportChallenge,
		AttestationSHA256: strings.Repeat("1", 64), Challenge: c,
		Attempt: offline.Attempt{PrincipalSubject: "runner", PrincipalTokenID: "runner-token", PrincipalDeviceID: "runner-device"}}, grant: offline.EffectGrant{ID: effectTransportGrant,
		PlanID: effectTransportPlan, AttemptID: effectTransportAttempt, TargetID: effectTransportTarget, SourceCommit: effectTransportSource, Phase: "issued"}}
	return f, trust
}
func effectTransportRouter(f *signedEffectTransportFixture, trust offlineTerminalTransportTrust) (http.Handler, **handler.AccessPrincipal) {
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
	registerEtcdFleetOfflineSignedEffectRoutesWithAuth(router, auth, auth, auth, signedEffectTransportIdentity{}, f, trust)
	return router, &principal
}
func effectRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func effectTransportPrincipal(subject, device, token, scope string) *handler.AccessPrincipal {
	return &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: subject, DeviceID: device, TokenID: token, Scopes: []string{scope}}
}

func TestOfflineSignedEffectTransportSeparatesPrincipals(t *testing.T) {
	f, trust := effectTransportFixture()
	router, p := effectTransportRouter(f, trust)
	runner := effectTransportPrincipal("runner", "runner-device", "runner-token", handler.ScopeFleetOperate)
	verifier := effectTransportPrincipal("verifier", "verifier-device", "verifier-token", handler.ScopeFleetVerify)
	observer := effectTransportPrincipal("observer", "observer-device", "observer-token", handler.ScopeFleetObserve)
	base := "/api/v1/fleet/offline/attempts/" + effectTransportPlan + "/" + effectTransportAttempt + "/signed-effect/effect-issue"
	*p = verifier
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusAccepted || f.issued != 1 || r.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("verifier issue: %d issued=%d", r.Code, f.issued)
	}
	*p = observer
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusForbidden || f.issued != 1 {
		t.Fatalf("observer issued challenge: %d", r.Code)
	}
	if r := effectRequest(router, http.MethodGet, base+"/observer/challenge", ""); r.Code != http.StatusOK {
		t.Fatalf("observer challenge read: %d", r.Code)
	}
	signed := offline.SignedEffectAttestationV1{Attestation: offline.EffectAttestationV1{Purpose: offline.EffectIssuePurpose, ChallengeID: effectTransportChallenge,
		ObserverSubject: "observer", Observation: offline.EffectObservation{PlanID: effectTransportPlan, AttemptID: effectTransportAttempt, TargetID: effectTransportTarget, SourceCommit: effectTransportSource}}}
	raw, _ := json.Marshal(signed)
	if r := effectRequest(router, http.MethodPut, base+"/observer/attestations/"+effectTransportChallenge, string(raw)); r.Code != http.StatusOK || f.submitted != 1 {
		t.Fatalf("observer submit: %d submitted=%d %s", r.Code, f.submitted, r.Body.String())
	}
	if r := effectRequest(router, http.MethodGet, base+"/observer/attestations/"+effectTransportChallenge, ""); r.Code != http.StatusOK {
		t.Fatalf("observer accepted read: %d", r.Code)
	}
	*p = runner
	if r := effectRequest(router, http.MethodPut, base+"/observer/attestations/"+effectTransportChallenge, string(raw)); r.Code != http.StatusForbidden || f.submitted != 1 {
		t.Fatalf("runner submitted evidence: %d", r.Code)
	}
	if r := effectRequest(router, http.MethodPost, base+"/runner/grants/"+effectTransportChallenge, `{}`); r.Code != http.StatusAccepted || f.granted != 1 {
		t.Fatalf("runner issue grant: %d granted=%d %s", r.Code, f.granted, r.Body.String())
	}
	if r := effectRequest(router, http.MethodGet, base+"/runner/grants/"+effectTransportChallenge, ""); r.Code != http.StatusOK {
		t.Fatalf("runner grant recovery: %d", r.Code)
	}
	*p = effectTransportPrincipal("runner", "runner-device", "different-token", handler.ScopeFleetOperate)
	if r := effectRequest(router, http.MethodGet, base+"/runner/grants/"+effectTransportChallenge, ""); r.Code != http.StatusForbidden {
		t.Fatalf("different runner token recovered grant: %d", r.Code)
	}
	*p = verifier
	if r := effectRequest(router, http.MethodPost, base+"/runner/grants/"+effectTransportChallenge, `{}`); r.Code != http.StatusForbidden || f.granted != 1 {
		t.Fatalf("verifier issued grant: %d", r.Code)
	}
}

func TestOfflineSignedEffectTransportRejectsMismatchedAndUnpinned(t *testing.T) {
	f, trust := effectTransportFixture()
	router, p := effectTransportRouter(f, trust)
	verifier := effectTransportPrincipal("verifier", "verifier-device", "verifier-token", handler.ScopeFleetVerify)
	*p = verifier
	base := "/api/v1/fleet/offline/attempts/" + effectTransportPlan + "/" + effectTransportAttempt + "/signed-effect/effect-issue"
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{} `); r.Code != http.StatusBadRequest || f.issued != 0 {
		t.Fatalf("noncanonical empty accepted: %d", r.Code)
	}
	f.challenge.SourceCommit = strings.Repeat("b", 40)
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusConflict {
		t.Fatalf("wrong source challenge exposed: %d", r.Code)
	}
	f.challenge.SourceCommit = effectTransportSource
	verifier.DeviceID = "observer-device"
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusForbidden {
		t.Fatalf("wrong device accepted: %d", r.Code)
	}
	verifier.DeviceID = "verifier-device"
	verifier.Scopes = []string{handler.ScopeFleetVerify, handler.ScopeAPIRead}
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusForbidden {
		t.Fatalf("extra scope accepted: %d", r.Code)
	}
	trust.runnerDeviceID = trust.observerDeviceID
	router, _ = effectTransportRouter(f, trust)
	if r := effectRequest(router, http.MethodPost, base+"/challenge", `{}`); r.Code != http.StatusNotFound {
		t.Fatalf("shared device routes registered: %d", r.Code)
	}
}

func TestOfflineSignedEffectTransportDoesNotReissueConsumedGrant(t *testing.T) {
	f, trust := effectTransportFixture()
	f.grant.Phase = "consumed"
	consumedAt := time.Now().UTC()
	f.grant.ConsumedAt = &consumedAt
	router, p := effectTransportRouter(f, trust)
	*p = effectTransportPrincipal("runner", "runner-device", "runner-token", handler.ScopeFleetOperate)
	path := "/api/v1/fleet/offline/attempts/" + effectTransportPlan + "/" + effectTransportAttempt +
		"/signed-effect/effect-issue/runner/grants/" + effectTransportChallenge
	if r := effectRequest(router, http.MethodGet, path, ""); r.Code != http.StatusConflict {
		t.Fatalf("consumed grant recovered as issued: %d", r.Code)
	}
	if r := effectRequest(router, http.MethodPost, path, `{}`); r.Code != http.StatusConflict || f.granted != 1 {
		t.Fatalf("consumed grant returned as issuance: %d grants=%d", r.Code, f.granted)
	}
}

func TestOfflineSignedEffectTransportConsumeAndCanonicalObserverBody(t *testing.T) {
	f, trust := effectTransportFixture()
	router, p := effectTransportRouter(f, trust)
	base := "/api/v1/fleet/offline/attempts/" + effectTransportPlan + "/" + effectTransportAttempt
	observer := effectTransportPrincipal("observer", "observer-device", "observer-token", handler.ScopeFleetObserve)
	*p = observer
	attest := base + "/signed-effect/effect-issue/observer/attestations/" + effectTransportChallenge
	if r := effectRequest(router, http.MethodPut, attest, ` {}`); r.Code != http.StatusBadRequest || f.submitted != 0 {
		t.Fatalf("noncanonical attestation accepted: %d", r.Code)
	}
	f.grant.Phase = "consumed"
	f.accepted.Purpose = offline.EffectConsumePurpose
	f.accepted.Challenge.Purpose = offline.EffectConsumePurpose
	f.grant.ConsumedAt = new(time.Time)
	*f.grant.ConsumedAt = time.Now().UTC()
	*p = effectTransportPrincipal("runner", "runner-device", "runner-token", handler.ScopeFleetOperate)
	path := base + "/signed-effect/effect-consume/runner/grants/" + effectTransportGrant + "/consume/" + effectTransportChallenge
	if r := effectRequest(router, http.MethodPost, path, `{}`); r.Code != http.StatusAccepted || f.consumed != 1 {
		t.Fatalf("runner consume: %d consumed=%d %s", r.Code, f.consumed, r.Body.String())
	}
	if r := effectRequest(router, http.MethodGet, path, ""); r.Code != http.StatusOK {
		t.Fatalf("runner consume recovery: %d", r.Code)
	}
	*p = effectTransportPrincipal("runner", "runner-device", "different-token", handler.ScopeFleetOperate)
	if r := effectRequest(router, http.MethodGet, path, ""); r.Code != http.StatusForbidden {
		t.Fatalf("different runner token recovered consumed grant: %d", r.Code)
	}
	*p = effectTransportPrincipal("runner", "runner-device", "runner-token", handler.ScopeFleetOperate)
	wrong := base + "/signed-effect/effect-consume/runner/grants/11111111-1111-4111-8111-111111111111/consume/" + effectTransportChallenge
	if r := effectRequest(router, http.MethodGet, wrong, ""); r.Code != http.StatusConflict {
		t.Fatalf("wrong grant recovered: %d", r.Code)
	}
}
