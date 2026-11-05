package main

import (
	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// The apply grant is a separate default-off lane. The verifier can record a
// claim or read-only refresh continuation, while only the exact norn-attempt
// token can consume the single apply response.
func registerEtcdFleetSavedPlanEffectRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, effects handler.SavedPlanEffectStore, executorLane bool) {
	if cfg == nil || !executorLane || !cfg.FleetSavedPlanEffectEnabled {
		return
	}
	verifier := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetSavedPlanEffectVerify)
	attempt := etcdFleetLaneAuth(cfg, identities, handler.ScopeFleetAttempt)
	planBase := "/api/v1/fleet/plans/{planID}"
	base := planBase + "/attempts/{attemptID}"
	router.With(verifier).Get("/api/v1/fleet/saved-plan-effect-preparations/pending", handler.SavedPlanEffectPreparationPendingHandler(effects))
	router.With(verifier).MethodFunc("POST", planBase+"/saved-plan-effect-plan-binding", handler.SavedPlanEffectPlanBindingHandler(effects))
	router.With(verifier).MethodFunc("GET", planBase+"/saved-plan-effect-plan-binding", handler.SavedPlanEffectPlanBindingHandler(effects))
	router.With(verifier).MethodFunc("POST", planBase+"/saved-plan-effect-dispatch-binding", handler.SavedPlanEffectDispatchBindingHandler(effects))
	router.With(verifier).MethodFunc("GET", planBase+"/saved-plan-effect-dispatch-binding", handler.SavedPlanEffectDispatchBindingHandler(effects))
	router.With(verifier).MethodFunc("POST", planBase+"/saved-plan-effect-preparation", handler.SavedPlanEffectPreparationHandler(effects))
	router.With(verifier).MethodFunc("GET", planBase+"/saved-plan-effect-preparation", handler.SavedPlanEffectPreparationHandler(effects))
	router.With(verifier).Post(base+"/saved-plan-effect-claim", handler.SavedPlanEffectClaimHandler(effects))
	router.With(verifier).Get(base+"/saved-plan-effect-claim", handler.SavedPlanEffectClaimReadHandler(effects))
	router.With(attempt).Post(base+"/saved-plan-effect-grant", handler.SavedPlanEffectApplyHandler(effects))
	router.With(attempt).Get(base+"/saved-plan-effect-recovery", handler.SavedPlanEffectRecoveryHandler(effects))
	router.With(verifier).Post(base+"/saved-plan-refresh-only", handler.SavedPlanRefreshOnlyHandler(effects))
}

var _ func(chi.Router, *config.Config, store.IdentityStore, handler.SavedPlanEffectStore, bool) = registerEtcdFleetSavedPlanEffectRoutes
