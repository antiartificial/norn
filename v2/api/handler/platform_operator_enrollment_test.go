package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/config"
)

func TestPlatformOperatorEnrollmentIsDedicatedAndSingleScope(t *testing.T) {
	cfg := &config.Config{APIToken: strings.Repeat("s", 32), FleetPlatformReleaseIntentEnabled: true}
	h := NewEtcdPlatformOperatorEnrollmentHandler(cfg, nil)
	for _, tc := range []struct {
		platform string
		scopes   []string
		allowed  bool
	}{
		{FleetPlatformOperatorPlatform, []string{ScopeFleetDeploymentRecover}, true},
		{"ios", []string{ScopeFleetDeploymentRecover}, false},
		{FleetExecutorPlatform, []string{ScopeFleetExecute}, false},
		{FleetPlatformOperatorPlatform, []string{ScopeAPIRead}, false},
		{FleetPlatformOperatorPlatform, []string{ScopeFleetDeploymentRecover, ScopeAPIRead}, false},
		{FleetPlatformOperatorPlatform, []string{ScopePlatformOperate}, false},
		{FleetPlatformOperatorPlatform, []string{ScopeAdmin}, false},
		{FleetPlatformOperatorPlatform, nil, false},
	} {
		_, err := h.deviceEnrollmentScopesForPlatform(tc.platform, tc.scopes)
		if (err == nil) != tc.allowed {
			t.Errorf("platform=%q scopes=%v allowed=%v err=%v", tc.platform, tc.scopes, tc.allowed, err)
		}
	}
	if _, err := (&Handler{cfg: cfg}).deviceEnrollmentScopesForPlatform(FleetPlatformOperatorPlatform, []string{ScopeFleetDeploymentRecover}); err == nil {
		t.Fatal("generic enrollment admitted dedicated operator platform")
	}
	if _, err := (&Handler{cfg: cfg}).deviceEnrollmentScopesForPlatform("ios", []string{ScopeFleetDeploymentRecover}); err == nil {
		t.Fatal("generic enrollment admitted recovery scope")
	}
	if got := deviceTokenLifetimeForPlatform(FleetPlatformOperatorPlatform); got != 30*time.Minute {
		t.Fatalf("operator token lifetime=%s", got)
	}
	subject := FleetDeploymentRecoverySubjectPrefix + uuid.NewString()
	_, record, err := issueDeviceTokenForPlatform(cfg, FleetPlatformOperatorPlatform, "device", subject, []string{ScopeFleetDeploymentRecover}, "", time.Hour)
	if err != nil || record.ExpiresAt.Sub(record.IssuedAt) != 30*time.Minute {
		t.Fatalf("operator lifetime not bounded: record=%v err=%v", record, err)
	}
	if _, _, err := issueDeviceTokenForPlatform(cfg, FleetPlatformOperatorPlatform, "device", subject, []string{ScopeAPIRead}, "", time.Hour); err == nil {
		t.Fatal("operator device received different scope")
	}
	if _, _, err := issueDeviceTokenForPlatform(cfg, "ios", "device", subject, []string{ScopeFleetDeploymentRecover}, "", time.Hour); err == nil {
		t.Fatal("other device platform received recovery scope")
	}
	if _, _, err := issueDeviceTokenForPlatform(&config.Config{APIToken: cfg.APIToken}, FleetPlatformOperatorPlatform, "device", subject, []string{ScopeFleetDeploymentRecover}, "", time.Hour); err == nil {
		t.Fatal("operator token issued while lane is disabled")
	}
	if _, err := NewManagedAccessTokenRecord(subject, []string{ScopeFleetDeploymentRecover}, time.Minute, time.Now()); err == nil {
		t.Fatal("plain managed token received device-only recovery scope")
	}
}

func TestPlatformOperatorPairingRejectsUntrustedTransport(t *testing.T) {
	h := NewEtcdPlatformOperatorEnrollmentHandler(&config.Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "http://control.example/api/v1/fleet/platform-operator/enrollments", strings.NewReader(`{"deviceName":"fleet-deployment-recovery:11111111-1111-4111-8111-111111111111","platform":"fleet-platform-operator","requestedScopes":["fleet:deployment-recover"]}`))
	req.RemoteAddr = "198.51.100.9:1234"
	rec := httptest.NewRecorder()
	h.StartDeviceEnrollment(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("untrusted pairing status=%d", rec.Code)
	}
}

func TestPlatformOperatorTokenCannotRotate(t *testing.T) {
	cfg := &config.Config{APIToken: strings.Repeat("s", 32), FleetPlatformReleaseIntentEnabled: true}
	auth := &lifecycleAuthStore{platform: FleetPlatformOperatorPlatform}
	principal := AccessPrincipal{Subject: FleetDeploymentRecoverySubjectPrefix + uuid.NewString(), TokenID: "jti", DeviceID: "device",
		Scopes: []string{ScopeFleetDeploymentRecover}, ExpiresAt: time.Now().Add(time.Minute),
		Source: AccessPrincipalSourceManagedToken}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &principal)
	rec := httptest.NewRecorder()
	RotateManagedToken(cfg, auth, nil, rec, req)
	if rec.Code != http.StatusForbidden || auth.issued != nil {
		t.Fatalf("operator rotation status=%d issued=%t", rec.Code, auth.issued != nil)
	}
}
