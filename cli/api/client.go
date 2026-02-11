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
			Timeout: 10 * time.Second,
		},
	}
}

type InfraSpec struct {
	App         string `json:"app"`
	Role        string `json:"role"`
	Core        bool   `json:"core,omitempty"`
	Port        int    `json:"port"`
	Healthcheck string `json:"healthcheck"`
	Hosts       *struct {
		External string `json:"external"`
		Internal string `json:"internal"`
	} `json:"hosts"`
	Services *struct {
		Postgres *struct {
			Database string `json:"database"`
		} `json:"postgres"`
		KV *struct {
			Namespace string `json:"namespace"`
		} `json:"kv"`
		Events *struct {
			Topics []string `json:"topics"`
		} `json:"events"`
	} `json:"services"`
	Secrets []string  `json:"secrets"`
	Repo    *RepoSpec `json:"repo,omitempty"`
}

type RepoSpec struct {
	URL           string `json:"url"`
	Branch        string `json:"branch,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
	AutoDeploy    bool   `json:"autoDeploy,omitempty"`
}

type AppStatus struct {
	Spec       InfraSpec `json:"spec"`
	Healthy    bool      `json:"healthy"`
	Ready      string    `json:"ready"`
	CommitSHA  string    `json:"commitSha"`
	DeployedAt string    `json:"deployedAt"`
	Pods       []Pod     `json:"pods"`
}

type Pod struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Ready  bool   `json:"ready"`
}

type HealthStatus struct {
	Status   string            `json:"status"`
	Services map[string]string `json:"services"`
}

type Deployment struct {
	ID        string `json:"id"`
	AppID     string `json:"appId"`
	CommitSHA string `json:"commitSha"`
	ImageTag  string `json:"imageTag"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	Error     string `json:"error"`
}

type AppDetail struct {
	AppStatus
	Deployments []Deployment `json:"deployments"`
}

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

func (c *Client) GetApp(id string) (*AppDetail, error) {
	var app AppDetail
	if err := c.get("/api/apps/"+id, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) Deploy(appID, commitSHA string) error {
	body := fmt.Sprintf(`{"commitSha":%q}`, commitSHA)
	return c.post("/api/apps/"+appID+"/deploy", body)
}

func (c *Client) Restart(appID string) error {
	return c.post("/api/apps/"+appID+"/restart", "{}")
}

func (c *Client) Rollback(appID string) error {
	return c.post("/api/apps/"+appID+"/rollback", "{}")
}

func (c *Client) Forge(appID string, force bool) error {
	body := "{}"
	if force {
		body = `{"force":true}`
	}
	return c.post("/api/apps/"+appID+"/forge", body)
}

func (c *Client) Teardown(appID string) error {
	return c.post("/api/apps/"+appID+"/teardown", "{}")
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

func (c *Client) ListSecrets(appID string) ([]string, error) {
	var secrets []string
	if err := c.get("/api/apps/"+appID+"/secrets", &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

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
	resp, err := c.HTTPClient.Post(
		c.BaseURL+path,
		"application/json",
		strings.NewReader(body),
	)
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

func (c *Client) WebSocketURL() string {
	base := c.BaseURL
	base = strings.Replace(base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	return base + "/ws"
}
