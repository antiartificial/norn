package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type verifierReadStore struct {
	binding offline.Binding
	attempt offline.Attempt
	grant   offline.EffectGrantV2
	epoch   int64
	fence   lifecycle.FenceFacts
	reads   int
}

func (s *verifierReadStore) GetOfflineDispatchReservation(context.Context, string) (offline.Binding, error) {
	s.reads++
	if s.binding.PlanID == "" {
		return offline.Binding{}, etcdstore.ErrNotFound
	}
	return s.binding, nil
}
func (s *verifierReadStore) GetOfflineAttempt(context.Context, string) (offline.Attempt, error) {
	s.reads++
	if s.attempt.ID == "" {
		return offline.Attempt{}, etcdstore.ErrNotFound
	}
	return s.attempt, nil
}
func (s *verifierReadStore) GetOfflineEffectGrantV2(context.Context, string) (offline.EffectGrantV2, error) {
	s.reads++
	if s.grant.ID == "" {
		return offline.EffectGrantV2{}, etcdstore.ErrNotFound
	}
	return s.grant, nil
}
func (s *verifierReadStore) FleetAuthorityEpoch(context.Context) (int64, error) {
	s.reads++
	return s.epoch, nil
}
func (s *verifierReadStore) GetFleetTargetFence(context.Context, string) (lifecycle.FenceFacts, int64, error) {
	s.reads++
	return s.fence, 1, nil
}

func verifierReadFixture(t *testing.T) *verifierReadStore {
	t.Helper()
	b, _, _ := signedReservation(t, time.Now().UTC())
	b.SchemaVersion = offline.BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("a", 64)
	b.ProviderNodes = []offline.ProviderNode{
		{"app-01", "610327211"}, {"app-02", "610327213"}, {"app-03", offline.WorkDropletID},
		{"control-01", "610327217"}, {"control-02", "610327219"}, {"control-03", "610327221"},
		{"ingress-01", "610327223"}, {"ingress-02", "610327225"},
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	boundJSON, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	boundDigest := sha256.Sum256(boundJSON)
	attempt := offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: uuid.NewString(), PlanID: b.PlanID,
		BindingSHA256: hex.EncodeToString(boundDigest[:]), PrincipalSubject: b.RunnerPrincipalSubject,
		PrincipalTokenID: "secret-runner-token-id", PrincipalDeviceID: "secret-runner-device-id",
		TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, Status: "queued", CurrentPhase: "prechange_verified"}
	return &verifierReadStore{binding: b, attempt: attempt, epoch: 3, fence: lifecycle.FenceFacts{
		TargetID: b.TargetID, Generation: int64(b.FenceGeneration), Held: true, HolderPlanID: b.PlanID,
		HolderNonceSHA256: b.DispatchNonceSHA256, AuthorityEpoch: 3}}
}

func verifierReadRequest(s *verifierReadStore, p handler.AccessPrincipal, path string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.With(offlineEffectNoStore).Get("/api/v1/fleet/offline/verifier/plans/{planID}/attempts/{attemptID}", etcdOfflineVerifierRead(s))
	request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, nil), &p)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}

