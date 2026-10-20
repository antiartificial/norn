package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// FleetExecutorPlatform is the only device platform that may hold
	// fleet:execute.
	FleetExecutorPlatform           = "fleet-executor"
	FleetOfflineRunnerPlatform      = "fleet-offline-runner"
	fleetOfflineDeviceSubjectPrefix = "fleet-offline-device:"
	// fleetExecutorTokenTTL is the executor token lifetime (D8). Rotation keeps it.
	fleetExecutorTokenTTL = 7 * 24 * time.Hour
)

func isFleetExecutorPlatform(platform string) bool {
	return strings.EqualFold(strings.TrimSpace(platform), FleetExecutorPlatform)
}

// deviceEnrollmentScopesForPlatform adds the fleet:execute rule to
// deviceEnrollmentScopes: the scope is valid only alone and only for the
// fleet-executor platform. Every other request is unchanged.
func (h *Handler) deviceEnrollmentScopesForPlatform(platform string, requested []string) ([]string, error) {
	if h != nil && h.offlineEnrollmentOnly && (!strings.EqualFold(strings.TrimSpace(platform), FleetOfflineRunnerPlatform) || len(requested) != 1 || requested[0] != ScopeFleetOfflineAttempt) {
		return nil, fmt.Errorf("offline enrollment permits only %s on %s", ScopeFleetOfflineAttempt, FleetOfflineRunnerPlatform)
	}
	if containsScope(requested, ScopeFleetOfflineAttempt) {
		scopes, err := normalizeAccessTokenScopes(requested)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(platform), FleetOfflineRunnerPlatform) || len(scopes) != 1 || scopes[0] != ScopeFleetOfflineAttempt {
			return nil, fmt.Errorf("%s must be granted alone to %s devices", ScopeFleetOfflineAttempt, FleetOfflineRunnerPlatform)
		}
		return scopes, nil
	}
	hasExecute := false
	for _, scope := range requested {
		if strings.TrimSpace(scope) == ScopeFleetExecute {
			hasExecute = true
		}
	}
	if !hasExecute {
		return h.deviceEnrollmentScopes(requested)
	}
	scopes, err := normalizeAccessTokenScopes(requested)
	if err != nil {
		return nil, err
	}
	if !isFleetExecutorPlatform(platform) {
		return nil, fmt.Errorf("%s is grantable only to %s devices", ScopeFleetExecute, FleetExecutorPlatform)
	}
	if len(scopes) != 1 {
		return nil, fmt.Errorf("%s must be granted on its own", ScopeFleetExecute)
	}
	return scopes, nil
}

// IsFleetExecutorPrincipal reports whether p is the executor lane: a live
// managed device token whose scopes are exactly [fleet:execute]. It does not
// use Allows, so the admin scope and legacy tokens never satisfy it, and CI or
// GitHub OIDC principals (CI != nil) never do either.
func IsFleetExecutorPrincipal(p AccessPrincipal) bool {
	return p.Source == AccessPrincipalSourceManagedToken && !p.Legacy &&
		p.TokenID != "" && p.DeviceID != "" && p.CI == nil &&
		len(p.Scopes) == 1 && p.Scopes[0] == ScopeFleetExecute &&
		!p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

// IsFleetOfflineAttemptPrincipal is a device-bound admission identity. It is
// distinct from the GitHub fleet:operate and executor lanes and grants no
// credential, provider access or effect transition.
func IsFleetOfflineAttemptPrincipal(p AccessPrincipal) bool {
	return p.Source == AccessPrincipalSourceManagedToken && !p.Legacy &&
		p.TokenID != "" && p.DeviceID != "" && p.Subject == fleetOfflineDeviceSubjectPrefix+p.DeviceID && p.CI == nil &&
		len(p.Scopes) == 1 && p.Scopes[0] == ScopeFleetOfflineAttempt &&
		!p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

// FleetExecutorPrincipalFromRequest returns the request's principal when it is
// the executor lane.
func FleetExecutorPrincipalFromRequest(r *http.Request) (AccessPrincipal, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	if !ok || !IsFleetExecutorPrincipal(p) {
		return AccessPrincipal{}, false
	}
	return p, true
}
