package main

import (
	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
)

// registerEtcdFleetCreateRoutes exposes the default-off pre-registration
// intent, dispatch, attempt, verifier-readback, and recovery records. It also
// requires the Fleet executor lane; no provider effect is mounted.
func registerEtcdFleetCreateRoutes(router chi.Router, cfg *config.Config, identities *etcdstore.AuthStore,
	operations *etcdstore.V3OperationStore, executorLane bool) {
	if cfg == nil || !executorLane || !cfg.FleetCreateEnabled {
		return
	}
	verifier := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetCreateVerify)
	runner := etcdFleetLaneAuthGated(cfg, identities, executorLane, handler.ScopeFleetOperate)
	base := "/api/v1/fleet/create/{cluster}/intents"
	router.With(verifier).MethodFunc("POST", base, handler.FleetCreateIntentHandler(operations))
	router.With(verifier).MethodFunc("GET", base, handler.FleetCreateIntentHandler(operations))
	router.With(verifier).MethodFunc("POST", base+"/{intentID}/dispatch-binding", handler.FleetCreateDispatchBindingHandler(operations))
	router.With(verifier).MethodFunc("GET", base+"/{intentID}/dispatch-binding", handler.FleetCreateDispatchBindingHandler(operations))
	router.With(runner).Post(base+"/{intentID}/attempts", handler.FleetCreateAttemptHandler(operations))
	router.With(runner).Get(base+"/{intentID}/attempts/{attemptID}", handler.FleetCreateAttemptHandler(operations))
	router.With(verifier).Post(base+"/{intentID}/attempts/{attemptID}/admission", handler.FleetCreateAdmissionHandler(operations))
	router.With(verifier).Get(base+"/{intentID}/attempts/{attemptID}/admission", handler.FleetCreateAdmissionHandler(operations))
	router.With(verifier).Post(base+"/{intentID}/attempts/{attemptID}/effect-grant", handler.FleetCreateEffectGrantHandler(operations))
	router.With(verifier).Get(base+"/{intentID}/attempts/{attemptID}/effect-grant", handler.FleetCreateEffectGrantHandler(operations))
	router.With(runner).Post(base+"/{intentID}/attempts/{attemptID}/effect-grant/consume", handler.FleetCreateEffectGrantConsumeHandler(operations))
	router.With(runner).Get(base+"/{intentID}/attempts/{attemptID}/effect-grant/status", handler.FleetCreateEffectGrantStatusHandler(operations))
	router.With(verifier).Post(base+"/{intentID}/attempts/{attemptID}/recovery-readback", handler.FleetCreateRecoveryReadbackHandler(operations))
	router.With(runner).Post(base+"/{intentID}/attempts/{priorAttemptID}/recoveries", handler.FleetCreateRecoveryAttemptHandler(operations))
	router.With(verifier).Post(base+"/{intentID}/attempts/{attemptID}/terminal-receipt", handler.FleetCreateTerminalReceiptHandler(operations))
	router.With(verifier).Get(base+"/{intentID}/terminal-receipt", handler.FleetCreateTerminalReceiptHandler(operations))
	router.With(verifier).Post(base+"/{intentID}/target-registration", handler.FleetCreateTerminalTargetHandler(operations))
}
