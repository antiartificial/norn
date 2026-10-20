package handler

import (
	"testing"
	"time"
)

func TestOfflineAttemptScopeOnlyDedicatedDevice(t *testing.T) {
	h := &Handler{}
	if _, err := h.deviceEnrollmentScopesForPlatform(FleetOfflineRunnerPlatform, []string{ScopeFleetOfflineAttempt}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		platform string
		scopes   []string
	}{
		{"ios", []string{ScopeFleetOfflineAttempt}},
		{FleetExecutorPlatform, []string{ScopeFleetOfflineAttempt}},
		{FleetOfflineRunnerPlatform, []string{ScopeFleetOfflineAttempt, ScopeAPIRead}},
		{FleetOfflineRunnerPlatform, []string{ScopeFleetOfflineAttempt, ScopeFleetOperate}},
	} {
		if _, err := h.deviceEnrollmentScopesForPlatform(tc.platform, tc.scopes); err == nil {
			t.Fatalf("accepted %s %v", tc.platform, tc.scopes)
		}
	}
	if _, err := NewManagedAccessTokenRecord("offline", []string{ScopeFleetOfflineAttempt}, time.Hour, time.Now()); err == nil {
		t.Fatal("general token issuance accepted offline scope")
	}
	p := AccessPrincipal{Subject: "offline", TokenID: "token", DeviceID: "device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetOfflineAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
	if !IsFleetOfflineAttemptPrincipal(p) {
		t.Fatal("dedicated device principal rejected")
	}
	p.CI = &CIIdentity{Provider: "github-actions"}
	if IsFleetOfflineAttemptPrincipal(p) {
		t.Fatal("GitHub principal accepted")
	}
}
