package main

import (
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

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineAttemptHTTPStore struct {
	claim     offline.AttemptClaim
	principal etcdstore.OfflineRunnerProof
	count     int
	item      offline.Attempt
}

func (s *offlineAttemptHTTPStore) AdmitOfflineAttemptWithRunnerProof(_ context.Context, c offline.AttemptClaim, p etcdstore.OfflineRunnerProof, id string, _ time.Time) (offline.Attempt, error) {
	s.claim, s.principal, s.count = c, p, s.count+1
	s.item = offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: id, PlanID: c.PlanID, PrincipalSubject: p.Subject,
		PrincipalTokenID: p.TokenID, PrincipalDeviceID: p.DeviceID, Status: "queued", CurrentPhase: "prechange_verified"}
	return s.item, nil
}
func (s *offlineAttemptHTTPStore) GetOfflineAttempt(_ context.Context, _ string) (offline.Attempt, error) {
	return s.item, nil
}

type offlineDeviceHTTPStore struct {
	platform string
	revoked  bool
}

func (s offlineDeviceHTTPStore) ActiveAccessDevice(_ context.Context, id string) (*store.AccessDevice, error) {
	device := &store.AccessDevice{ID: id, Platform: s.platform}
	if s.revoked {
		now := time.Now()
		device.RevokedAt = &now
	}
	return device, nil
}

func (s offlineDeviceHTTPStore) ProveOfflineRunner(_ context.Context, subject string, now time.Time) (etcdstore.OfflineRunnerProof, error) {
	p := offlinePrincipal()
	if s.revoked || s.platform != handler.FleetOfflineRunnerPlatform || subject != p.Subject {
		return etcdstore.OfflineRunnerProof{}, errors.New("runner unavailable")
	}
	return etcdstore.OfflineRunnerProof{Subject: subject, TokenID: p.TokenID, DeviceID: p.DeviceID,
		DeviceRevision: 1, TokenRevision: 1, TokenExpiresAt: now.Add(time.Hour)}, nil
}

func offlineHTTPRouter(attempts *offlineAttemptHTTPStore, devices offlineDeviceHTTPStore) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/v1/fleet/offline/plans/{planID}/attempt", etcdOfflineAttemptCreate(attempts, devices))
	r.Get("/api/v1/fleet/offline/plans/{planID}/attempt", etcdOfflineAttemptGet(attempts, devices))
	return r
}

func offlinePrincipal() handler.AccessPrincipal {
	return handler.AccessPrincipal{Subject: "fleet-offline-device:device-1", TokenID: "token-1", DeviceID: "device-1", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetOfflineAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestOfflineAttemptHTTPAdmissionRequiresDistinctLiveDevice(t *testing.T) {
	planID := uuid.NewString()
	claim := offline.AttemptClaim{PlanID: planID, RunnerPrincipalSubject: "fleet-offline-device:device-1"}
	body, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*handler.AccessPrincipal, *offlineDeviceHTTPStore){
		"github oidc": func(p *handler.AccessPrincipal, _ *offlineDeviceHTTPStore) {
			p.Scopes = []string{handler.ScopeFleetOperate}
			p.CI = &handler.CIIdentity{Provider: "github-actions"}
		},
		"admin": func(p *handler.AccessPrincipal, _ *offlineDeviceHTTPStore) { p.Scopes = []string{handler.ScopeAdmin} },
		"combined": func(p *handler.AccessPrincipal, _ *offlineDeviceHTTPStore) {
			p.Scopes = append(p.Scopes, handler.ScopeAPIRead)
		},
		"wrong platform": func(_ *handler.AccessPrincipal, d *offlineDeviceHTTPStore) {
			d.platform = handler.FleetExecutorPlatform
		},
		"revoked":       func(_ *handler.AccessPrincipal, d *offlineDeviceHTTPStore) { d.revoked = true },
		"wrong subject": func(p *handler.AccessPrincipal, _ *offlineDeviceHTTPStore) { p.Subject = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			p, d, s := offlinePrincipal(), offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}, &offlineAttemptHTTPStore{}
			change(&p, &d)
			req := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+planID+"/attempt", strings.NewReader(string(body))), &p)
			response := httptest.NewRecorder()
			offlineHTTPRouter(s, d).ServeHTTP(response, req)
			if response.Code < 400 || s.count != 0 {
				t.Fatalf("unexpected admission: status=%d count=%d", response.Code, s.count)
			}
		})
	}
	s := &offlineAttemptHTTPStore{}
	p := offlinePrincipal()
	req := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+planID+"/attempt", strings.NewReader(string(body))), &p)
	response := httptest.NewRecorder()
	offlineHTTPRouter(s, offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}).ServeHTTP(response, req)
	if response.Code != http.StatusCreated || s.count != 1 || s.principal.Subject != p.Subject || s.principal.TokenID != p.TokenID || s.claim.PlanID != planID {
		t.Fatalf("admission failed: status=%d count=%d principal=%+v claim=%+v body=%s", response.Code, s.count, s.principal, s.claim, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("attempt response cacheable")
	}
	for _, bad := range []string{`{"planId":"` + planID + `","planId":"` + planID + `"}`, `{"planId":"` + planID + `","unexpected":true}`, `{"planId":"` + planID + `"} {}`} {
		before := s.count
		request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+planID+"/attempt", strings.NewReader(bad)), &p)
		denied := httptest.NewRecorder()
		offlineHTTPRouter(s, offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}).ServeHTTP(denied, request)
		if denied.Code != http.StatusBadRequest || s.count != before {
			t.Fatalf("ambiguous claim accepted: %d %s", denied.Code, bad)
		}
	}

	read := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/offline/plans/"+planID+"/attempt", nil), &p)
	response = httptest.NewRecorder()
	offlineHTTPRouter(s, offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}).ServeHTTP(response, read)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), s.item.ID) {
		t.Fatalf("readback failed: %d %s", response.Code, response.Body)
	}
	other := p
	other.DeviceID, other.Subject = "device-2", "fleet-offline-device:device-2"
	read = handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/offline/plans/"+planID+"/attempt", nil), &other)
	response = httptest.NewRecorder()
	offlineHTTPRouter(s, offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}).ServeHTTP(response, read)
	if response.Code != http.StatusNotFound {
		t.Fatalf("other runner readback: %d", response.Code)
	}
}
