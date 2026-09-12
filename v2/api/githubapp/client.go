// Package githubapp provides the narrowly scoped GitHub App client used by
// Norn's fleet GitOps bridge. It deliberately has no cloud-provider clients.
package githubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"

	"norn/v2/api/fleet"
)

const apiVersion = "2026-03-10"

var (
	repositoryRe         = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	workflowRe           = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.ya?ml$`)
	branchRe             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	commitSHARe          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Re             = regexp.MustCompile(`^[0-9a-f]{64}$`)
	dispatchNonceRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pilotRunIDRe         = regexp.MustCompile(`^[a-z0-9]{8,24}$`)
	planIDRe             = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	ErrNotReady          = errors.New("reviewed fleet plan is not ready")
	ErrStalePlan         = errors.New("fleet plan no longer matches GitHub main")
	ErrDispatchPreSubmit = errors.New("protected dispatch failed before submission")
	ErrDispatchAmbiguous = errors.New("protected dispatch outcome is ambiguous")
	ErrRerunIneligible   = errors.New("protected workflow rerun is not eligible")
)

const applyRunDisplayTitleFormat = "Apply %s Norn plan %s nonce %s"

// RunBoundFleetRoot returns the protected workflow lane for an explicitly
// allowlisted disposable Fleet document. These routes are intentionally
// distinct from staging: a receipt or workflow run for one root must never
// authorize another root. Callers must also require the exact pilot run ID;
// this helper intentionally does not infer a lane from a path.
func RunBoundFleetRoot(configPath string) (string, bool) {
	switch configPath {
	case "environments/disposable/fleet/nyc3/cluster.yaml":
		return "disposable/fleet/nyc3", true
	case "environments/disposable/external-mac/nyc3/cluster.yaml":
		return "disposable/external-mac/nyc3", true
	default:
		return "", false
	}
}

type Config struct {
	AppID          string
	InstallationID int64
	PrivateKeyFile string
	Repository     string
	// Environment is the fixed control-plane lane that maps to the exact Fleet
	// root and protected GitHub Environment passed to the apply workflow.
	Environment   string
	PilotRunID    string
	DefaultBranch string
	ConfigPath    string
	PlanWorkflow  string
	ApplyWorkflow string
	APIBaseURL    string
	Production    bool
}

type Client struct {
	cfg        Config
	httpClient *http.Client
	now        func() time.Time
	pause      func(time.Duration)
	tokenMu    sync.Mutex
	tokens     map[string]cachedToken
}

type cachedToken struct {
	value      string
	reuseUntil time.Time
}

type Status struct {
	SchemaVersion string `json:"schemaVersion"`
	Configured    bool   `json:"configured"`
	Connected     bool   `json:"connected"`
	Repository    string `json:"repository,omitempty"`
	Installation  int64  `json:"installationId,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	ConfigPath    string `json:"configPath,omitempty"`
	PlanWorkflow  string `json:"planWorkflow,omitempty"`
	ApplyWorkflow string `json:"applyWorkflow,omitempty"`
	Environment   string `json:"environment,omitempty"`
	PilotRunID    string `json:"pilotRunId,omitempty"`
	Message       string `json:"message,omitempty"`
}

type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
}

type Dispatch struct {
	RunID           int64  `json:"runId"`
	RunAttempt      int    `json:"runAttempt,omitempty"`
	URL             string `json:"url"`
	PlanRunID       int64  `json:"planRunId"`
	PlanSHA         string `json:"planSha256"`
	ApprovedHeadSHA string `json:"approvedHeadSha"`
	PilotRunID      string `json:"pilotRunId,omitempty"`
	Existing        bool   `json:"existing"`
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("GitHub API returned %d: %s", e.Status, e.Message)
}

func New(cfg Config, httpClient *http.Client) (*Client, error) {
	rawConfigPath := strings.TrimSpace(cfg.ConfigPath)
	if rawConfigPath == "" || strings.HasPrefix(rawConfigPath, "/") || strings.Contains(rawConfigPath, "..") {
		return nil, fmt.Errorf("fleet GitHub config path must be repository-relative without traversal")
	}
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.PrivateKeyFile = strings.TrimSpace(cfg.PrivateKeyFile)
	cfg.Repository = strings.TrimSpace(cfg.Repository)
	cfg.Environment = strings.ToLower(strings.TrimSpace(cfg.Environment))
	cfg.PilotRunID = strings.ToLower(strings.TrimSpace(cfg.PilotRunID))
	cfg.DefaultBranch = strings.TrimSpace(cfg.DefaultBranch)
	cfg.ConfigPath = path.Clean(rawConfigPath)
	cfg.PlanWorkflow = strings.TrimSpace(cfg.PlanWorkflow)
	cfg.ApplyWorkflow = strings.TrimSpace(cfg.ApplyWorkflow)
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if cfg.DefaultBranch == "" {
		cfg.DefaultBranch = "main"
	}
	if cfg.PlanWorkflow == "" {
		cfg.PlanWorkflow = "plan.yml"
	}
	if cfg.ApplyWorkflow == "" {
		cfg.ApplyWorkflow = "apply.yml"
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.github.com"
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{cfg: cfg, httpClient: httpClient, now: time.Now, pause: time.Sleep, tokens: map[string]cachedToken{}}, nil
}

func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.AppID) != "" || cfg.InstallationID != 0 || strings.TrimSpace(cfg.PrivateKeyFile) != "" || strings.TrimSpace(cfg.Repository) != "" || strings.TrimSpace(cfg.Environment) != ""
}

