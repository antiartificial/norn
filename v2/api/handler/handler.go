package handler

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/consul"
	"norn/v2/api/hub"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
)

var validAppIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Handler struct {
	db        *store.DB
	nomad     *nomad.Client
	consul    *consul.Client
	ws        *hub.Hub
	cfg       *config.Config
	pipeline  *pipeline.Pipeline
	secrets   *secrets.Manager
	sagaStore saga.Store
	s3        *storage.Client
}

func New(db *store.DB, n *nomad.Client, c *consul.Client, ws *hub.Hub, cfg *config.Config, p *pipeline.Pipeline, sec *secrets.Manager, ss saga.Store, s3 *storage.Client) *Handler {
	return &Handler{
		db:        db,
		nomad:     n,
		consul:    c,
		ws:        ws,
		cfg:       cfg,
		pipeline:  p,
		secrets:   sec,
		sagaStore: ss,
		s3:        s3,
	}
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
