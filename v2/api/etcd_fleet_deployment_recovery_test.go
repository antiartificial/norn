package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/handler"
)

type recoveryRouteStore struct {
	operation, deployment string
	calls                 int
}

func (s *recoveryRouteStore) RequeueHeldFleetDeploymentByID(_ context.Context, operation, deployment string) error {
	s.calls++
	if operation != s.operation || deployment != s.deployment {
		return errors.New("identity mismatch")
	}
	return nil
}

func TestFleetDeploymentRecoveryRouteAuthorization(t *testing.T) {
	cfg := &config.Config{APIToken: "fleet-recovery-auth-test-secret"}
	fixture := &recoveryRouteStore{operation: uuid.NewString(), deployment: uuid.NewString()}
	router := chi.NewRouter()
	registerEtcdFleetDeploymentRecoveryRoutes(router, cfg, platformReleaseAuthIdentity{}, fixture)
	path := func(deployment, operation string) string {
		return "/api/v1/fleet/deployments/" + deployment + "/operations/" + operation + "/recovery"
	}
	cases := []struct {
		name                  string
		scopes                []string
		device                string
		ci                    bool
		deployment, operation string
		status, calls         int
	}{
		{"ci", []string{handler.ScopePlatformOperate}, "device", true, fixture.deployment, fixture.operation, http.StatusForbidden, 0},
		{"unenrolled", []string{handler.ScopePlatformOperate}, "", false, fixture.deployment, fixture.operation, http.StatusForbidden, 0},
		{"wrong scope", []string{handler.ScopeAPIWrite}, "device", false, fixture.deployment, fixture.operation, http.StatusForbidden, 0},
		{"malformed operation", []string{handler.ScopePlatformOperate}, "device", false, fixture.deployment, "bad", http.StatusBadRequest, 0},
		{"malformed deployment", []string{handler.ScopePlatformOperate}, "device", false, "bad", fixture.operation, http.StatusBadRequest, 0},
		{"wrong deployment", []string{handler.ScopePlatformOperate}, "device", false, uuid.NewString(), fixture.operation, http.StatusConflict, 1},
		{"wrong operation", []string{handler.ScopePlatformOperate}, "device", false, fixture.deployment, uuid.NewString(), http.StatusConflict, 1},
		{"enrolled operator", []string{handler.ScopePlatformOperate}, "device", false, fixture.deployment, fixture.operation, http.StatusAccepted, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fixture.calls
			request := httptest.NewRequest(http.MethodPost, path(tc.deployment, tc.operation), nil)
			request.Header.Set("Authorization", "Bearer "+platformReleaseTestToken(t, cfg.APIToken, tc.scopes, tc.device, tc.ci))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || fixture.calls-before != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, fixture.calls-before, response.Body)
			}
		})
	}
}