func validateConfig(cfg Config) error {
	if cfg.AppID == "" || cfg.InstallationID <= 0 || cfg.PrivateKeyFile == "" || cfg.Repository == "" || cfg.ConfigPath == "" || cfg.Environment == "" {
		return fmt.Errorf("GitHub App ID, installation ID, private key file, repository, fleet config path, and environment are required")
	}
	if cfg.Environment != "staging" && cfg.Environment != "production" {
		return fmt.Errorf("fleet GitHub environment must be staging or production")
	}
	if !repositoryRe.MatchString(cfg.Repository) {
		return fmt.Errorf("GitHub repository must be owner/name")
	}
	if !branchRe.MatchString(cfg.DefaultBranch) || strings.Contains(cfg.DefaultBranch, "..") {
		return fmt.Errorf("GitHub default branch is invalid")
	}
	if !workflowRe.MatchString(cfg.PlanWorkflow) || !workflowRe.MatchString(cfg.ApplyWorkflow) {
		return fmt.Errorf("GitHub workflow names must be YAML filenames")
	}
	if cfg.ConfigPath == "." || strings.HasPrefix(cfg.ConfigPath, "../") || !strings.HasSuffix(cfg.ConfigPath, ".yaml") {
		return fmt.Errorf("fleet GitHub config path must be a repository-relative YAML path")
	}
	if cfg.PilotRunID != "" {
		if _, allowed := RunBoundFleetRoot(cfg.ConfigPath); cfg.Environment != "staging" || !pilotRunIDRe.MatchString(cfg.PilotRunID) || !allowed {
			return fmt.Errorf("run-bound Fleet dispatch requires staging, an allowlisted disposable root, and an exact pilot run ID")
		}
	} else if cfg.ConfigPath != fmt.Sprintf("environments/%s/nyc3/cluster.yaml", cfg.Environment) {
		return fmt.Errorf("fleet GitHub config path must match the configured %s environment root", cfg.Environment)
	}
	base, err := url.Parse(cfg.APIBaseURL)
	if err != nil || base == nil {
		return fmt.Errorf("GitHub API base URL is invalid")
	}
	loopbackHTTP := base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1")
	if base.Host == "" || base.User != nil || (base.Scheme != "https" && !loopbackHTTP) {
		return fmt.Errorf("GitHub API base URL must be credential-free HTTPS, except for loopback tests")
	}
	if cfg.Production && !strings.EqualFold(base.Hostname(), "api.github.com") {
		return fmt.Errorf("production GitHub API base URL must be api.github.com")
	}
	return nil
}

func (c *Client) Status(ctx context.Context) Status {
	status := Status{SchemaVersion: "norn.fleet-github-status/v1", Configured: true, Repository: c.cfg.Repository, Installation: c.cfg.InstallationID, Environment: c.cfg.Environment, PilotRunID: c.cfg.PilotRunID, DefaultBranch: c.cfg.DefaultBranch, ConfigPath: c.cfg.ConfigPath, PlanWorkflow: c.cfg.PlanWorkflow, ApplyWorkflow: c.cfg.ApplyWorkflow}
	token, err := c.installationToken(ctx, map[string]string{"metadata": "read"})
	if err != nil {
		status.Message = "GitHub App authentication failed"
		return status
	}
	var repo struct {
		FullName string `json:"full_name"`
	}
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(""), nil, &repo); err != nil || !strings.EqualFold(repo.FullName, c.cfg.Repository) {
		status.Message = "GitHub App cannot access the configured repository"
		return status
	}
	status.Connected = true
	status.Message = "GitHub App installation is ready"
	return status
}

