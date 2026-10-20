package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineTestIdentity struct {
	activeManagedIdentity
	device *store.AccessDevice
}

func (i offlineTestIdentity) ActiveAccessDevice(context.Context, string) (*store.AccessDevice, error) {
	return i.device, nil
}

func TestOfflineFleetTrustFailsClosed(t *testing.T) {
	base := &config.Config{FleetAuthorityOnly: true, Profile: "staging", Environment: "staging", FleetOfflineOwnerKeyID: "owner-1", FleetOfflineRunnerSubject: "offline-runner", FleetOfflineVerifierSubject: "offline-verifier"}
	if trust, err := loadOfflineFleetTrust(&config.Config{FleetAuthorityOnly: true, Environment: "staging"}); err != nil || trust != nil {
		t.Fatalf("unset trust must disable route: %v %v", trust, err)
	}
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("partial trust accepted")
	}
	path := filepath.Join(t.TempDir(), "owner.pub")
	public, _, _ := ed25519.GenerateKey(nil)
	if err := os.WriteFile(path, []byte(base64.RawStdEncoding.EncodeToString(public)), 0600); err != nil {
		t.Fatal(err)
	}
	base.FleetOfflineOwnerPublicKeyFile = path
	if trust, err := loadOfflineFleetTrust(base); err != nil || trust == nil || trust.runnerSubject != "offline-runner" || trust.verifierSubject != "offline-verifier" {
		t.Fatalf("valid trust refused: %v %v", trust, err)
	}
	base.FleetOfflineVerifierSubject = base.FleetOfflineRunnerSubject
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("shared runner and verifier identity accepted")
	}
	base.FleetOfflineVerifierSubject = "offline-verifier"
	base.Environment = "production"
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("production trust accepted")
	}
	base.Environment = "staging"
	base.Profile = "production"
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("production profile with staging environment accepted")
	}
	base.Profile = "development"
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("development profile with staging environment accepted")
	}
	base.Profile = "staging"
	link := filepath.Join(t.TempDir(), "link.pub")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	base.FleetOfflineOwnerPublicKeyFile = link
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("symlinked owner key accepted")
	}
	base.FleetOfflineOwnerPublicKeyFile = path
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOfflineFleetTrust(base); err == nil {
		t.Fatal("writable owner key accepted")
	}
}

func TestOfflineReservationRefusesUndelegatedAdministrator(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	conf := &config.Config{APIToken: secret}
	router := chi.NewRouter()
	registerEtcdFleetOfflineRoutes(router, conf, offlineTestIdentity{}, nil,
		&offlineFleetTrust{keyID: "owner-1", public: make(ed25519.PublicKey, ed25519.PublicKeySize), runnerSubject: "offline-runner"})
	record, err := handler.NewManagedAccessTokenRecord("operator", []string{handler.ScopeAdmin}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := handler.SignManagedAccessToken(secret, record)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/reservations", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "offline_reservation_principal_invalid") {
		t.Fatalf("non-device admin was admitted: %d %s", response.Code, response.Body.String())
	}
}

func TestOfflineReservationRequiresActiveMatchingDevice(t *testing.T) {
	for name, device := range map[string]*store.AccessDevice{
		"missing":   nil,
		"different": {ID: "other"},
		"revoked":   {ID: "device-1", RevokedAt: func() *time.Time { at := time.Now(); return &at }()},
	} {
		t.Run(name, func(t *testing.T) {
			if offlineReservationDeviceActive(context.Background(), offlineTestIdentity{device: device}, "device-1") {
				t.Fatal("inactive or mismatched device accepted")
			}
		})
	}
	if !offlineReservationDeviceActive(context.Background(), offlineTestIdentity{device: &store.AccessDevice{ID: "device-1"}}, "device-1") {
		t.Fatal("active matching device refused")
	}
}

func TestOfflineRunnerPrincipalRequiresPinnedActiveDevice(t *testing.T) {
	trust := &offlineFleetTrust{runnerSubject: "offline-runner"}
	active := offlineTestIdentity{device: &store.AccessDevice{ID: "runner-device"}}
	base := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "offline-runner", TokenID: "runner-token", DeviceID: "runner-device", Scopes: []string{handler.ScopeFleetOperate}}
	check := func(principal handler.AccessPrincipal, identities offlineTestIdentity) bool {
		request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/", nil), &principal)
		_, ok := offlineRunnerPrincipal(request, trust, identities)
		return ok
	}
	if !check(base, active) {
		t.Fatal("pinned active runner refused")
	}
	for name, change := range map[string]func(*handler.AccessPrincipal){
		"other subject":  func(p *handler.AccessPrincipal) { p.Subject = "operator" },
		"missing device": func(p *handler.AccessPrincipal) { p.DeviceID = "" },
		"missing token":  func(p *handler.AccessPrincipal) { p.TokenID = "" },
		"admin scope":    func(p *handler.AccessPrincipal) { p.Scopes = []string{handler.ScopeFleetOperate, handler.ScopeAdmin} },
		"legacy":         func(p *handler.AccessPrincipal) { p.Legacy = true },
		"shared api":     func(p *handler.AccessPrincipal) { p.Source = handler.AccessPrincipalSourceSharedAPI },
	} {
		t.Run(name, func(t *testing.T) {
			principal := base
			change(&principal)
			if check(principal, active) {
				t.Fatal("invalid runner accepted")
			}
		})
	}
	if check(base, offlineTestIdentity{}) {
		t.Fatal("runner without registered active device accepted")
	}
}

func TestOfflineVerifierPrincipalRequiresDistinctPinnedActiveDevice(t *testing.T) {
	trust := &offlineFleetTrust{runnerSubject: "offline-runner", verifierSubject: "offline-verifier"}
	active := offlineTestIdentity{device: &store.AccessDevice{ID: "verifier-device"}}
	base := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "offline-verifier", TokenID: "verifier-token", DeviceID: "verifier-device", Scopes: []string{handler.ScopeFleetVerify}}
	check := func(principal handler.AccessPrincipal, identities offlineTestIdentity) bool {
		request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &principal)
		_, ok := offlineVerifierPrincipal(request, trust, identities)
		return ok
	}
	if !check(base, active) {
		t.Fatal("pinned active verifier refused")
	}
	for name, change := range map[string]func(*handler.AccessPrincipal){
		"runner subject": func(p *handler.AccessPrincipal) { p.Subject = "offline-runner" },
		"runner scope":   func(p *handler.AccessPrincipal) { p.Scopes = []string{handler.ScopeFleetOperate} },
		"extra scope":    func(p *handler.AccessPrincipal) { p.Scopes = []string{handler.ScopeFleetVerify, handler.ScopeAPIRead} },
		"missing device": func(p *handler.AccessPrincipal) { p.DeviceID = "" },
		"legacy":         func(p *handler.AccessPrincipal) { p.Legacy = true },
		"ci":             func(p *handler.AccessPrincipal) { p.CI = &handler.CIIdentity{} },
	} {
		t.Run(name, func(t *testing.T) {
			principal := base
			change(&principal)
			if check(principal, active) {
				t.Fatal("invalid verifier accepted")
			}
		})
	}
	if check(base, offlineTestIdentity{}) {
		t.Fatal("verifier without active device accepted")
	}
}
