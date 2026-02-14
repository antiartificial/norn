package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
)

func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	// Find app spec
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

	// Get current deployment
	deployments, err := h.db.ListDeployments(ctx, id, 1)
	if err != nil || len(deployments) == 0 {
		writeError(w, http.StatusNotFound, "no current deployment found")
		return
	}
	current := deployments[0]

	// Find previous successful deployment
	prev, err := h.db.LastSuccessfulDeployment(ctx, id, current.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no previous successful deployment to roll back to")
		return
	}

	// Create saga
	sg := saga.New(h.sagaStore, id, "api", "rollback")
	sg.Log(ctx, "rollback.start", fmt.Sprintf("rolling back %s to %s", id, prev.ImageTag), nil)

	// Create deployment record
	deploy := &model.Deployment{
		ID:        uuid.New().String(),
		App:       id,
		CommitSHA: prev.CommitSHA,
		ImageTag:  prev.ImageTag,
		SagaID:    sg.ID,
		Status:    model.StatusQueued,
		StartedAt: time.Now(),
	}
	if err := h.db.InsertDeployment(ctx, deploy); err != nil {
		log.Printf("rollback: insert deployment: %v", err)
	}

	// Run rollback async
	go h.runRollback(spec, deploy, sg, prev.ImageTag)

	writeJSON(w, map[string]string{
		"sagaId":   sg.ID,
		"status":   "rolling_back",
		"imageTag": prev.ImageTag,
	})
}

func (h *Handler) runRollback(spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string) {
	ctx := context.Background()

	steps := []struct {
		name string
		fn   func() error
	}{
		{"resolve-secrets", func() error {
			env := make(map[string]string)
			if h.secrets != nil {
				secretEnv, err := h.secrets.EnvMap(spec.App)
				if err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("resolve secrets: %w", err)
				}
				for k, v := range secretEnv {
					env[k] = v
				}
			}
			job := nomad.Translate(spec, imageTag, env)
			_, err := h.nomad.SubmitJob(job)
			return err
		}},
		{"healthy", func() error {
			return h.nomad.WaitHealthy(ctx, spec.App, 5*time.Minute)
		}},
	}

	for _, s := range steps {
		sg.StepStart(ctx, s.name)
		h.ws.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step": s.name, "sagaId": sg.ID, "status": "running",
		}})

		if err := s.fn(); err != nil {
			sg.StepFailed(ctx, s.name, err)
			h.ws.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
				"step": s.name, "sagaId": sg.ID, "status": "failed",
			}})
			h.db.UpdateDeployment(ctx, deploy.ID, model.StatusFailed)
			sg.Log(ctx, "deploy.failed", fmt.Sprintf("rollback failed at %s: %v", s.name, err), nil)
			h.ws.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{
				"sagaId": sg.ID, "error": err.Error(),
			}})
			return
		}

		sg.StepComplete(ctx, s.name, 0)
		h.ws.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step": s.name, "sagaId": sg.ID, "status": "complete",
		}})
	}

	deploy.Status = model.StatusDeployed
	h.db.UpdateDeployment(ctx, deploy.ID, deploy.Status)
	sg.Log(ctx, "deploy.complete", fmt.Sprintf("rollback complete: %s → %s", spec.App, imageTag), nil)
	h.ws.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{
		"sagaId": sg.ID, "imageTag": imageTag,
	}})
}