func (c *Client) CreatePullRequest(ctx context.Context, planID, planDigest, poolName, action string, proposed fleet.NodePool, sourceDigest string) (*PullRequest, error) {
	token, err := c.installationToken(ctx, map[string]string{"contents": "write", "pull_requests": "write"})
	if err != nil {
		return nil, err
	}
	content, fileSHA, err := c.getContent(ctx, token, c.cfg.DefaultBranch)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	if sourceDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return nil, ErrStalePlan
	}
	document, report := fleet.ParseAndValidate(content)
	if document == nil || report == nil || !report.Valid {
		return nil, fmt.Errorf("GitHub fleet document does not pass Norn validation")
	}
	if !strings.EqualFold(document.Metadata.Repository, c.cfg.Repository) {
		return nil, fmt.Errorf("GitHub fleet document repository does not match the configured installation")
	}
	if _, ok := document.NodePools[poolName]; !ok {
		return nil, fmt.Errorf("capacity plan node pool is not present on GitHub main")
	}
	noDesiredChange := reflect.DeepEqual(document.NodePools[poolName], proposed)
	var updated []byte
	var receiptPath string
	var receipt []byte
	if noDesiredChange {
		receiptPath, receipt, err = planReviewReceipt(planID, planDigest, sourceDigest, poolName, action, proposed)
		if err != nil {
			return nil, err
		}
	} else {
		document.NodePools[poolName] = proposed
		updated, err = yaml.Marshal(document)
		if err != nil {
			return nil, fmt.Errorf("encode fleet document: %w", err)
		}
		if _, check := fleet.ParseAndValidate(updated); check == nil || !check.Valid {
			return nil, fmt.Errorf("proposed fleet document failed validation")
		}
	}
	branch := "norn/plan-" + planID
	baseSHA, err := c.getRef(ctx, token, c.cfg.DefaultBranch)
	if err != nil {
		return nil, err
	}
	created, err := c.createRef(ctx, token, branch, baseSHA)
	if err != nil {
		return nil, err
	}
	if created {
		message := fmt.Sprintf("fleet: apply Norn plan %s", planID)
		if noDesiredChange {
			message = fmt.Sprintf("fleet: record Norn plan %s review receipt", planID)
			if err := c.putContentAt(ctx, token, receiptPath, branch, "", message, receipt); err != nil {
				return nil, err
			}
		} else if err := c.putContent(ctx, token, branch, fileSHA, message, updated); err != nil {
			return nil, err
		}
	} else {
		var existing []byte
		var getErr error
		if noDesiredChange {
			existing, _, getErr = c.getContentAt(ctx, token, receiptPath, branch)
		} else {
			existing, _, getErr = c.getContent(ctx, token, branch)
		}
		expected := updated
		if noDesiredChange {
			expected = receipt
		}
		if getErr != nil || !bytes.Equal(existing, expected) {
			return nil, fmt.Errorf("plan branch already exists with different content")
		}
	}
	if existing, _ := c.findPullRequest(ctx, token, branch); existing != nil {
		if existing.State == "closed" && !existing.Merged {
			return nil, fmt.Errorf("fleet pull request was closed without merge; create a fresh Norn plan")
		}
		return existing, nil
	}
	title := fmt.Sprintf("Fleet: %s %s", action, poolName)
	body := fmt.Sprintf("Norn capacity plan `%s`\n\nDigest: `%s`\n\nThis pull request changes desired infrastructure only. Provider and state credentials remain in the protected runner.", planID, planDigest)
	if noDesiredChange {
		body = fmt.Sprintf("Norn capacity plan `%s`\n\nDigest: `%s`\n\nDesired topology is already exactly the proposed configuration. This pull request adds the immutable Norn review receipt only; it does not manufacture a topology change. Provider and state credentials remain in the protected runner.", planID, planDigest)
	}
	var response struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
	}
	err = c.request(ctx, token, http.MethodPost, c.repoPath("/pulls"), map[string]any{"title": title, "head": branch, "base": c.cfg.DefaultBranch, "body": body, "maintainer_can_modify": false}, &response)
	if err != nil {
		return nil, err
	}
	return &PullRequest{Number: response.Number, URL: response.HTMLURL, Branch: branch, State: response.State}, nil
}

// ResolveApprovedPlan pins the successful protected plan workflow and its
// review artifact before a dispatch is persisted. Callers must not rediscover
// a moving main branch after this binding is created.
func (c *Client) ResolveApprovedPlan(ctx context.Context, planID, fleetEnvironment string) (*Dispatch, error) {
	if fleetEnvironment != c.fleetRoot() {
		return nil, fmt.Errorf("requested fleet environment is not the configured fleet root")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, err
	}
	return c.resolveApprovedPlan(ctx, token, planID, fleetEnvironment)
}

// DispatchBoundPlan dispatches or recovers exactly one server-persisted
// approval binding. The nonce is generated by Norn for this one request and
// never returned or persisted in raw form.
func (c *Client) DispatchBoundPlan(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonce string) (*Dispatch, error) {
	return c.dispatchBoundPlan(ctx, planID, fleetEnvironment, allowDestructive, approved, nonce, "")
}

// DispatchBoundExternalMacPlan transmits the canonical owner-approval digest
// alongside the transient nonce. The raw approval never enters Norn.
func (c *Client) DispatchBoundExternalMacPlan(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonce, approvalEnvelopeSHA256 string) (*Dispatch, error) {
	if !sha256Re.MatchString(approvalEnvelopeSHA256) {
		return nil, fmt.Errorf("dispatch approval digest is invalid")
	}
	return c.dispatchBoundPlan(ctx, planID, fleetEnvironment, allowDestructive, approved, nonce, approvalEnvelopeSHA256)
}