func verifierPrincipal() handler.AccessPrincipal {
	return handler.AccessPrincipal{Subject: offlineVerifierSubject, TokenID: "verifier-token", Source: handler.AccessPrincipalSourceManagedToken,
		Scopes: []string{handler.ScopeFleetOfflineVerify}, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestOfflineVerifierReadIsExactNonauthorizingAndSecretFree(t *testing.T) {
	s := verifierReadFixture(t)
	path := "/api/v1/fleet/offline/verifier/plans/" + s.binding.PlanID + "/attempts/" + s.attempt.ID + "?audience=work-staging&authorityEpoch=3"
	response := verifierReadRequest(s, verifierPrincipal(), path)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("read=%d headers=%v body=%s", response.Code, response.Header(), response.Body)
	}
	var result struct {
		BindingSHA256    string                 `json:"bindingSHA256"`
		ObservedAt       time.Time              `json:"observedAt"`
		Binding          offlineVerifierBinding `json:"binding"`
		Attempt          offlineVerifierAttempt `json:"attempt"`
		AuthorizesEffect bool                   `json:"authorizesEffect"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AuthorizesEffect || result.BindingSHA256 != s.attempt.BindingSHA256 || result.ObservedAt.IsZero() ||
		result.Binding.DropletID != offline.WorkDropletID || len(result.Binding.ProviderNodes) != 8 ||
		result.Binding.Backend.VersionID != s.binding.Backend.VersionID || result.Attempt.BindingSHA256 != s.attempt.BindingSHA256 {
		t.Fatalf("incomplete or authorizing verifier view: %+v", result)
	}
	for _, secret := range []string{s.binding.RunnerPrincipalSubject, s.binding.DispatchNonceSHA256,
		s.binding.OwnerAuthorizationID, s.attempt.PrincipalTokenID, s.attempt.PrincipalDeviceID} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("verifier view exposed credential/owner field %q", secret)
		}
	}
}

func TestOfflineVerifierReadIncludesOnlyBoundGrantFields(t *testing.T) {
	s := verifierReadFixture(t)
	now := time.Now().UTC()
	s.grant = offline.EffectGrantV2{SchemaVersion: offline.EffectGrantSchemaV2, ID: uuid.NewString(), PlanID: s.binding.PlanID,
		AttemptID: s.attempt.ID, BindingSHA256: s.attempt.BindingSHA256, TargetID: s.binding.TargetID,
		TargetFenceGeneration: s.binding.FenceGeneration, AuthorityEpoch: s.epoch,
		OwnerAuthorizationID: s.binding.OwnerAuthorizationID, SourceCommit: s.binding.SourceCommit,
		SavedPlanSHA256: s.binding.SavedPlanSHA256, EffectSHA256: s.binding.EffectSHA256,
		Backend: s.binding.Backend, ProviderAccountUUID: s.binding.ProviderAccountUUID,
		NodeName: s.binding.NodeName, DropletID: s.binding.DropletID,
		RunnerSubject: s.binding.RunnerPrincipalSubject, RunnerTokenID: "secret-token", RunnerDeviceID: "secret-device",
		WriterFence:         offline.WriterFence{BackendID: "work-staging-postgres", Generation: 7, Holder: s.attempt.ID, SessionSHA256: strings.Repeat("7", 64)},
		EffectEvidence:      offline.EffectEvidenceV2{SHA256: strings.Repeat("8", 64), ObservedAt: now, ValidUntil: now.Add(20 * time.Second)},
		DeleteRequestSHA256: offline.ExactDeleteRequestSHA256V2(), Phase: "issued", IssuedAt: now, ExpiresAt: now.Add(20 * time.Second)}
	if err := s.grant.Validate(); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/fleet/offline/verifier/plans/" + s.binding.PlanID + "/attempts/" + s.attempt.ID + "?audience=work-staging&authorityEpoch=3"
	response := verifierReadRequest(s, verifierPrincipal(), path)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"phase":"issued"`) ||
		!strings.Contains(response.Body.String(), `"generation":7`) || strings.Contains(response.Body.String(), "secret-token") ||
		strings.Contains(response.Body.String(), "secret-device") || strings.Contains(response.Body.String(), s.binding.RunnerPrincipalSubject) {
		t.Fatalf("bound grant view=%d %s", response.Code, response.Body)
	}
	s.grant.AuthorityEpoch = 2
	if stale := verifierReadRequest(s, verifierPrincipal(), path); stale.Code != http.StatusConflict {
		t.Fatalf("stale grant=%d %s", stale.Code, stale.Body)
	}
}

func TestOfflineVerifierReadRejectsRunnerAdminAndStaleOrMissingState(t *testing.T) {
	s := verifierReadFixture(t)
	base := "/api/v1/fleet/offline/verifier/plans/" + s.binding.PlanID + "/attempts/" + s.attempt.ID
	for name, p := range map[string]handler.AccessPrincipal{
		"runner":        offlinePrincipal(),
		"admin":         {Subject: "admin", TokenID: "admin", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		"mixed":         {Subject: offlineVerifierSubject, TokenID: "mixed", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetOfflineVerify, handler.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		"wrong subject": {Subject: "other", TokenID: "verify", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetOfflineVerify}, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			before := s.reads
			if got := verifierReadRequest(s, p, base+"?audience=work-staging&authorityEpoch=3"); got.Code != http.StatusForbidden || s.reads != before {
				t.Fatalf("unauthorized verifier read=%d reads=%d body=%s", got.Code, s.reads-before, got.Body)
			}
		})
	}
	for _, query := range []string{"", "?audience=other&authorityEpoch=3", "?audience=work-staging&authorityEpoch=2", "?audience=work-staging&authorityEpoch=3&extra=1"} {
		got := verifierReadRequest(s, verifierPrincipal(), base+query)
		if got.Code == http.StatusOK {
			t.Fatalf("accepted stale/invalid request %q", query)
		}
	}
	missing := *s
	missing.attempt = offline.Attempt{}
	if got := verifierReadRequest(&missing, verifierPrincipal(), base+"?audience=work-staging&authorityEpoch=3"); got.Code != http.StatusNotFound {
		t.Fatalf("missing attempt=%d %s", got.Code, got.Body)
	}
	stale := *s
	stale.fence.AuthorityEpoch = 2
	if got := verifierReadRequest(&stale, verifierPrincipal(), base+"?audience=work-staging&authorityEpoch=3"); got.Code != http.StatusConflict {
		t.Fatalf("stale fence=%d %s", got.Code, got.Body)
	}
}

func TestOfflineVerifierTokenScopeRequiresExactSubjectAndSoleScope(t *testing.T) {
	now := time.Now().UTC()
	if _, err := handler.NewManagedAccessTokenRecord(offlineVerifierSubject, []string{handler.ScopeFleetOfflineVerify}, time.Hour, now); err != nil {
		t.Fatalf("dedicated verifier token rejected: %v", err)
	}
	for _, tc := range []struct {
		subject string
		scopes  []string
	}{
		{"other", []string{handler.ScopeFleetOfflineVerify}},
		{offlineVerifierSubject, []string{handler.ScopeFleetOfflineVerify, handler.ScopeAPIRead}},
	} {
		if _, err := handler.NewManagedAccessTokenRecord(tc.subject, tc.scopes, time.Hour, now); err == nil {
			t.Fatalf("accepted shared verifier identity: %+v", tc)
		}
	}
}

func TestOfflineVerifierPrincipalCannotUseRunnerEffectRoute(t *testing.T) {
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &offlineEffectV2HTTPStore{}
	path := "/api/v1/fleet/offline/attempts/" + planID + "/" + attemptID + "/effect-grants/" + grantID + "/dispatch"
	response := grantHTTPRequest(offlineEffectV2Router(store, &offlineEffectV2HTTPSource{}), verifierPrincipal(), http.MethodPost, path, "{}")
	if response.Code != http.StatusForbidden || store.dispatchCalls != 0 {
		t.Fatalf("verifier crossed effect boundary: status=%d dispatchCalls=%d body=%s", response.Code, store.dispatchCalls, response.Body)
	}
}

func TestOfflineVerifierRouteIsAbsentUntilExplicitlyEnabled(t *testing.T) {
	s := verifierReadFixture(t)
	router := chi.NewRouter()
	registerEtcdFleetOfflineVerifierRoute(router, &config.Config{}, nil, s)
	path := "/api/v1/fleet/offline/verifier/plans/" + s.binding.PlanID + "/attempts/" + s.attempt.ID + "?audience=work-staging&authorityEpoch=3"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusNotFound || s.reads != 0 {
		t.Fatalf("disabled verifier route=%d reads=%d", response.Code, s.reads)
	}
}
