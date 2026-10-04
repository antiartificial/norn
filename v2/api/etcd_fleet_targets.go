package main

import (
	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// registerEtcdFleetTargetRoutes mounts the Fleet target and fence routes on
// the etcd runtime router (plan.md WP9b, Q6). Scopes are enforced twice: by
// the managed-token middleware here and again inside each handler.
func registerEtcdFleetTargetRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations *etcdstore.V3OperationStore, fleetGitHub *githubapp.Client) {
	var observer handler.FleetTargetRunObserver
	if fleetGitHub != nil {
		observer = fleetGitHub
	}
	fleetTargets := handler.NewEtcdFleetTargetRoutes(cfg, operations, observer)
	targetRead := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)
	targetRegister := etcdManagedTokenAuth(cfg, identities, handler.ScopePlatformOperate)
	targetAdmin := etcdManagedTokenAuth(cfg, identities, handler.ScopeAdmin)
	router.With(targetRegister).Post("/api/v1/fleet/targets", fleetTargets.Register)
	router.With(targetAdmin).Post("/api/v1/fleet/targets/abandon-plan", fleetTargets.AbandonPlan)
	router.With(targetRead).Get("/api/v1/fleet/targets/{targetID}", fleetTargets.Get)
	router.With(targetRead).Get("/api/v1/fleet/targets/{targetID}/fence", fleetTargets.Fence)
	router.With(targetAdmin).Post("/api/v1/fleet/targets/{targetID}/fence/release", fleetTargets.Release)
}