func (c *Client) dispatchBoundPlan(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonce, approvalEnvelopeSHA256 string) (*Dispatch, error) {
	if fleetEnvironment != c.fleetRoot() {
		return nil, fmt.Errorf("requested fleet environment is not the configured fleet root")
	}
	if approved == nil || approved.PlanRunID <= 0 || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !dispatchNonceRe.MatchString(nonce) || approved.PilotRunID != c.cfg.PilotRunID {
		return nil, fmt.Errorf("dispatch binding is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDispatchPreSubmit, err)
	}
	appActor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDispatchPreSubmit, err)
	}
	if approved.RunID > 0 {
		existing, getErr := c.getApplyRun(ctx, token, approved.RunID)
		if getErr != nil {
			return nil, getErr
		}
		if approved.URL != "" && existing.HTMLURL != approved.URL {
			return nil, fmt.Errorf("persisted GitHub apply run URL does not match its protected binding")
		}
		if verifyErr := c.verifyApplyRun(existing, planID, fleetEnvironment, approved, nonce, appActor); verifyErr != nil {
			return nil, verifyErr
		}
		return &Dispatch{RunID: existing.ID, RunAttempt: existing.RunAttempt, URL: existing.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID, Existing: true}, nil
	}
	if existing, findErr := c.findApplyRun(ctx, token, planID, fleetEnvironment, approved, nonce, appActor); findErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrDispatchPreSubmit, findErr)
	} else if existing != nil {
		existing.Existing = true
		return existing, nil
	}
	var response struct {
		WorkflowRunID int64  `json:"workflow_run_id"`
		HTMLURL       string `json:"html_url"`
	}
	inputs := map[string]string{
		"fleet_environment": fleetEnvironment,
		"plan_run_id":       fmt.Sprintf("%d", approved.PlanRunID),
		"plan_sha256":       approved.PlanSHA,
		"norn_plan_id":      planID,
		"allow_destructive": fmt.Sprintf("%t", allowDestructive),
		"dispatch_nonce":    nonce,
	}
	if c.cfg.PilotRunID != "" {
		inputs["pilot_run_id"] = c.cfg.PilotRunID
		if approvalEnvelopeSHA256 != "" {
			inputs["approval_envelope_sha256"] = approvalEnvelopeSHA256
		}
	}
	err = c.request(ctx, token, http.MethodPost, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/dispatches"), map[string]any{
		"ref":                c.cfg.DefaultBranch,
		"return_run_details": true,
		"inputs":             inputs,
	}, &response)
	if err != nil {
		if apiErr := new(apiError); errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			return nil, fmt.Errorf("%w: %v", ErrDispatchPreSubmit, err)
		}
		return c.recoverSubmittedDispatch(ctx, token, planID, fleetEnvironment, approved, nonce, appActor)
	}
	if response.WorkflowRunID <= 0 || !canonicalWorkflowURL(c.cfg.Repository, response.WorkflowRunID, response.HTMLURL) {
		return c.recoverSubmittedDispatch(ctx, token, planID, fleetEnvironment, approved, nonce, appActor)
	}
	run, err := c.getApplyRun(ctx, token, response.WorkflowRunID)
	if err != nil {
		return c.recoverSubmittedDispatch(ctx, token, planID, fleetEnvironment, approved, nonce, appActor)
	}
	if err := c.verifyApplyRun(run, planID, fleetEnvironment, approved, nonce, appActor); err != nil {
		return c.recoverSubmittedDispatch(ctx, token, planID, fleetEnvironment, approved, nonce, appActor)
	}
	return &Dispatch{RunID: response.WorkflowRunID, RunAttempt: run.RunAttempt, URL: response.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID}, nil
}

