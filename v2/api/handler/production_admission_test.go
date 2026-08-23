package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"norn/v2/api/config"
)

func TestProductionSubstrateMutationClassification(t *testing.T) {
	for _, path := range []string{
		"/api/apps/demo/deploy", "/api/apps/demo/rollback", "/api/deploy-groups/core/deploy",
		"/api/v1/platform/upgrades", "/api/webhooks/github",
	} {
		if !productionSubstrateMutation(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("expected %s to require production substrate", path)
		}
	}
	for _, path := range []string{
		"/api/v1/host/assurances", "/api/v1/auth/revoke", "/api/events/1/ack", "/api/v1/production/readiness",
	} {
		if productionSubstrateMutation(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("expected recovery/identity path %s to remain available", path)
		}
	}
}

func TestProductionMutationAdmissionFailsClosedButLeavesRepairPath(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{Profile: "production"}, nil, nil, nil, nil, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	middleware := h.ProductionMutationAdmissionMiddleware(next)

	blocked := httptest.NewRecorder()
	middleware.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", nil))
	if blocked.Code != http.StatusServiceUnavailable {
		t.Fatalf("runtime mutation status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	repair := httptest.NewRecorder()
	middleware.ServeHTTP(repair, httptest.NewRequest(http.MethodPost, "/api/v1/host/assurances", nil))
	if repair.Code != http.StatusNoContent {
		t.Fatalf("repair path status=%d", repair.Code)
	}
}

func TestProductionCriticalBlockersAreStableAndScoped(t *testing.T) {
	report := ProductionReadinessReport{Checks: []ProductionReadinessCheck{
		{ID: "observability.core", Status: "fail"},
		{ID: "consul.quorum", Status: "fail"},
		{ID: "nomad.reachable", Status: "fail"},
		{ID: "audit.durable", Status: "pass"},
	}}
	want := []string{"consul.quorum", "nomad.reachable"}
	if got := productionCriticalBlockers(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("blockers=%v want=%v", got, want)
	}
}
