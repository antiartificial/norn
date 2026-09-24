package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"norn/v2/api/beacon"
	"norn/v2/api/config"
	"norn/v2/api/consul"
	"norn/v2/api/githubapp"
	"norn/v2/api/hub"
	"norn/v2/api/logcollect"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/redpanda"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
)

var validAppIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const maxControlJSONBody = 64 << 10
const maxDocumentValidationJSONBody = 320 << 10
const maxValidationDocumentBytes = 64 << 10

type Handler struct {
	db                        *store.DB
	nomad                     *nomad.Client
	consul                    *consul.Client
	ws                        *hub.Hub
	cfg                       *config.Config
	pipeline                  *pipeline.Pipeline
	beacon                    *beacon.Service
	secrets                   *secrets.Manager
	sagaStore                 saga.Store
	s3                        *storage.Client
	redpanda                  *redpanda.Client
	access                    *AccessLog
	hostMetrics               *hostMetricsCache
	fleetGitHub               *githubapp.Client
	fleetGitHubConfigError    error
	productionGateMu          sync.Mutex
	productionGateAt          time.Time
	productionGateBlockers    []string
	evidenceReserveMu         sync.Mutex
	evidenceReserveAt         time.Time
	evidenceReserveStatus     store.EvidenceReserveStatus
	logSpool                  *logcollect.Spool
	auditPruneMu              sync.Mutex
	auditPruneAt              time.Time
	execConns                 sync.Map
	execOwnerOnce             sync.Once
	execOwnerID               string
	execLeaseDurationOverride time.Duration
	execWatchIntervalOverride time.Duration
	accessTokenLineage        accessTokenLineageResolver
	operationStore            store.OperationStore
	operationStoreError       error
}

func New(db *store.DB, n *nomad.Client, c *consul.Client, ws *hub.Hub, cfg *config.Config, p *pipeline.Pipeline, beaconSvc *beacon.Service, sec *secrets.Manager, ss saga.Store, s3 *storage.Client, rp *redpanda.Client) *Handler {
	h := &Handler{
		db:          db,
		nomad:       n,
		consul:      c,
		ws:          ws,
		cfg:         cfg,
		pipeline:    p,
		beacon:      beaconSvc,
		secrets:     sec,
		sagaStore:   ss,
		s3:          s3,
		redpanda:    rp,
		access:      NewAccessLog(defaultAccessLogLimit),
		hostMetrics: newHostMetricsCache(defaultHostMetricsSampler, time.Now, defaultHostMetricsSamplePeriod),
		execOwnerID: execRuntimeOwnerID(),
	}
	if db != nil && db.Pool != nil {
		h.accessTokenLineage = postgresAccessTokenLineageResolver{db: db}
		if cfg != nil {
			if signer, err := store.NewHMACAcceptanceSigner(cfg.AuditSigningKey, cfg.AuditPreviousSigningKeys...); err == nil {
				operationStore, storeErr := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{ExpectedAuthority: cfg.ControlAuthority, ReplayTTL: cfg.OperationReplayTTL})
				if storeErr != nil {
					h.operationStoreError = storeErr
				} else {
					h.operationStore = operationStore
				}
			}
		}
	}
	if cfg != nil && githubapp.Configured(fleetGitHubConfig(cfg)) {
		h.fleetGitHub, h.fleetGitHubConfigError = githubapp.New(fleetGitHubConfig(cfg), nil)
	}
	return h
}

// OperationStore exposes the single signed acceptance boundary constructed
// from this handler's audit-key and authority policy so pipeline producers use
// the same signer and replay namespace as HTTP handlers.
func (h *Handler) OperationStore() store.OperationStore {
	if h == nil {
		return nil
	}
	return h.operationStore
}

// OperationStoreError reports invalid operation-acceptance policy discovered
// while assembling the handler. Startup must check it before serving traffic.
func (h *Handler) OperationStoreError() error {
	if h == nil {
		return nil
	}
	return h.operationStoreError
}

func execRuntimeOwnerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), uuid.NewString())
}

func fleetGitHubConfig(cfg *config.Config) githubapp.Config {
	if cfg == nil {
		return githubapp.Config{}
	}
	return githubapp.Config{
		AppID: cfg.FleetGitHubAppID, InstallationID: cfg.FleetGitHubInstallationID,
		PrivateKeyFile: cfg.FleetGitHubPrivateKeyFile, Repository: cfg.FleetGitHubRepository,
		DefaultBranch: cfg.FleetGitHubDefaultBranch, ConfigPath: cfg.FleetGitHubConfigPath,
		PlanWorkflow: cfg.FleetGitHubPlanWorkflow, ApplyWorkflow: cfg.FleetGitHubApplyWorkflow,
		APIBaseURL: cfg.FleetGitHubAPIBaseURL, Production: cfg.Production(),
	}
}

// ValidateAppID is middleware that rejects requests with invalid app IDs.
func ValidateAppID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id != "" && !validAppIDRe.MatchString(id) {
			if strings.HasPrefix(r.URL.Path, "/api/v1/") {
				WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_id", "app ID is invalid")
			} else {
				http.Error(w, "invalid app id", http.StatusBadRequest)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func preventSensitiveResponseCaching(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func decodeControlJSON(w http.ResponseWriter, r *http.Request, target interface{}) error {
	return decodeControlJSONLimit(w, r, target, maxControlJSONBody)
}

func decodeControlJSONLimit(w http.ResponseWriter, r *http.Request, target interface{}, limit int64) error {
	if r.Body == nil {
		return io.EOF
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	controller := http.NewResponseController(w)
	if controller.SetReadDeadline(time.Now().Add(15*time.Second)) == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON request: multiple values")
		}
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Error     string `json:"error,omitempty"`
}

func WriteControlProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	problem := Problem{
		Type: "https://norn.dev/problems/" + code, Title: http.StatusText(status),
		Status: status, Code: code, Detail: detail, Error: detail,
	}
	if r != nil {
		problem.RequestID = middleware.GetReqID(r.Context())
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func requireControlScope(w http.ResponseWriter, r *http.Request, scope string) (AccessPrincipal, bool) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated control principal is required")
		return AccessPrincipal{}, false
	}
	if !principal.Allows(scope) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "token lacks required scope "+scope)
		return AccessPrincipal{}, false
	}
	return principal, true
}
