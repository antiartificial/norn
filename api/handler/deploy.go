package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/api/hub"
	"norn/api/model"
)

type DeployRequest struct {
	CommitSHA string `json:"commitSha"`
}

func (h *Handler) Deploy(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "deploy requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	var req DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	commitSHA := req.CommitSHA
	imageTag := fmt.Sprintf("%s:%s", appID, commitSHA[:min(12, len(commitSHA))])

	// For repo-backed apps with "HEAD", the clone step resolves the real SHA
	if spec.Repo != nil && (commitSHA == "" || commitSHA == "HEAD") {
		commitSHA = "HEAD"
		imageTag = fmt.Sprintf("%s:pending", appID)
	}

	deploy := &model.Deployment{
		ID:        uuid.NewString(),
		App:       appID,
		CommitSHA: commitSHA,
		ImageTag:  imageTag,
		Status:    model.StatusQueued,
		StartedAt: time.Now(),
	}

	if err := h.db.InsertDeployment(r.Context(), deploy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "deploy.queued", AppID: appID, Payload: deploy})

	go h.runPipeline(deploy, spec)

	writeJSON(w, deploy)
}

func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "rollback requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	var req struct {
		ImageTag string `json:"imageTag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ImageTag == "" {
		deploys, err := h.db.ListDeployments(r.Context(), appID, 2)
		if err != nil || len(deploys) < 2 {
			http.Error(w, "no previous deployment to rollback to", http.StatusBadRequest)
			return
		}
		req.ImageTag = deploys[1].ImageTag
	}

	if err := h.kube.SetImage(r.Context(), "default", appID, appID, req.ImageTag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "deploy.rollback", AppID: appID, Payload: map[string]string{"imageTag": req.ImageTag}})
	writeJSON(w, map[string]string{"status": "rolling_back", "imageTag": req.ImageTag})
}

func (h *Handler) ListArtifacts(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	retain := 5
	if spec.Artifacts != nil {
		retain = spec.Artifacts.Retain
	}

	deploys, err := h.db.ListDeployments(r.Context(), appID, retain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var artifacts []map[string]interface{}
	for _, d := range deploys {
		artifacts = append(artifacts, map[string]interface{}{
			"imageTag":  d.ImageTag,
			"commitSha": d.CommitSHA,
			"status":    d.Status,
			"deployedAt": d.StartedAt,
		})
	}

	writeJSON(w, artifacts)
}