// RerunBoundExternalMacPlan spends the sole retry for an already-bound
// workflow run. It never dispatches a new workflow and returns ambiguity for
// every outcome that cannot be proved by observing this exact run attempt.
func (c *Client) RerunBoundExternalMacPlan(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonceHash, approvalEnvelopeSHA256 string, runID int64, previousAttempt int) (*Dispatch, error) {
	if err := c.CheckBoundExternalMacRerunEligibility(ctx, planID, fleetEnvironment, approved, nonceHash, approvalEnvelopeSHA256, runID, previousAttempt); err != nil {
		return nil, err
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	appActor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	if err := c.request(ctx, token, http.MethodPost, c.repoPath(fmt.Sprintf("/actions/runs/%d/rerun", runID)), map[string]bool{"enable_debug_logging": false}, nil); err != nil {
		return nil, ErrDispatchAmbiguous
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && c.pause != nil {
			c.pause(time.Duration(attempt) * 25 * time.Millisecond)
		}
		observed, getErr := c.getApplyRun(ctx, token, runID)
		if getErr != nil {
			return nil, ErrDispatchAmbiguous
		}
		if verifyErr := c.verifyApplyRunByNonceHash(observed, planID, fleetEnvironment, approved, nonceHash, appActor); verifyErr != nil {
			return nil, ErrDispatchAmbiguous
		}
		if observed.RunAttempt == previousAttempt+1 {
			return &Dispatch{RunID: observed.ID, RunAttempt: observed.RunAttempt, URL: observed.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID, Existing: true}, nil
		}
	}
	return nil, ErrDispatchAmbiguous
}

// CheckBoundExternalMacRerunEligibility performs only the conclusive
// pre-submit checks. Callers use it before taking the durable rerun fence so
// a known ineligible run does not get mistaken for an ambiguous submission.
func (c *Client) CheckBoundExternalMacRerunEligibility(ctx context.Context, planID, fleetEnvironment string, approved *Dispatch, nonceHash, approvalEnvelopeSHA256 string, runID int64, previousAttempt int) error {
	if fleetEnvironment != c.fleetRoot() || approved == nil || approved.PlanRunID <= 0 || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !sha256Re.MatchString(nonceHash) || !sha256Re.MatchString(approvalEnvelopeSHA256) || runID <= 0 || previousAttempt <= 0 || approved.PilotRunID != c.cfg.PilotRunID {
		return ErrRerunIneligible
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return ErrDispatchAmbiguous
	}
	appActor, err := c.appActorLogin(ctx)
	if err != nil {
		return ErrDispatchAmbiguous
	}
	run, err := c.getApplyRun(ctx, token, runID)
	if err != nil {
		return ErrDispatchAmbiguous
	}
	if err := c.verifyApplyRunByNonceHash(run, planID, fleetEnvironment, approved, nonceHash, appActor); err != nil {
		return ErrRerunIneligible
	}
	if run.RunAttempt != previousAttempt || run.Status != "completed" || (run.Conclusion != "failure" && run.Conclusion != "cancelled" && run.Conclusion != "timed_out") {
		return ErrRerunIneligible
	}
	return nil
}

// RecoverBoundExternalMacRerun is lookup-only recovery for a rerun whose POST
// response was lost. It must never be replaced with RerunBoundExternalMacPlan:
// that would issue a second GitHub rerun from an ambiguous durable fence.
func (c *Client) RecoverBoundExternalMacRerun(ctx context.Context, planID, fleetEnvironment string, approved *Dispatch, nonceHash string, runID int64, previousAttempt int) (*Dispatch, error) {
	if fleetEnvironment != c.fleetRoot() || approved == nil || approved.PlanRunID <= 0 || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !sha256Re.MatchString(nonceHash) || runID <= 0 || previousAttempt <= 0 || approved.PilotRunID != c.cfg.PilotRunID {
		return nil, ErrDispatchAmbiguous
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	appActor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && c.pause != nil {
			c.pause(time.Duration(attempt) * 25 * time.Millisecond)
		}
		observed, getErr := c.getApplyRun(ctx, token, runID)
		if getErr != nil || c.verifyApplyRunByNonceHash(observed, planID, fleetEnvironment, approved, nonceHash, appActor) != nil {
			return nil, ErrDispatchAmbiguous
		}
		if observed.RunAttempt == previousAttempt+1 {
			return &Dispatch{RunID: observed.ID, RunAttempt: observed.RunAttempt, URL: observed.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID, Existing: true}, nil
		}
	}
	return nil, ErrDispatchAmbiguous
}

// RecoverBoundPlan performs lookup-only recovery of a dispatch whose POST may
// have succeeded before Norn lost its response or crashed.  The durable
// binding intentionally retains only nonceHash, so candidates are selected by
// parsing their exact run title and comparing hashes in constant time.  This
// method never creates a GitHub workflow dispatch.
func (c *Client) RecoverBoundPlan(ctx context.Context, planID, fleetEnvironment string, approved *Dispatch, nonceHash string) (*Dispatch, error) {
	if fleetEnvironment != c.fleetRoot() || approved == nil || approved.PlanRunID <= 0 || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !sha256Re.MatchString(nonceHash) || approved.PilotRunID != c.cfg.PilotRunID {
		return nil, ErrDispatchAmbiguous
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	appActor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && c.pause != nil {
			c.pause(time.Duration(attempt) * 25 * time.Millisecond)
		}
		found, findErr := c.findApplyRunByNonceHash(ctx, token, planID, fleetEnvironment, approved, nonceHash, appActor)
		if findErr != nil {
			return nil, ErrDispatchAmbiguous
		}
		if found != nil {
			found.Existing = true
			return found, nil
		}
	}
	return nil, ErrDispatchAmbiguous
}

func (c *Client) recoverSubmittedDispatch(ctx context.Context, token, planID, fleetEnvironment string, approved *Dispatch, nonce, appActor string) (*Dispatch, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && c.pause != nil {
			c.pause(time.Duration(attempt) * 25 * time.Millisecond)
		}
		found, err := c.findApplyRun(ctx, token, planID, fleetEnvironment, approved, nonce, appActor)
		if err == nil && found != nil {
			found.Existing = true
			return found, nil
		}
	}
	return nil, ErrDispatchAmbiguous
}

func (c *Client) resolveApprovedPlan(ctx context.Context, token, planID, fleetEnvironment string) (*Dispatch, error) {
	pr, err := c.findPullRequest(ctx, token, "norn/plan-"+planID)
	if err != nil || pr == nil || !pr.Merged {
		return nil, ErrNotReady
	}
	var pulls struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
		MergedAt       string `json:"merged_at"`
	}
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/pulls/%d", pr.Number)), nil, &pulls); err != nil || pulls.MergedAt == "" || pulls.MergeCommitSHA == "" {
		return nil, ErrNotReady
	}
	var runs struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	event := "push"
	if c.cfg.PilotRunID != "" {
		event = "workflow_dispatch"
	}
	query := "?event=" + event + "&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&status=success&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.PlanWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	for _, run := range runs.WorkflowRuns {
		if run.HeadSHA != pulls.MergeCommitSHA || run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		planSHA, artifactErr := c.planArtifactSHA(ctx, token, run.ID, fleetEnvironment)
		if artifactErr == nil {
			return &Dispatch{PlanRunID: run.ID, PlanSHA: planSHA, ApprovedHeadSHA: pulls.MergeCommitSHA, PilotRunID: c.cfg.PilotRunID}, nil
		}
	}
	return nil, ErrNotReady
}

