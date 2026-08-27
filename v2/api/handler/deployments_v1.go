package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

func (h *Handler) ListDeploymentsV1(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "deployment_store_unavailable", "deployment history storage is unavailable")
		return
	}
	app := strings.TrimSpace(r.URL.Query().Get("app"))
	if app != "" && !validAppIDRe.MatchString(app) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_id", "app must match the InfraSpec name format")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validDeploymentStatus(status) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_deployment_status", "status must be a known deployment state")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 10000 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offset", "offset must be between 0 and 10000")
			return
		}
		offset = parsed
	}
	deployments, err := h.db.ListDeploymentsPage(r.Context(), app, status, limit, offset)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "deployment_history_read_failed", "failed to read deployment history")
		return
	}
	if deployments == nil {
		deployments = []model.Deployment{}
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"schemaVersion": "norn.deployments/v1", "deployments": deployments, "count": len(deployments), "offset": offset})
}

func validDeploymentStatus(status string) bool {
	switch model.DeployStatus(status) {
	case model.StatusQueued, model.StatusBuilding, model.StatusTesting, model.StatusMigrating,
		model.StatusSubmitting, model.StatusHealthy, model.StatusDeployed, model.StatusFailed:
		return true
	default:
		return false
	}
}

func (h *Handler) GetDeploymentV1(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	deployment, ok := h.requireDeploymentV1(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"schemaVersion": "norn.deployment/v1", "deployment": deployment})
}

func (h *Handler) ListDeploymentStepsV1(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	deployment, ok := h.requireDeploymentV1(w, r)
	if !ok {
		return
	}
	steps, err := h.db.ListDeploymentSteps(r.Context(), deployment.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "deployment_steps_read_failed", "failed to read deployment steps")
		return
	}
	if steps == nil {
		steps = []model.DeploymentStep{}
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{
		"schemaVersion": "norn.deployment-steps/v1", "deploymentId": deployment.ID,
		"steps": steps, "count": len(steps),
	})
}

func (h *Handler) requireDeploymentV1(w http.ResponseWriter, r *http.Request) (*model.Deployment, bool) {
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "deployment_store_unavailable", "deployment history storage is unavailable")
		return nil, false
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_deployment_id", "deployment ID must be a UUID")
		return nil, false
	}
	deployment, err := h.db.GetDeployment(r.Context(), id)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "deployment_not_found", "deployment not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "deployment_history_read_failed", "failed to read deployment")
		return nil, false
	}
	return deployment, true
}
