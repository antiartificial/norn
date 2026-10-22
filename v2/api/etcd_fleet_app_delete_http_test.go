package main

import (
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
)

func TestEtcdFleetAppDeleteCoPermitRouteNeedsBothGates(t *testing.T) {
	const route = "POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-effect"
	for _, tc := range []struct {
		name, flag string
		lane, want bool
	}{
		{name: "default off", lane: true},
		{name: "executor off", flag: "on"},
		{name: "both on", flag: "on", lane: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			registerEtcdFleetAppDeleteCoPermitRoute(router, &config.Config{FleetAppDeleteCoPermitEnabled: tc.flag == "on"}, nil, nil, tc.lane)
			if got := laneRouteSet(laneWalkRoutes(t, router))[route]; got != tc.want {
				t.Fatalf("route present=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestEtcdFleetAppDeleteTerminalRoutesShareDefaultOffGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		router := chi.NewRouter()
		registerEtcdFleetAppDeleteCoPermitRoute(router, &config.Config{FleetAppDeleteCoPermitEnabled: enabled}, nil, nil, true)
		routes := laneRouteSet(laneWalkRoutes(t, router))
		for _, route := range []string{"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-terminal", "GET /api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-terminal"} {
			if routes[route] != enabled {
				t.Fatalf("%s present=%t", route, routes[route])
			}
		}
	}
}
