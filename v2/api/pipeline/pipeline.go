package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/store"
)

type Pipeline struct {
	DB          *store.DB
	Nomad       *nomad.Client
	WS          *hub.Hub
	SagaStore   saga.Store
	Secrets     *secrets.Manager
	AppsDir     string
	GitToken    string
	GitSSHKey   string
	RegistryURL string
}

type state struct {
	spec      *model.InfraSpec
	workDir   string
	commitSHA string
	imageTag  string
}

type step struct {
	name string
	fn   func(ctx context.Context, s *state, sg *saga.Saga) error
}

// Run executes the full deploy pipeline for an app.
// Returns the saga ID for event tracking.
func (p *Pipeline) Run(spec *model.InfraSpec, ref string) string {
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "deploy")
	ctx := context.Background()

	deploy := &model.Deployment{
		ID:        uuid.New().String(),
		App:       spec.App,
		CommitSHA: ref,
		SagaID:    sg.ID,
		Status:    model.StatusQueued,
		StartedAt: time.Now(),
	}
	if err := p.DB.InsertDeployment(ctx, deploy); err != nil {
		log.Printf("pipeline: insert deployment: %v", err)
	}

	sg.Log(ctx, "deploy.start", fmt.Sprintf("deploying %s (ref: %s)", spec.App, ref), nil)

	go p.run(ctx, spec, deploy, sg)
	return sg.ID
}

func (p *Pipeline) run(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga) {
	st := &state{
		spec:      spec,
		commitSHA: deploy.CommitSHA,
	}

	steps := []step{
		{name: "clone", fn: p.clone},
		{name: "build", fn: p.build},
		{name: "test", fn: p.test},
		{name: "snapshot", fn: p.snapshot},
		{name: "migrate", fn: p.migrate},
		{name: "submit", fn: p.submit},
		{name: "healthy", fn: p.healthy},
		{name: "forge", fn: p.forge},
		{name: "cleanup", fn: p.cleanup},
	}

	for _, s := range steps {
		sg.StepStart(ctx, s.name)
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"sagaId": sg.ID,
			"status": "running",
		}})

		start := time.Now()
		err := s.fn(ctx, st, sg)
		elapsed := time.Since(start).Milliseconds()

		if err != nil {
			sg.StepFailed(ctx, s.name, err)
			p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
				"step":   s.name,
				"sagaId": sg.ID,
				"status": "failed",
			}})
			deploy.Status = model.StatusFailed
			p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status)
			sg.Log(ctx, "deploy.failed", fmt.Sprintf("deploy failed at %s: %v", s.name, err), nil)
			p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{
				"sagaId": sg.ID,
				"error":  err.Error(),
			}})
			return
		}

		sg.StepComplete(ctx, s.name, elapsed)
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"sagaId": sg.ID,
			"status": "complete",
		}})
	}

	deploy.CommitSHA = st.commitSHA
	deploy.ImageTag = st.imageTag
	deploy.Status = model.StatusDeployed
	p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status)
	sg.Log(ctx, "deploy.complete", fmt.Sprintf("deploy complete: %s → %s", spec.App, st.imageTag), nil)
	p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{
		"sagaId":   sg.ID,
		"imageTag": st.imageTag,
	}})
}
