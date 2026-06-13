package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   firstNonEmpty(os.Getenv("NORN_TOKEN"), os.Getenv("NORN_API_TOKEN")),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// App types matching API responses

type AppStatus struct {
	Spec        *InfraSpec   `json:"spec"`
	NomadStatus string       `json:"nomadStatus"`
	Healthy     bool         `json:"healthy"`
	Allocations []Allocation `json:"allocations"`
}

type Endpoint struct {
	URL    string `json:"url"`
	Region string `json:"region,omitempty"`
}

type ServiceManifest struct {
	Version     int                     `json:"version"`
	GeneratedAt string                  `json:"generatedAt"`
	NetworkMode string                  `json:"networkMode,omitempty"`
	Contract    ServiceManifestContract `json:"contract"`
	Services    []ServiceManifestEntry  `json:"services"`
}

type ServiceManifestEntry struct {
	Name         string              `json:"name"`
	App          string              `json:"app"`
	Process      string              `json:"process"`
	Type         string              `json:"type"`
	Status       string              `json:"status"`
	HealthPath   string              `json:"healthPath,omitempty"`
	Metrics      *ServiceMetrics     `json:"metrics,omitempty"`
	Reachability ServiceReachability `json:"reachability"`
	Endpoints    []Endpoint          `json:"endpoints,omitempty"`
	Instances    []ServiceInstance   `json:"instances,omitempty"`
	Metadata     map[string]string   `json:"metadata,omitempty"`
}

type ServiceMetrics struct {
	Enabled      bool                `json:"enabled"`
	Path         string              `json:"path"`
	ServiceName  string              `json:"serviceName,omitempty"`
	Reachability ServiceReachability `json:"reachability"`
	Instances    []ServiceInstance   `json:"instances,omitempty"`
}

type ServiceManifestContract struct {
	Schema             string   `json:"schema"`
	ProcessTypes       []string `json:"processTypes"`
	ReachabilityScopes []string `json:"reachabilityScopes"`
}

type ServiceReachability struct {
	EndpointScope string `json:"endpointScope"`
	InstanceScope string `json:"instanceScope"`
	Exposure      string `json:"exposure"`
	Routable      bool   `json:"routable"`
}

type ServiceInstance struct {
	ID      string `json:"id,omitempty"`
	Node    string `json:"node,omitempty"`
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	Status  string `json:"status,omitempty"`
}

type InfraSpec struct {
	App            string             `json:"name"`
	Processes      map[string]Process `json:"processes"`
	Repo           *RepoSpec          `json:"repo,omitempty"`
	Endpoints      []Endpoint         `json:"endpoints,omitempty"`
	Infrastructure *Infrastructure    `json:"infrastructure,omitempty"`
}

type Process struct {
	Port     int    `json:"port,omitempty"`
	Command  string `json:"command,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	Metrics  *struct {
		Enabled bool   `json:"enabled,omitempty"`
		Path    string `json:"path,omitempty"`
		Port    int    `json:"port,omitempty"`
	} `json:"metrics,omitempty"`
}

type RepoSpec struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
}

type Infrastructure struct {
	Postgres      *PostgresInfra      `json:"postgres,omitempty"`
	Kafka         *KafkaInfra         `json:"kafka,omitempty"`
	Redis         *RedisInfra         `json:"redis,omitempty"`
	NATS          *NATSInfra          `json:"nats,omitempty"`
	ObjectStorage *ObjectStorageInfra `json:"objectStorage,omitempty"`
}

type PostgresInfra struct {
	Database string `json:"database"`
}

type KafkaInfra struct {
	Topics []string `json:"topics,omitempty"`
}

type RedisInfra struct {
	Namespace string `json:"namespace,omitempty"`
}

type NATSInfra struct {
	Streams []string `json:"streams,omitempty"`
}

type ObjectStorageInfra struct {
	Provider string                `json:"provider,omitempty"`
	Buckets  []ObjectStorageBucket `json:"buckets,omitempty"`
}

type ObjectStorageBucket struct {
	Name   string `json:"name"`
	Access string `json:"access,omitempty"`
	Public bool   `json:"public,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Env    string `json:"env,omitempty"`
}

