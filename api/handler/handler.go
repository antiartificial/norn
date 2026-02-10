package handler

import (
	"norn/api/config"
	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/pipeline"
	"norn/api/secrets"
	"norn/api/store"
)

type Handler struct {
	db            *store.DB
	kube          *k8s.Client
	ws            *hub.Hub
	cfg           *config.Config
	pipeline      *pipeline.Pipeline
	forgePipeline *pipeline.ForgePipeline
	secrets       *secrets.Manager
}

func New(db *store.DB, kube *k8s.Client, ws *hub.Hub, cfg *config.Config) *Handler {
	return &Handler{
		db:   db,
		kube: kube,
		ws:   ws,
		cfg:  cfg,
		pipeline: &pipeline.Pipeline{
			DB:   db,
			Kube: kube,
			WS:   ws,
		},
		forgePipeline: &pipeline.ForgePipeline{
			Kube:       kube,
			WS:         ws,
			TunnelName: cfg.TunnelName,
		},
		secrets: secrets.NewManager(cfg.AppsDir),
	}
}

func (h *Handler) runPipeline(deploy *model.Deployment, spec *model.InfraSpec) {
	h.pipeline.Run(deploy, spec)
}
