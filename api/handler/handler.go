package handler

import (
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"norn/api/config"
	ncron "norn/api/cron"
	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/pipeline"
	"norn/api/secrets"
	"norn/api/store"
)

var validAppIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Handler struct {
	db               *store.DB
	kube             *k8s.Client
	ws               *hub.Hub
	cfg              *config.Config
	pipeline         *pipeline.Pipeline
	forgePipeline    *pipeline.ForgePipeline
	teardownPipeline *pipeline.TeardownPipeline
	secrets          *secrets.Manager
	scheduler        *ncron.Scheduler
}

func New(db *store.DB, kube *k8s.Client, ws *hub.Hub, cfg *config.Config, scheduler *ncron.Scheduler) *Handler {
	return &Handler{
		db:        db,
		kube:      kube,
		ws:        ws,
		cfg:       cfg,
		scheduler: scheduler,
		pipeline: &pipeline.Pipeline{
			DB:          db,
			Kube:        kube,
			WS:          ws,
			Scheduler:   scheduler,
			AppsDir:     cfg.AppsDir,
			GitToken:    cfg.GitToken,
			GitSSHKey:   cfg.GitSSHKey,
			RegistryURL: cfg.RegistryURL,
		},
		forgePipeline: &pipeline.ForgePipeline{
			DB:         db,
			Kube:       kube,
			WS:         ws,
			TunnelName: cfg.TunnelName,
			PGHost:     cfg.PGHost,
			PGUser:     cfg.PGUser,
		},
		teardownPipeline: &pipeline.TeardownPipeline{
			DB:         db,
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

// ValidateAppID is middleware that rejects requests with invalid app IDs.
func ValidateAppID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id != "" && !validAppIDRe.MatchString(id) {
			http.Error(w, "invalid app id", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