type Allocation struct {
	ID        string `json:"id"`
	TaskGroup string `json:"taskGroup"`
	Status    string `json:"status"`
	Healthy   *bool  `json:"healthy,omitempty"`
	NodeID    string `json:"nodeId,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

type HealthStatus struct {
	Status   string            `json:"status"`
	Services map[string]string `json:"services"`
	Network  NetworkStatus     `json:"network,omitempty"`
}

type SecretStatus struct {
	App                 string   `json:"app"`
	Declared            []string `json:"declared"`
	Encrypted           []string `json:"encrypted"`
	MissingEncrypted    []string `json:"missingEncrypted"`
	EncryptedUndeclared []string `json:"encryptedUndeclared"`
	PlainEnvWarnings    []string `json:"plainEnvWarnings"`
	OK                  bool     `json:"ok"`
}

type NetworkStatus struct {
	Mode       string `json:"mode,omitempty"`
	BindAddr   string `json:"bindAddr,omitempty"`
	NomadAddr  string `json:"nomadAddr,omitempty"`
	ConsulAddr string `json:"consulAddr,omitempty"`
}

type Deployment struct {
	ID            string   `json:"id"`
	App           string   `json:"app"`
	CommitSHA     string   `json:"commitSha"`
	ImageTag      string   `json:"imageTag"`
	SagaID        string   `json:"sagaId"`
	Status        string   `json:"status"`
	SourceKind    string   `json:"sourceKind,omitempty"`
	SourceRef     string   `json:"sourceRef,omitempty"`
	SourceDirty   bool     `json:"sourceDirty,omitempty"`
	SourceChanges []string `json:"sourceChanges,omitempty"`
	StartedAt     string   `json:"startedAt"`
}

type DeploymentStep struct {
	DeploymentID string                 `json:"deploymentId"`
	App          string                 `json:"app"`
	SagaID       string                 `json:"sagaId"`
	Step         string                 `json:"step"`
	Status       string                 `json:"status"`
	Attempt      int                    `json:"attempt,omitempty"`
	StartedAt    string                 `json:"startedAt"`
	FinishedAt   string                 `json:"finishedAt,omitempty"`
	DurationMs   int64                  `json:"durationMs,omitempty"`
	Message      string                 `json:"message,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type SagaEvent struct {
	ID        string            `json:"id"`
	SagaID    string            `json:"sagaId"`
	Timestamp string            `json:"timestamp"`
	Source    string            `json:"source"`
	App       string            `json:"app"`
	Category  string            `json:"category"`
	Action    string            `json:"action"`
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type ValidationResult struct {
	App      string              `json:"app"`
	Valid    bool                `json:"valid"`
	Findings []ValidationFinding `json:"findings"`
}

type ValidationFinding struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

type StatsResponse struct {
	Deploys struct {
		Total          int    `json:"total"`
		Success        int    `json:"success"`
		Failed         int    `json:"failed"`
		MostPopularApp string `json:"mostPopularApp,omitempty"`
		MostPopularN   int    `json:"mostPopularN,omitempty"`
	} `json:"deploys"`
	AppCount          int           `json:"appCount"`
	TotalAllocs       int           `json:"totalAllocs"`
	RunningAllocs     int           `json:"runningAllocs"`
	UptimeLeaderboard []UptimeEntry `json:"uptimeLeaderboard"`
}

type UptimeEntry struct {
	AllocID   string `json:"allocId"`
	JobID     string `json:"jobId"`
	TaskGroup string `json:"taskGroup"`
	Uptime    string `json:"uptime"`
	NodeName  string `json:"nodeName"`
	StartedAt string `json:"startedAt"`
}

type Snapshot struct {
	Filename  string `json:"filename"`
	Database  string `json:"database"`
	CommitSHA string `json:"commitSha,omitempty"`
	Timestamp string `json:"timestamp"`
	CreatedAt string `json:"createdAt,omitempty"`
	Size      int64  `json:"size"`
}

type RestoreReceipt struct {
	Status     string   `json:"status"`
	App        string   `json:"app"`
	Database   string   `json:"database"`
	Snapshot   Snapshot `json:"snapshot"`
	RestoredAt string   `json:"restoredAt"`
}

type SnapshotRetentionReceipt struct {
	Status     string     `json:"status"`
	App        string     `json:"app"`
	Keep       int        `json:"keep"`
	DryRun     bool       `json:"dryRun"`
	Kept       []Snapshot `json:"kept"`
	Pruned     []Snapshot `json:"pruned"`
	WouldPrune []Snapshot `json:"wouldPrune,omitempty"`
	AppliedAt  string     `json:"appliedAt"`
}

type AccessEvent struct {
	Timestamp  string `json:"timestamp"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"durationMs"`
	ClientIP   string `json:"clientIp,omitempty"`
	Forwarded  string `json:"forwarded,omitempty"`
	CFIP       string `json:"cfConnectingIp,omitempty"`
	CFEmail    string `json:"cfAccessEmail,omitempty"`
	UserAgent  string `json:"userAgent,omitempty"`
}

type BeaconEvent struct {
	ID          string                 `json:"id"`
	Source      string                 `json:"source"`
	App         string                 `json:"app,omitempty"`
	Environment string                 `json:"environment,omitempty"`
	Type        string                 `json:"type"`
	Severity    string                 `json:"severity"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	DedupeKey   string                 `json:"dedupeKey"`
	OccurredAt  string                 `json:"occurredAt"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type CronState struct {
	App      string    `json:"app"`
	Process  string    `json:"process"`
	Paused   bool      `json:"paused"`
	Schedule string    `json:"schedule"`
	Runs     []CronRun `json:"runs,omitempty"`
}

type CronRun struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
}

type FuncExecution struct {
	ID         string `json:"id"`
	App        string `json:"app"`
	Process    string `json:"process"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type ContextDBOpsSummary struct {
	GeneratedAt    string                      `json:"generatedAt"`
	App            *AppStatus                  `json:"app,omitempty"`
	Services       []ServiceManifestEntry      `json:"services"`
	WebURL         string                      `json:"webUrl,omitempty"`
	WorkerURL      string                      `json:"workerUrl,omitempty"`
	Worker         *ContextDBWorkerStatus      `json:"worker,omitempty"`
	ProviderGate   ContextDBProviderGate       `json:"providerGate"`
	Queue          ContextDBReviewQueue        `json:"queue"`
	WorkerRuns     []ContextDBWorkerRun        `json:"workerRuns"`
	FeedbackEvents []ContextDBFeedbackEvent    `json:"feedbackEvents"`
	Rollbacks      []ContextDBFeedbackRollback `json:"rollbacks"`
	Snapshots      []Snapshot                  `json:"snapshots"`
	Deployments    []Deployment                `json:"deployments"`
	Secrets        *SecretStatus               `json:"secrets,omitempty"`
	AccessEvents   []AccessEvent               `json:"accessEvents"`
	Warnings       []string                    `json:"warnings,omitempty"`
}

type ContextDBProviderGate struct {
	Ready               bool   `json:"ready"`
	Reason              string `json:"reason,omitempty"`
	ProviderBacked      int    `json:"providerBacked"`
	MutationEnabled     int    `json:"mutationEnabled"`
	MissingProviderKeys int    `json:"missingProviderKeys"`
	Warnings            int    `json:"warnings"`
	Errors              int    `json:"errors"`
}

type ContextDBReviewQueue struct {
	Items []ContextDBReviewItem `json:"items"`
	Total int                   `json:"total"`
	Error string                `json:"error,omitempty"`
}

type ContextDBReviewItem struct {
	ReviewID string `json:"review_id"`
	NodeID   string `json:"node_id"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Owner    string `json:"owner"`
	Reason   string `json:"reason"`
}

type ContextDBWorkerStatus struct {
	Status string                `json:"status"`
	Worker string                `json:"worker"`
	DryRun bool                  `json:"dry_run"`
	Policy ContextDBPolicyReport `json:"policy"`
}

type ContextDBPolicyReport struct {
	GeneratedAt string                     `json:"generated_at"`
	DryRun      bool                       `json:"dry_run"`
	Namespaces  []ContextDBNamespacePolicy `json:"namespaces"`
	Totals      ContextDBPolicyTotals      `json:"totals"`
}

type ContextDBNamespacePolicy struct {
	Namespace             string   `json:"namespace"`
	Mode                  string   `json:"mode"`
	PolicyPreset          string   `json:"policy_preset"`
	DryRun                bool     `json:"dry_run"`
	Evaluator             string   `json:"evaluator"`
	Provider              string   `json:"provider"`
	ProviderKeyRequired   bool     `json:"provider_key_required"`
	ProviderKeyConfigured bool     `json:"provider_key_configured"`
	AllowedActions        []string `json:"allowed_actions"`
	MutationAllowed       bool     `json:"mutation_allowed"`
	Warnings              []string `json:"warnings"`
	OK                    bool     `json:"ok"`
	Error                 string   `json:"error"`
}

type ContextDBPolicyTotals struct {
	Namespaces          int `json:"namespaces"`
	MutationEnabled     int `json:"mutation_enabled"`
	ProviderBacked      int `json:"provider_backed"`
	MissingProviderKeys int `json:"missing_provider_keys"`
	Warnings            int `json:"warnings"`
	Errors              int `json:"errors"`
}

type ContextDBWorkerRun struct {
	CycleID     string                       `json:"cycle_id"`
	Namespace   string                       `json:"namespace"`
	Mode        string                       `json:"mode"`
	Evaluator   string                       `json:"evaluator"`
	DryRun      bool                         `json:"dry_run"`
	Scanned     int                          `json:"scanned"`
	Applied     int                          `json:"applied"`
	Skipped     int                          `json:"skipped"`
	Errors      int                          `json:"errors"`
	GeneratedAt string                       `json:"generated_at"`
	Decisions   []ContextDBWorkerRunDecision `json:"decisions,omitempty"`
}

type ContextDBWorkerRunDecision struct {
	ReviewID              string `json:"review_id"`
	NodeID                string `json:"node_id"`
	Type                  string `json:"type"`
	Action                string `json:"action"`
	Applied               bool   `json:"applied"`
	Reason                string `json:"reason"`
	FeedbackEventID       string `json:"feedback_event_id,omitempty"`
	ReviewDecisionEventID string `json:"review_decision_event_id,omitempty"`
}

type ContextDBFeedbackEvent struct {
	EventID    string  `json:"event_id"`
	Namespace  string  `json:"namespace"`
	NodeID     string  `json:"node_id"`
	Action     string  `json:"action"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
	TxTime     string  `json:"tx_time"`
}

type ContextDBFeedbackRollback struct {
	EventID            string  `json:"event_id"`
	RolledBackEventID  string  `json:"rolled_back_event_id"`
	NodeID             string  `json:"node_id"`
	Action             string  `json:"action"`
	PreviousConfidence float64 `json:"previous_confidence"`
	RestoredConfidence float64 `json:"restored_confidence"`
	Reason             string  `json:"reason"`
	Owner              string  `json:"owner"`
	TxTime             string  `json:"tx_time"`
}

type PlatformOpsSummary struct {
	GeneratedAt   string                   `json:"generatedAt"`
	NetworkMode   string                   `json:"networkMode,omitempty"`
	Services      PlatformServiceSummary   `json:"services"`
	Deployments   PlatformDeploySummary    `json:"deployments"`
	Operations    PlatformOperationSummary `json:"operations"`
	Secrets       PlatformSecretSummary    `json:"secrets"`
	Snapshots     []PlatformSnapshotStatus `json:"snapshots"`
	Access        PlatformAccessSummary    `json:"access"`
	Observability PlatformObserveSummary   `json:"observability"`
	Warnings      []string                 `json:"warnings,omitempty"`
}

type Operation struct {
	ID            string                 `json:"id"`
	Kind          string                 `json:"kind"`
	App           string                 `json:"app,omitempty"`
	SagaID        string                 `json:"sagaId,omitempty"`
	Ref           string                 `json:"ref,omitempty"`
	Status        string                 `json:"status"`
	Risk          string                 `json:"risk,omitempty"`
	Source        string                 `json:"source,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Payload       map[string]interface{} `json:"payload,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Attempts      int                    `json:"attempts,omitempty"`
	MaxAttempts   int                    `json:"maxAttempts,omitempty"`
	LockedBy      string                 `json:"lockedBy,omitempty"`
	LockedUntil   string                 `json:"lockedUntil,omitempty"`
	NextAttemptAt string                 `json:"nextAttemptAt,omitempty"`
	LastError     string                 `json:"lastError,omitempty"`
	StartedAt     string                 `json:"startedAt"`
	UpdatedAt     string                 `json:"updatedAt"`
	FinishedAt    string                 `json:"finishedAt,omitempty"`
}

type PlatformOperationSummary struct {
	Recent   []Operation    `json:"recent"`
	Active   []Operation    `json:"active"`
	ByKind   map[string]int `json:"byKind"`
	ByStatus map[string]int `json:"byStatus"`
}

type WebhookDelivery struct {
	ID         string                 `json:"id"`
	Provider   string                 `json:"provider"`
	Event      string                 `json:"event,omitempty"`
	DeliveryID string                 `json:"deliveryId,omitempty"`
	Repository string                 `json:"repository,omitempty"`
	Ref        string                 `json:"ref,omitempty"`
	Branch     string                 `json:"branch,omitempty"`
	App        string                 `json:"app,omitempty"`
	SagaID     string                 `json:"sagaId,omitempty"`
	Status     string                 `json:"status"`
	Reason     string                 `json:"reason,omitempty"`
	RemoteAddr string                 `json:"remoteAddr,omitempty"`
	UserAgent  string                 `json:"userAgent,omitempty"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	ReceivedAt string                 `json:"receivedAt"`
	UpdatedAt  string                 `json:"updatedAt"`
}

type WebhookReplayResponse struct {
	SagaID string `json:"sagaId"`
	App    string `json:"app"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
}

type PlatformReleaseList struct {
	Current  string            `json:"current,omitempty"`
	Releases []PlatformRelease `json:"releases"`
}

type PlatformRelease struct {
	SHA       string `json:"sha"`
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
	Path      string `json:"path"`
	Current   bool   `json:"current"`
}

type PlatformServiceSummary struct {
	Total    int            `json:"total"`
	ByType   map[string]int `json:"byType"`
	ByStatus map[string]int `json:"byStatus"`
	Public   int            `json:"public"`
	Private  int            `json:"private"`
	Local    int            `json:"local"`
	Internal int            `json:"internal"`
}

type PlatformDeploySummary struct {
	Recent     []Deployment `json:"recent"`
	Dirty      []Deployment `json:"dirty"`
	Failed     int          `json:"failed"`
	Successful int          `json:"successful"`
}

type PlatformSecretSummary struct {
	OK             int            `json:"ok"`
	NeedsAttention int            `json:"needsAttention"`
	Apps           []SecretStatus `json:"apps"`
}

type PlatformSnapshotStatus struct {
	App       string    `json:"app"`
	Database  string    `json:"database,omitempty"`
	Keep      int       `json:"keep"`
	Count     int       `json:"count"`
	OverLimit int       `json:"overLimit"`
	Latest    *Snapshot `json:"latest,omitempty"`
}

type PlatformAccessSummary struct {
	Recent      []AccessEvent  `json:"recent"`
	TotalRecent int            `json:"totalRecent"`
	ByStatus    map[string]int `json:"byStatus"`
	ByClientIP  map[string]int `json:"byClientIp"`
}

type PlatformObserveSummary struct {
	Enabled      bool   `json:"enabled"`
	LogsEnabled  bool   `json:"logsEnabled"`
	LogFormat    string `json:"logFormat"`
	ServiceName  string `json:"serviceName,omitempty"`
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
}

// API methods

func (c *Client) Health() (*HealthStatus, error) {
	var h HealthStatus
	if err := c.get("/api/health", &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Client) ListApps() ([]AppStatus, error) {
	var apps []AppStatus
	if err := c.get("/api/apps", &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func (c *Client) ServiceManifest() (*ServiceManifest, error) {
	var manifest ServiceManifest
	if err := c.get("/api/services/manifest", &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (c *Client) ContextDBOps(namespace, mode string, limit int) (*ContextDBOpsSummary, error) {
	values := url.Values{}
	if namespace != "" {
		values.Set("namespace", namespace)
	}
	if mode != "" {
		values.Set("mode", mode)
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/api/ops/contextdb"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var summary ContextDBOpsSummary
	if err := c.get(path, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func (c *Client) PlatformOps() (*PlatformOpsSummary, error) {
	var summary PlatformOpsSummary
	if err := c.get("/api/ops/platform", &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func (c *Client) ListOperations(active bool, limit int) ([]Operation, error) {
	values := url.Values{}
	if active {
		values.Set("active", "true")
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/api/operations"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Operations []Operation `json:"operations"`
	}
	if err := c.get(path, &resp); err != nil {
		return nil, err
	}
	return resp.Operations, nil
}

func (c *Client) ListEvents(app, eventType, severity string, limit int) ([]BeaconEvent, int, error) {
	values := url.Values{}
	if app != "" {
		values.Set("app", app)
	}
	if eventType != "" {
		values.Set("type", eventType)
	}
	if severity != "" {
		values.Set("severity", severity)
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/api/events"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Events []BeaconEvent `json:"events"`
		Total  int           `json:"total"`
	}
	if err := c.get(path, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Events, resp.Total, nil
}

func (c *Client) PlatformReleases() (*PlatformReleaseList, error) {
	var releases PlatformReleaseList
	if err := c.get("/api/platform/releases", &releases); err != nil {
		return nil, err
	}
	return &releases, nil
}

func (c *Client) DeploymentSteps(deploymentID string) ([]DeploymentStep, error) {
	var resp struct {
		Steps []DeploymentStep `json:"steps"`
	}
	if err := c.get("/api/deployments/"+url.PathEscape(deploymentID)+"/steps", &resp); err != nil {
		return nil, err
	}
	return resp.Steps, nil
}

func (c *Client) ListWebhookDeliveries(limit int) ([]WebhookDelivery, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/api/webhooks/deliveries"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Deliveries []WebhookDelivery `json:"deliveries"`
	}
	if err := c.get(path, &resp); err != nil {
		return nil, err
	}
	return resp.Deliveries, nil
}

func (c *Client) ReplayWebhookDelivery(id, mode string) (*WebhookReplayResponse, error) {
	body := fmt.Sprintf(`{"mode":%q}`, mode)
	var resp WebhookReplayResponse
	if err := c.postJSON("/api/webhooks/deliveries/"+url.PathEscape(id)+"/replay", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ContextDBRollbackFeedback(eventID, namespace, mode, reason, owner string) (*ContextDBFeedbackRollback, error) {
	values := url.Values{}
	if namespace != "" {
		values.Set("namespace", namespace)
	}
	if mode != "" {
		values.Set("mode", mode)
	}
	path := "/api/ops/contextdb/feedback/" + url.PathEscape(eventID) + "/rollback"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, _ := json.Marshal(map[string]string{"reason": reason, "owner": owner})
	var receipt ContextDBFeedbackRollback
	if err := c.postJSON(path, string(body), &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (c *Client) GetApp(id string) (*AppStatus, error) {
	var app AppStatus
	if err := c.get("/api/apps/"+id, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) Deploy(appID, ref string) (string, error) {
	body := fmt.Sprintf(`{"ref":%q}`, ref)
	var resp struct {
		SagaID string `json:"sagaId"`
	}
	if err := c.postJSON("/api/apps/"+appID+"/deploy", body, &resp); err != nil {
		return "", err
	}
	return resp.SagaID, nil
}

func (c *Client) Preflight(appID, ref string) (string, error) {
	body := fmt.Sprintf(`{"ref":%q}`, ref)
	var resp struct {
		SagaID string `json:"sagaId"`
	}
	if err := c.postJSON("/api/apps/"+appID+"/preflight", body, &resp); err != nil {
		return "", err
	}
	return resp.SagaID, nil
}

func (c *Client) Rollback(appID string) (string, error) {
	var resp struct {
		SagaID string `json:"sagaId"`
	}
	if err := c.postJSON("/api/apps/"+appID+"/rollback", "{}", &resp); err != nil {
		return "", err
	}
	return resp.SagaID, nil
}

func (c *Client) Restart(appID string) error {
	return c.post("/api/apps/"+appID+"/restart", "{}")
}

func (c *Client) StreamLogs(appID string) (io.ReadCloser, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/apps/"+appID+"/logs?follow=true", nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *Client) Exec(appID, process string, argv []string) (*websocket.Conn, error) {
	params := url.Values{}
	if process != "" {
		params.Set("process", process)
	}
	if len(argv) > 0 {
		encoded, err := json.Marshal(argv)
		if err != nil {
			return nil, err
		}
		params.Set("argv", string(encoded))
	}
	wsURL := c.WebSocketURLFor("/api/apps/" + appID + "/exec")
	if encoded := params.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("exec websocket: %w", err)
	}
	return conn, nil
}

func (c *Client) GetSagaEvents(sagaID string) ([]SagaEvent, error) {
	var events []SagaEvent
	if err := c.get("/api/saga/"+sagaID, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *Client) ListRecentSaga(app string, limit int) ([]SagaEvent, error) {
	path := fmt.Sprintf("/api/saga?limit=%d", limit)
	if app != "" {
		path += "&app=" + app
	}
	var events []SagaEvent
	if err := c.get(path, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *Client) ListDeployments(app string) ([]Deployment, error) {
	path := "/api/deployments"
	if app != "" {
		path += "?app=" + app
	}
	var deps []Deployment
	if err := c.get(path, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

func (c *Client) ListSecrets(appID string) ([]string, error) {
	var secrets []string
	if err := c.get("/api/apps/"+appID+"/secrets", &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

func (c *Client) SecretsStatusAll() ([]SecretStatus, error) {
	var statuses []SecretStatus
	if err := c.get("/api/secrets/status", &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (c *Client) SecretsStatusApp(appID string) (*SecretStatus, error) {
	var status SecretStatus
	if err := c.get("/api/apps/"+appID+"/secrets/status", &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) Forge(appID string) error {
	return c.post("/api/apps/"+appID+"/forge", "{}")
}

func (c *Client) Teardown(appID string) error {
	return c.post("/api/apps/"+appID+"/teardown", "{}")
}

func (c *Client) CloudflaredIngress() ([]string, error) {
	var resp struct {
		Hostnames []string `json:"hostnames"`
	}
	if err := c.get("/api/cloudflared/ingress", &resp); err != nil {
		return nil, err
	}
	return resp.Hostnames, nil
}

func (c *Client) ToggleEndpoint(appID, hostname string, enabled bool) error {
	body := fmt.Sprintf(`{"hostname":%q,"enabled":%t}`, hostname, enabled)
	return c.post("/api/apps/"+appID+"/endpoints/toggle", body)
}

func (c *Client) ValidateAll() ([]ValidationResult, error) {
	var results []ValidationResult
	if err := c.get("/api/validate", &results); err != nil {
		return nil, err
	}
	return results, nil
}

func (c *Client) ValidateApp(appID string) (*ValidationResult, error) {
	var result ValidationResult
	if err := c.get("/api/validate/"+appID, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) UpdateSecrets(appID string, secrets map[string]string) error {
	body, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	return c.put("/api/apps/"+appID+"/secrets", string(body))
}

func (c *Client) DeleteSecret(appID, key string) error {
	return c.del("/api/apps/" + appID + "/secrets/" + key)
}

func (c *Client) Scale(appID, group string, count int) error {
	body := fmt.Sprintf(`{"group":%q,"count":%d}`, group, count)
	return c.post("/api/apps/"+appID+"/scale", body)
}

func (c *Client) Stats() (*StatsResponse, error) {
	var s StatsResponse
	if err := c.get("/api/stats", &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) ListSnapshots(appID string) ([]Snapshot, error) {
	var snaps []Snapshot
	if err := c.get("/api/apps/"+appID+"/snapshots", &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

func (c *Client) RestoreSnapshot(appID, ts string, confirm bool) (*RestoreReceipt, error) {
	var receipt RestoreReceipt
	path := "/api/apps/" + appID + "/snapshots/" + ts + "/restore"
	if confirm {
		path += "?confirm=true"
	}
	if err := c.postJSON(path, "{}", &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (c *Client) ApplySnapshotRetention(appID string, keep int, confirm bool) (*SnapshotRetentionReceipt, error) {
	path := fmt.Sprintf("/api/apps/%s/snapshots/retention?keep=%d", appID, keep)
	if confirm {
		path += "&confirm=true"
	}
	var receipt SnapshotRetentionReceipt
	if err := c.postJSON(path, "{}", &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (c *Client) AccessEvents(limit int) ([]AccessEvent, error) {
	var events []AccessEvent
	if limit <= 0 {
		limit = 50
	}
	if err := c.get(fmt.Sprintf("/api/access/events?limit=%d", limit), &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *Client) CronHistory(appID string) ([]CronState, error) {
	var states []CronState
	if err := c.get("/api/apps/"+appID+"/cron/history", &states); err != nil {
		return nil, err
	}
	return states, nil
}

func (c *Client) CronTrigger(appID, process string) error {
	body := fmt.Sprintf(`{"process":%q}`, process)
	return c.post("/api/apps/"+appID+"/cron/trigger", body)
}

func (c *Client) CronPause(appID, process string) error {
	body := fmt.Sprintf(`{"process":%q}`, process)
	return c.post("/api/apps/"+appID+"/cron/pause", body)
}

func (c *Client) CronResume(appID, process string) error {
	body := fmt.Sprintf(`{"process":%q}`, process)
	return c.post("/api/apps/"+appID+"/cron/resume", body)
}

func (c *Client) CronUpdateSchedule(appID, process, schedule string) error {
	body := fmt.Sprintf(`{"process":%q,"schedule":%q}`, process, schedule)
	return c.put("/api/apps/"+appID+"/cron/schedule", body)
}

func (c *Client) InvokeFunction(appID, process, body string) (*FuncExecution, error) {
	reqBody := fmt.Sprintf(`{"process":%q,"body":%q}`, process, body)
	var exec FuncExecution
	if err := c.postJSON("/api/apps/"+appID+"/invoke", reqBody, &exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

func (c *Client) FunctionHistory(appID string) ([]FuncExecution, error) {
	var execs []FuncExecution
	if err := c.get("/api/apps/"+appID+"/function/history", &execs); err != nil {
		return nil, err
	}
	return execs, nil
}

func (c *Client) WebSocketURL() string {
	return c.WebSocketURLFor("/ws")
}

func (c *Client) WebSocketURLFor(path string) string {
	base := c.BaseURL
	base = strings.Replace(base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	return base + path
}

// HTTP helpers

func (c *Client) get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) post(path, body string) error {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) postJSON(path, body string, v any) error {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) put(path, body string) error {
	req, err := http.NewRequest(http.MethodPut, c.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) del(path string) error {
	req, err := http.NewRequest(http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
