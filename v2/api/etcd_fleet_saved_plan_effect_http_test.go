package main

import (
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
)

func TestEtcdFleetSavedPlanEffectRoutesNeedBothGates(t *testing.T) {
	routes := []string{
		"GET /api/v1/fleet/saved-plan-effect-preparations/pending",
		"POST /api/v1/fleet/plans/{planID}/saved-plan-effect-plan-binding",
		"GET /api/v1/fleet/plans/{planID}/saved-plan-effect-plan-binding",
		"POST /api/v1/fleet/plans/{planID}/saved-plan-effect-dispatch-binding",
		"GET /api/v1/fleet/plans/{planID}/saved-plan-effect-dispatch-binding",
		"POST /api/v1/fleet/plans/{planID}/saved-plan-effect-preparation",
		"GET /api/v1/fleet/plans/{planID}/saved-plan-effect-preparation",
		"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/saved-plan-effect-grant",
		"GET /api/v1/fleet/plans/{planID}/attempts/{attemptID}/saved-plan-effect-claim",
	}
	for _, tc := range []struct{ enabled, executor, want bool }{{}, {enabled: true}, {executor: true}, {enabled: true, executor: true, want: true}} {
		router := chi.NewRouter()
		registerEtcdFleetSavedPlanEffectRoutes(router, &config.Config{FleetSavedPlanEffectEnabled: tc.enabled}, nil, nil, tc.executor)
		set := laneRouteSet(laneWalkRoutes(t, router))
		for _, route := range routes {
			if got := set[route]; got != tc.want {
				t.Fatalf("enabled=%t executor=%t route=%s got=%t", tc.enabled, tc.executor, route, got)
			}
		}
	}
}