func (c *Client) planArtifactSHA(ctx context.Context, token string, runID int64, fleetEnvironment string) (string, error) {
	var artifacts struct {
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Expired bool   `json:"expired"`
		} `json:"artifacts"`
	}
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/artifacts", runID)), nil, &artifacts); err != nil {
		return "", err
	}
	matching := []int{}
	for index, artifact := range artifacts.Artifacts {
		if artifact.Name == fmt.Sprintf("fleet-plan-%s-%d", strings.ReplaceAll(fleetEnvironment, "/", "-"), runID) {
			matching = append(matching, index)
		}
	}
	if len(matching) != 1 || artifacts.Artifacts[matching[0]].Expired {
		return "", ErrNotReady
	}
	for _, index := range matching {
		artifact := artifacts.Artifacts[index]
		archive, err := c.requestBytes(ctx, token, c.repoPath(fmt.Sprintf("/actions/artifacts/%d/zip", artifact.ID)), 16<<20)
		if err != nil {
			return "", err
		}
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return "", err
		}
		var candidate, pilotRun *zip.File
		for _, file := range reader.File {
			if !safeArtifactEntry(file.Name) {
				return "", ErrNotReady
			}
			if file.Name == "pilot-run-id.txt" {
				if pilotRun != nil || file.FileInfo().IsDir() || file.UncompressedSize64 > 128 {
					return "", ErrNotReady
				}
				pilotRun = file
				continue
			}
			if file.Name != "fleet-plan.sha256" {
				continue
			}
			if candidate != nil || file.FileInfo().IsDir() || file.UncompressedSize64 > 256 {
				return "", ErrNotReady
			}
			candidate = file
		}
		if c.cfg.PilotRunID != "" && pilotRun == nil {
			return "", ErrNotReady
		}
		if pilotRun != nil {
			opened, openErr := pilotRun.Open()
			if openErr != nil {
				return "", openErr
			}
			value, readErr := io.ReadAll(io.LimitReader(opened, 129))
			_ = opened.Close()
			pilot := strings.TrimSpace(string(value))
			if readErr != nil || (c.cfg.PilotRunID != "" && pilot != c.cfg.PilotRunID) || (c.cfg.PilotRunID == "" && pilot != "") {
				return "", ErrNotReady
			}
		}
		if candidate != nil {
			file := candidate
			opened, openErr := file.Open()
			if openErr != nil {
				return "", openErr
			}
			value, readErr := io.ReadAll(io.LimitReader(opened, 257))
			_ = opened.Close()
			sha := strings.TrimSpace(string(value))
			if readErr == nil && sha256Re.MatchString(sha) {
				return sha, nil
			}
		}
	}
	return "", ErrNotReady
}

func (c *Client) findApplyRun(ctx context.Context, token, planID, fleetEnvironment string, approved *Dispatch, nonce, appActor string) (*Dispatch, error) {
	var runs struct {
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	query := "?event=workflow_dispatch&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	for _, run := range runs.WorkflowRuns {
		candidate, err := c.getApplyRun(ctx, token, run.ID)
		if err != nil {
			return nil, err
		}
		if err := c.verifyApplyRun(candidate, planID, fleetEnvironment, approved, nonce, appActor); err == nil {
			return &Dispatch{RunID: candidate.ID, RunAttempt: candidate.RunAttempt, URL: candidate.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID}, nil
		}
	}
	return nil, nil
}

