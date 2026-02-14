package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
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

type InfraSpec struct {
	App       string              `json:"name"`
	Processes map[string]Process  `json:"processes"`
	Repo      *RepoSpec           `json:"repo,omitempty"`
}

type Process struct {
	Port     int    `json:"port,omitempty"`
	Command  string `json:"command,omitempty"`
	Schedule string `json:"schedule,omitempty"`
}

type RepoSpec struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
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
}

type Deployment struct {
	ID        string `json:"id"`
	App       string `json:"app"`
	CommitSHA string `json:"commitSha"`
	ImageTag  string `json:"imageTag"`
	SagaID    string `json:"sagaId"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
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
	resp, err := c.HTTPClient.Get(c.BaseURL + "/api/apps/" + appID + "/logs?follow=true")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
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

func (c *Client) Forge(appID string) error {
	return c.post("/api/apps/"+appID+"/forge", "{}")
}

func (c *Client) Teardown(appID string) error {
	return c.post("/api/apps/"+appID+"/teardown", "{}")
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

func (c *Client) WebSocketURL() string {
	base := c.BaseURL
	base = strings.Replace(base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	return base + "/ws"
}

// HTTP helpers

func (c *Client) get(path string, v any) error {
	resp, err := c.HTTPClient.Get(c.BaseURL + path)
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
	resp, err := c.HTTPClient.Post(c.BaseURL+path, "application/json", strings.NewReader(body))
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
	resp, err := c.HTTPClient.Post(c.BaseURL+path, "application/json", strings.NewReader(body))
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
