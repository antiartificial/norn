package main

import (
	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// registerEtcdFleetAppDeleteCoPermitRoute exposes only Norn's one-use
// attempt/fence co-permit. The separate Fleet PostgreSQL verifier and writer
// reservation are still required before a provider DELETE can run.
func registerEtcdFleetAppDeleteCoPermitRoute(router chi.Router, cfg *config.Config, identities store.IdentityStore, permits interface {
	handler.AppDeleteEffectStore
	handler.App04DeleteAdmissionStore
	handler.AppDeleteTerminalStore
	handler.AppDeleteFinalizeStore
	handler.AppDeleteSnapshotStore
}, executorLane bool) {
	if cfg == nil || !executorLane || !cfg.FleetAppDeleteCoPermitEnabled {
		return
	}
	attemptAuth := etcdFleetLaneAuth(cfg, identities, handler.ScopeFleetAttempt)
	router.With(attemptAuth).Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-effect", handler.AppDeleteEffectHandler(permits))
	verifierAuth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetAppDeleteVerify)
	router.With(verifierAuth).Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-snapshot", handler.AppDeleteSnapshotHandler(permits))
	router.With(verifierAuth).Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app04-delete-admission", handler.App04DeleteAdmissionHandler(permits))
	router.With(verifierAuth).Get("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app04-delete-admission", handler.App04DeleteAdmissionHandler(permits))
	terminal := "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-terminal"
	router.With(verifierAuth).Post(terminal, handler.AppDeleteTerminalHandler(permits))
	router.With(verifierAuth).Get(terminal, handler.AppDeleteTerminalHandler(permits))
	finalize := "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/app-delete-finalize"
	router.With(verifierAuth).Post(finalize, handler.AppDeleteFinalizeHandler(permits))
	router.With(verifierAuth).Get(finalize, handler.AppDeleteFinalizeHandler(permits))
}
