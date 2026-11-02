package main

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type fleetDeploymentRecoveryStore interface {
	RequeueHeldFleetDeploymentByID(context.Context, string, string) error
}

// Only a device-enrolled platform operator can requeue a specific held receipt.
func registerEtcdFleetDeploymentRecoveryRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations fleetDeploymentRecoveryStore) {
	router.With(platformReleaseOperatorApprovalAuth(cfg, identities)).Post("/api/v1/fleet/deployments/{deploymentId}/operations/{operationId}/recovery", func(w http.ResponseWriter, r *http.Request) {
		deploymentID, operationID := chi.URLParam(r, "deploymentId"), chi.URLParam(r, "operationId")
		if _, err := uuid.Parse(deploymentID); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_deployment_id", "invalid deployment ID")
			return
		}
		if _, err := uuid.Parse(operationID); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_operation_id", "invalid operation ID")
			return
		}
		if r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_recovery_request", "request body must be empty")
			return
		}
		if err := operations.RequeueHeldFleetDeploymentByID(r.Context(), operationID, deploymentID); err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_deployment_recovery_refused", "held deployment could not be requeued")
			return
		}
		platformReleaseResponse(w, http.StatusAccepted, map[string]string{"operationId": operationID, "deploymentId": deploymentID, "status": "queued"})
	})
}
