package main

import (
	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// registerEtcdFleetResourceRoutes mounts the Fleet resource routes on the
// etcd runtime router (plan.md WP13, Q6). Scopes are enforced twice: by the
// managed-token middleware here and again inside each handler. liveness is
// the in-process reconciler, or nil when this runtime runs none.
func registerEtcdFleetResourceRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations *etcdstore.V3OperationStore, fleetGitHub *githubapp.Client, liveness handler.FleetControllerLiveness) {
	var resolver handler.FleetApprovedPlanResolver
	if fleetGitHub != nil {
		resolver = fleetGitHub
	}
	fleetResources := handler.NewEtcdFleetResourceRoutes(cfg, operations, resolver, func() (string, error) { return etcdFleetGitHubEnvironment(cfg) }, liveness)
	resourceRead := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)
	resourceCreate := etcdManagedTokenAuth(cfg, identities, handler.ScopePlatformOperate)
	resourceWrite := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)
	resourceObserve := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetOperate)
	router.With(resourceRead).Get("/api/v1/fleet/resources", fleetResources.List)
	router.With(resourceCreate).Post("/api/v1/fleet/resources/{name}", fleetResources.Create)
	router.With(resourceRead).Get("/api/v1/fleet/resources/{name}", fleetResources.Get)
	router.With(resourceWrite).Post("/api/v1/fleet/resources/{name}/desired", fleetResources.SetDesired)
	router.With(resourceRead).Get("/api/v1/fleet/resources/{name}/observations", fleetResources.ListObservations)
	router.With(resourceObserve).Post("/api/v1/fleet/resources/{name}/observations", fleetResources.AppendObservation)
}
