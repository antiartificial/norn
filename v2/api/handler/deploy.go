package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) Deploy(w http.ResponseWriter, r *http.Request) {
	if h.productionRequiresSignedPromotion() {
		WriteControlProblem(w, r, http.StatusConflict, "signed_promotion_required", "direct production deploys are disabled; promote a signed staging qualification through /api/v1/apps/{id}/promotions")
		return
	}
	id := chi.URLParam(r, "id")

	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"ref": req.Ref, "action": "deploy"})
	if !ok {
		return
	}

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	accepted, err := h.pipeline.Run(r.Context(), spec, req.Ref, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}

	writeJSON(w, map[string]string{
		"sagaId":      accepted.Operation.SagaID,
		"operationId": accepted.Operation.ID,
		"status":      string(accepted.Operation.Status),
	})
}

func (h *Handler) Preflight(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"ref": req.Ref, "action": "preflight"})
	if !ok {
		return
	}

	specs, err := model.DiscoverAllApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	accepted, err := h.pipeline.Preflight(r.Context(), spec, req.Ref, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}

	writeJSON(w, map[string]string{
		"sagaId":      accepted.Operation.SagaID,
		"operationId": accepted.Operation.ID,
		"status":      string(accepted.Operation.Status),
	})
}

func (h *Handler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	deployments, err := h.db.ListDeployments(r.Context(), app, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deployments == nil {
		deployments = []model.Deployment{}
	}
	writeJSON(w, deployments)
}

func decodeJSON(r *http.Request, v interface{}) error {
	const maxLegacyJSONBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLegacyJSONBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxLegacyJSONBody {
		return fmt.Errorf("JSON request exceeds %d bytes", maxLegacyJSONBody)
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}
