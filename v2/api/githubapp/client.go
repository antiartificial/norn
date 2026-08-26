// Package githubapp provides the narrowly scoped GitHub App client used by
// Norn's fleet GitOps bridge. It deliberately has no cloud-provider clients.
package githubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"

	"norn/v2/api/fleet"
)

const apiVersion = "2026-03-10"

var (
	repositoryRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	workflowRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.ya?ml$`)
	branchRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	sha256Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ErrNotReady  = errors.New("reviewed fleet plan is not ready")
	ErrStalePlan = errors.New("fleet plan no longer matches GitHub main")
)

type Config struct {
	AppID          string
	InstallationID int64
	PrivateKeyFile string
	Repository     string
	DefaultBranch  string
	ConfigPath     string
	PlanWorkflow   string
	ApplyWorkflow  string
	APIBaseURL     string
	Production     bool
}

type Client struct {
	cfg        Config
	httpClient *http.Client
	now        func() time.Time
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
	RunID     int64  `json:"runId"`
	URL       string `json:"url"`
	PlanRunID int64  `json:"planRunId"`
	PlanSHA   string `json:"planSha256"`
	Existing  bool   `json:"existing"`
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
	return &Client{cfg: cfg, httpClient: httpClient, now: time.Now, tokens: map[string]cachedToken{}}, nil
}

func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.AppID) != "" || cfg.InstallationID != 0 || strings.TrimSpace(cfg.PrivateKeyFile) != "" || strings.TrimSpace(cfg.Repository) != ""
}

func validateConfig(cfg Config) error {
	if cfg.AppID == "" || cfg.InstallationID <= 0 || cfg.PrivateKeyFile == "" || cfg.Repository == "" || cfg.ConfigPath == "" {
		return fmt.Errorf("GitHub App ID, installation ID, private key file, repository, and fleet config path are required")
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
	status := Status{SchemaVersion: "norn.fleet-github-status/v1", Configured: true, Repository: c.cfg.Repository, Installation: c.cfg.InstallationID, DefaultBranch: c.cfg.DefaultBranch, ConfigPath: c.cfg.ConfigPath, PlanWorkflow: c.cfg.PlanWorkflow, ApplyWorkflow: c.cfg.ApplyWorkflow}
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
	document.NodePools[poolName] = proposed
	updated, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode fleet document: %w", err)
	}
	if _, check := fleet.ParseAndValidate(updated); check == nil || !check.Valid {
		return nil, fmt.Errorf("proposed fleet document failed validation")
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
		if err := c.putContent(ctx, token, branch, fileSHA, message, updated); err != nil {
			return nil, err
		}
	} else {
		existing, _, getErr := c.getContent(ctx, token, branch)
		if getErr != nil || !bytes.Equal(existing, updated) {
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

func (c *Client) DispatchApprovedPlan(ctx context.Context, planID string, allowDestructive bool) (*Dispatch, error) {
	token, err := c.installationToken(ctx, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, err
	}
	existing, existingErr := c.findApplyRun(ctx, token, planID)
	if existingErr != nil {
		return nil, existingErr
	}
	approved, approvedErr := c.resolveApprovedPlan(ctx, token, planID)
	if existing != nil {
		if approvedErr == nil {
			existing.PlanRunID = approved.PlanRunID
			existing.PlanSHA = approved.PlanSHA
		}
		existing.Existing = true
		return existing, nil
	}
	if approvedErr != nil {
		return nil, approvedErr
	}
	var response struct {
		WorkflowRunID int64  `json:"workflow_run_id"`
		HTMLURL       string `json:"html_url"`
	}
	err = c.request(ctx, token, http.MethodPost, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/dispatches"), map[string]any{
		"ref": c.cfg.DefaultBranch,
		"inputs": map[string]string{
			"plan_run_id":       fmt.Sprintf("%d", approved.PlanRunID),
			"plan_sha256":       approved.PlanSHA,
			"norn_plan_id":      planID,
			"allow_destructive": fmt.Sprintf("%t", allowDestructive),
		},
	}, &response)
	if err != nil {
		return nil, err
	}
	return &Dispatch{RunID: response.WorkflowRunID, URL: response.HTMLURL, PlanRunID: approved.PlanRunID, PlanSHA: approved.PlanSHA}, nil
}

func (c *Client) resolveApprovedPlan(ctx context.Context, token, planID string) (*Dispatch, error) {
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
	query := "?event=push&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&status=success&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.PlanWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	for _, run := range runs.WorkflowRuns {
		if run.HeadSHA != pulls.MergeCommitSHA || run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		planSHA, artifactErr := c.planArtifactSHA(ctx, token, run.ID)
		if artifactErr == nil {
			return &Dispatch{PlanRunID: run.ID, PlanSHA: planSHA}, nil
		}
	}
	return nil, ErrNotReady
}

func (c *Client) planArtifactSHA(ctx context.Context, token string, runID int64) (string, error) {
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
	for _, artifact := range artifacts.Artifacts {
		if artifact.Name != fmt.Sprintf("fleet-plan-%d", runID) || artifact.Expired {
			continue
		}
		archive, err := c.requestBytes(ctx, token, c.repoPath(fmt.Sprintf("/actions/artifacts/%d/zip", artifact.ID)), 16<<20)
		if err != nil {
			return "", err
		}
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return "", err
		}
		for _, file := range reader.File {
			if path.Base(file.Name) != "fleet-plan.sha256" || file.UncompressedSize64 > 256 {
				continue
			}
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

func (c *Client) findApplyRun(ctx context.Context, token, planID string) (*Dispatch, error) {
	var runs struct {
		WorkflowRuns []struct {
			ID           int64  `json:"id"`
			HTMLURL      string `json:"html_url"`
			DisplayTitle string `json:"display_title"`
		} `json:"workflow_runs"`
	}
	query := "?event=workflow_dispatch&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	want := "Apply Norn plan " + planID
	for _, run := range runs.WorkflowRuns {
		if run.DisplayTitle == want {
			return &Dispatch{RunID: run.ID, URL: run.HTMLURL}, nil
		}
	}
	return nil, nil
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
	var response struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
	}
	endpoint := c.repoPath("/contents/"+escapePath(c.cfg.ConfigPath)) + "?ref=" + url.QueryEscape(ref)
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
	return c.request(ctx, token, http.MethodPut, c.repoPath("/contents/"+escapePath(c.cfg.ConfigPath)), map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString(content), "branch": branch, "sha": sha}, nil)
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
	info, err := os.Stat(c.cfg.PrivateKeyFile)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("GitHub App private key is unavailable")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("GitHub App private key must not be group/world accessible")
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