func (c *Client) findApplyRunByNonceHash(ctx context.Context, token, planID, fleetEnvironment string, approved *Dispatch, nonceHash, appActor string) (*Dispatch, error) {
	var runs struct {
		WorkflowRuns []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	query := "?event=workflow_dispatch&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	var match *Dispatch
	for _, run := range runs.WorkflowRuns {
		candidate, err := c.getApplyRun(ctx, token, run.ID)
		if err != nil {
			return nil, err
		}
		if err := c.verifyApplyRunByNonceHash(candidate, planID, fleetEnvironment, approved, nonceHash, appActor); err != nil {
			continue
		}
		if match != nil {
			return nil, ErrDispatchAmbiguous
		}
		match = &Dispatch{RunID: candidate.ID, RunAttempt: candidate.RunAttempt, URL: candidate.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID}
	}
	return match, nil
}

type applyRun struct {
	ID           int64  `json:"id"`
	HTMLURL      string `json:"html_url"`
	Event        string `json:"event"`
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	RunAttempt   int    `json:"run_attempt"`
	Actor        struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"actor"`
}

func (c *Client) getApplyRun(ctx context.Context, token string, runID int64) (*applyRun, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("GitHub workflow run ID is invalid")
	}
	var run applyRun
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d", runID)), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (c *Client) verifyApplyRun(run *applyRun, planID, fleetEnvironment string, approved *Dispatch, nonce, appActor string) error {
	if err := c.verifyApplyRunIdentity(run, approved, appActor); err != nil {
		return err
	}
	if run.DisplayTitle != fmt.Sprintf(applyRunDisplayTitleFormat, fleetEnvironment, planID, nonce) {
		return fmt.Errorf("GitHub apply run does not carry the dispatch nonce")
	}
	return nil
}

func (c *Client) verifyApplyRunByNonceHash(run *applyRun, planID, fleetEnvironment string, approved *Dispatch, nonceHash, appActor string) error {
	if err := c.verifyApplyRunIdentity(run, approved, appActor); err != nil {
		return err
	}
	environment, candidatePlanID, candidateNonce, ok := parseApplyRunDisplayTitle(run.DisplayTitle)
	if !ok || environment != fleetEnvironment || candidatePlanID != planID {
		return fmt.Errorf("GitHub apply run does not carry the protected dispatch identity")
	}
	candidateHash := sha256.Sum256([]byte(candidateNonce))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(candidateHash[:])), []byte(nonceHash)) != 1 {
		return fmt.Errorf("GitHub apply run does not carry the protected dispatch nonce")
	}
	return nil
}

func (c *Client) verifyApplyRunIdentity(run *applyRun, approved *Dispatch, appActor string) error {
	if run == nil || approved == nil || !canonicalWorkflowURL(c.cfg.Repository, run.ID, run.HTMLURL) || run.Event != "workflow_dispatch" || run.HeadBranch != c.cfg.DefaultBranch || run.HeadSHA != approved.ApprovedHeadSHA || !workflowPathMatches(run.Path, c.cfg.ApplyWorkflow, c.cfg.DefaultBranch) || run.Name != "apply" || run.Actor.Type != "Bot" || run.Actor.Login != appActor {
		return fmt.Errorf("GitHub apply run does not match the protected dispatch identity")
	}
	return nil
}

func parseApplyRunDisplayTitle(value string) (environment, planID, nonce string, ok bool) {
	const planMarker = " Norn plan "
	const nonceMarker = " nonce "
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "Apply ") {
		return "", "", "", false
	}
	environment, rest, found := strings.Cut(strings.TrimPrefix(value, "Apply "), planMarker)
	if !found || environment == "" {
		return "", "", "", false
	}
	planID, nonce, found = strings.Cut(rest, nonceMarker)
	if !found || planID == "" || !dispatchNonceRe.MatchString(nonce) {
		return "", "", "", false
	}
	return environment, planID, nonce, true
}

func workflowPathMatches(value, workflow, defaultBranch string) bool {
	want := ".github/workflows/" + workflow
	if value == want {
		return true
	}
	base, ref, found := strings.Cut(value, "@")
	return found && base == want && (ref == defaultBranch || ref == "refs/heads/"+defaultBranch)
}

func (c *Client) appActorLogin(ctx context.Context) (string, error) {
	now := c.now().UTC()
	key, err := c.privateKey()
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": c.cfg.AppID}
	appJWT, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := c.requestWithAuth(ctx, appJWT, http.MethodGet, "/app", nil, &app); err != nil || !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(app.Slug) {
		return "", fmt.Errorf("GitHub App identity is unavailable")
	}
	return app.Slug + "[bot]", nil
}

func canonicalWorkflowURL(repository string, runID int64, value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "/"+repository+fmt.Sprintf("/actions/runs/%d", runID)
}

func safeArtifactEntry(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".") || strings.Contains(name, "\\\\") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func (c *Client) fleetRoot() string {
	if c.cfg.PilotRunID != "" {
		root, _ := RunBoundFleetRoot(c.cfg.ConfigPath) // New validates this configuration.
		return root
	}
	return c.cfg.Environment + "/nyc3"
}

func (c *Client) findPullRequest(ctx context.Context, token, branch string) (*PullRequest, error) {
	owner := strings.SplitN(c.cfg.Repository, "/", 2)[0]
	query := "?state=all&head=" + url.QueryEscape(owner+":"+branch) + "&base=" + url.QueryEscape(c.cfg.DefaultBranch) + "&per_page=10"
	var pulls []struct {
		Number   int    `json:"number"`
		HTMLURL  string `json:"html_url"`
		State    string `json:"state"`
		MergedAt string `json:"merged_at"`
	}
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/pulls"+query), nil, &pulls); err != nil {
		return nil, err
	}
	if len(pulls) == 0 {
		return nil, nil
	}
	return &PullRequest{Number: pulls[0].Number, URL: pulls[0].HTMLURL, Branch: branch, State: pulls[0].State, Merged: pulls[0].MergedAt != ""}, nil
}

func (c *Client) getRef(ctx context.Context, token, branch string) (string, error) {
	var response struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/git/ref/heads/"+url.PathEscape(branch)), nil, &response); err != nil {
		return "", err
	}
	return response.Object.SHA, nil
}

func (c *Client) createRef(ctx context.Context, token, branch, sha string) (bool, error) {
	err := c.request(ctx, token, http.MethodPost, c.repoPath("/git/refs"), map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
	if apiErr := new(apiError); errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) getContent(ctx context.Context, token, ref string) ([]byte, string, error) {
	return c.getContentAt(ctx, token, c.cfg.ConfigPath, ref)
}

func (c *Client) getContentAt(ctx context.Context, token, repoPath, ref string) ([]byte, string, error) {
	var response struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
	}
	endpoint := c.repoPath("/contents/"+escapePath(repoPath)) + "?ref=" + url.QueryEscape(ref)
	if err := c.request(ctx, token, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, "", err
	}
	if response.Encoding != "base64" || response.Size > 64<<10 {
		return nil, "", fmt.Errorf("fleet document response is invalid or too large")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
	return content, response.SHA, err
}

func (c *Client) putContent(ctx context.Context, token, branch, sha, message string, content []byte) error {
	return c.putContentAt(ctx, token, c.cfg.ConfigPath, branch, sha, message, content)
}

func (c *Client) putContentAt(ctx context.Context, token, repoPath, branch, sha, message string, content []byte) error {
	payload := map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString(content), "branch": branch}
	if sha != "" {
		payload["sha"] = sha
	}
	return c.request(ctx, token, http.MethodPut, c.repoPath("/contents/"+escapePath(repoPath)), payload, nil)
}

// planReviewReceipt creates the only alternate review diff a capacity plan may
// write: a deterministic, plan-ID-addressed receipt. It exists solely when the
// validated desired node pool already equals the proposal, so a first cold
// create still has an honest, reviewable PR without formatting churn.
func planReviewReceipt(planID, planDigest, sourceDigest, poolName, action string, proposed fleet.NodePool) (string, []byte, error) {
	if !planIDRe.MatchString(planID) || !strings.HasPrefix(planDigest, "sha256:") || !strings.HasPrefix(sourceDigest, "sha256:") || !sha256Re.MatchString(strings.TrimPrefix(planDigest, "sha256:")) || !sha256Re.MatchString(strings.TrimPrefix(sourceDigest, "sha256:")) || poolName == "" || action == "" {
		return "", nil, fmt.Errorf("plan review receipt inputs are invalid")
	}
	receipt := struct {
		SchemaVersion string         `json:"schemaVersion"`
		PlanID        string         `json:"planID"`
		PlanDigest    string         `json:"planDigest"`
		SourceDigest  string         `json:"sourceDigest"`
		NodePool      string         `json:"nodePool"`
		Action        string         `json:"action"`
		Proposed      fleet.NodePool `json:"proposed"`
	}{
		SchemaVersion: "norn.fleet-plan-review-receipt/v1",
		PlanID:        strings.ToLower(planID),
		PlanDigest:    planDigest,
		SourceDigest:  sourceDigest,
		NodePool:      poolName,
		Action:        action,
		Proposed:      proposed,
	}
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode plan review receipt: %w", err)
	}
	return ".norn/fleet-plan-receipts/" + strings.ToLower(planID) + ".json", append(encoded, '\n'), nil
}

func escapePath(value string) string {
	parts := strings.Split(value, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c *Client) installationToken(ctx context.Context, permissions map[string]string) (string, error) {
	cacheKey := permissionCacheKey(permissions)
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := c.now().UTC()
	if cached, ok := c.tokens[cacheKey]; ok && cached.reuseUntil.After(now) {
		return cached.value, nil
	}
	key, err := c.privateKey()
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": c.cfg.AppID}
	appJWT, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{strings.SplitN(c.cfg.Repository, "/", 2)[1]}, "permissions": permissions}
	endpoint := fmt.Sprintf("/app/installations/%d/access_tokens", c.cfg.InstallationID)
	if err := c.requestWithAuth(ctx, appJWT, http.MethodPost, endpoint, body, &response); err != nil {
		return "", err
	}
	if response.Token == "" || !response.ExpiresAt.After(now.Add(time.Minute)) {
		return "", fmt.Errorf("GitHub returned an invalid installation token")
	}
	reuseUntil := response.ExpiresAt.Add(-2 * time.Minute)
	if ceiling := now.Add(10 * time.Minute); reuseUntil.After(ceiling) {
		reuseUntil = ceiling
	}
	c.tokens[cacheKey] = cachedToken{value: response.Token, reuseUntil: reuseUntil}
	return response.Token, nil
}

func permissionCacheKey(permissions map[string]string) string {
	keys := make([]string, 0, len(permissions))
	for key := range permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(permissions[key])
		builder.WriteByte('\n')
	}
	return builder.String()
}

func (c *Client) privateKey() (*rsa.PrivateKey, error) {
	info, err := os.Lstat(c.cfg.PrivateKeyFile)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("GitHub App private key is unavailable")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("GitHub App private key must not be group/world accessible")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return nil, fmt.Errorf("GitHub App private key must be owned by the Norn process user")
	}
	pem, err := os.ReadFile(c.cfg.PrivateKeyFile)
	if err != nil || len(pem) > 64<<10 {
		return nil, fmt.Errorf("GitHub App private key is unreadable")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("GitHub App private key is invalid")
	}
	return key, nil
}

func (c *Client) request(ctx context.Context, token, method, endpoint string, body, response any) error {
	return c.requestWithAuth(ctx, token, method, endpoint, body, response)
}

func (c *Client) requestWithAuth(ctx context.Context, token, method, endpoint string, body, response any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIBaseURL+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&problem)
		if problem.Message == "" {
			problem.Message = http.StatusText(resp.StatusCode)
		}
		return &apiError{Status: resp.StatusCode, Message: problem.Message}
	}
	if response == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(response); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func (c *Client) requestBytes(ctx context.Context, token, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GitHub artifact exceeds size limit")
	}
	return data, err
}

func (c *Client) repoPath(suffix string) string {
	parts := strings.SplitN(c.cfg.Repository, "/", 2)
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + suffix
}
