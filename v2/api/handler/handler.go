package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"norn/v2/api/beacon"
	"norn/v2/api/config"
	"norn/v2/api/connector"
	"norn/v2/api/consul"
	"norn/v2/api/engine"
	"norn/v2/api/githubapp"
	"norn/v2/api/hub"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/privateattestation"
	"norn/v2/api/redpanda"
	containerruntime "norn/v2/api/runtime"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
)

var validAppIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const maxControlJSONBody = 64 << 10
const maxDocumentValidationJSONBody = 320 << 10
const maxValidationDocumentBytes = 64 << 10
const maxPrivateAttestationJSONBody = 3 << 20

// A 2 MiB SPDX document is base64-wrapped once in its DSSE envelope and again
// in the signed qualification payload. Keep bounded headroom for that portable
// receipt without relying on process argument limits in CI.
const maxReleaseEvidenceJSONBody = 12 << 20

type Handler struct {
	db                     *store.DB
	nomad                  *nomad.Client
	consul                 *consul.Client
	workloads              connector.Connector
	localEngine            *engine.Engine
	containerRuntime       *containerruntime.Runtime
	ws                     *hub.Hub
	cfg                    *config.Config
	pipeline               *pipeline.Pipeline
	beacon                 *beacon.Service
	secrets                *secrets.Manager
	sagaStore              saga.Store
	s3                     *storage.Client
	redpanda               *redpanda.Client
	access                 *AccessLog
	hostMetrics            *hostMetricsCache
	fleetGitHub            *githubapp.Client
	fleetGitHubConfigError error
	githubJWKS             *githubJWKCache
	privateReleaseSigner   privateattestation.Signer
	productionGateMu       sync.Mutex
	productionGateAt       time.Time
	productionGateBlockers []string
	auditPruneMu           sync.Mutex
	auditPruneAt           time.Time
	wakeLocks              sync.Map
	execConns              sync.Map
}

// ConfigurePrivateReleaseSigner installs the staging-only signer after startup
// configuration has selected norn-signed-private. Production receives only the
// corresponding public-key verifier through Pipeline.
func (h *Handler) ConfigurePrivateReleaseSigner(signer privateattestation.Signer) {
	h.privateReleaseSigner = signer
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
		githubJWKS:  newGitHubJWKCache(),
		workloads:   connector.NewNomadConsul(n, c),
	}
	if cfg != nil && githubapp.Configured(fleetGitHubConfig(cfg)) {
		h.fleetGitHub, h.fleetGitHubConfigError = githubapp.New(fleetGitHubConfig(cfg), nil)
	}
	return h
}

// ConfigureWorkloads installs the explicit workload connector selected at
// startup. Keeping this separate from New preserves the embedded/test API and
// makes accidental backend auto-detection impossible.
func (h *Handler) ConfigureWorkloads(workloads connector.Connector, eng *engine.Engine, runtime *containerruntime.Runtime) {
	if workloads != nil {
		h.workloads = workloads
	}
	h.localEngine = eng
	h.containerRuntime = runtime
}

func (h *Handler) usesNomadConnector() bool {
	if h.workloads != nil {
		return h.workloads.Name() == connector.NomadConsul
	}
	// Compatibility for focused handler tests and embedded callers. The server
	// always injects an explicit connector before serving requests.
	return h.nomad != nil
}

func (h *Handler) requireNomadConnector(w http.ResponseWriter) bool {
	if !h.usesNomadConnector() {
		writeError(w, http.StatusNotImplemented, "operation requires the nomad-consul workload connector")
		return false
	}
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return false
	}
	return true
}

func fleetGitHubConfig(cfg *config.Config) githubapp.Config {
	if cfg == nil {
		return githubapp.Config{}
	}
	return githubapp.Config{
		AppID: cfg.FleetGitHubAppID, InstallationID: cfg.FleetGitHubInstallationID,
		PrivateKeyFile: cfg.FleetGitHubPrivateKeyFile, Repository: cfg.FleetGitHubRepository,
		Environment:   cfg.FleetGitHubEnvironment,
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

// requireFleetOperateScope accepts the dedicated least-privilege runner scope.
// api:write remains a temporary compatibility superset for already-issued
// automation tokens; newly enrolled infrastructure runners should request only
// fleet:operate plus api:read when inventory reads are required.
func requireFleetOperateScope(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated fleet runner principal is required")
		return AccessPrincipal{}, false
	}
	if !principal.Allows(ScopeFleetOperate) && !principal.Allows(ScopeAPIWrite) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "token lacks required scope "+ScopeFleetOperate)
		return AccessPrincipal{}, false
	}
	return principal, true
}
