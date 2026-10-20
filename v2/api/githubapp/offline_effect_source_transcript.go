package githubapp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

// NewOfflineEffectSourceClient constructs the only production client permitted
// to emit an offline source transcript. Authentication and exact run checks
// remain in Client; this client refuses proxies and redirects while TLS binds
// the fixed api.github.com hostname to the system trust store.
func NewOfflineEffectSourceClient(cfg Config) (*Client, error) {
	cfg.Production = true
	if cfg.APIBaseURL != "" && cfg.APIBaseURL != "https://api.github.com" {
		return nil, errors.New("offline effect GitHub API must be api.github.com")
	}
	cfg.APIBaseURL = "https://api.github.com"
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "api.github.com"},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c, err := New(cfg, client)
	if err != nil {
		return nil, err
	}
	c.offlineEffectSourceSafe = true
	return c, nil
}

// OfflineEffectSourceTranscript is a bounded, redacted account of authenticated
// GitHub reads. It contains no installation token or arbitrary response body.
// The validation run must already exist; this method never dispatches Actions.
type OfflineEffectSourceTranscript struct {
	Schema        string `json:"schema"`
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit"`
	Protected     bool   `json:"protected"`
	ValidationRun int64  `json:"validationRun"`
}

// ReadOfflineEffectSourceTranscript verifies the exact protected Fleet main
// tip with an existing successful validate.yml push run, then returns only
// the fields needed for a durable source transcript. Without that run it fails
// closed; local tests cannot be substituted for authenticated GitHub proof.
func (c *Client) ReadOfflineEffectSourceTranscript(ctx context.Context, sha string) ([]byte, error) {
	if c == nil || !c.offlineEffectSourceSafe || (c.cfg.Production && c.cfg.APIBaseURL != "https://api.github.com") ||
		c.cfg.Repository != "antiartificial/norn-fleet" || c.cfg.DefaultBranch != "main" {
		return nil, errors.New("offline effect GitHub source is not the configured Fleet main")
	}
	proof, err := c.VerifyProtectedCommit(ctx, sha, "")
	if err != nil {
		return nil, err
	}
	if proof == nil || proof.SHA != sha || proof.Verdict != CommitVerified || proof.ValidateRun <= 0 {
		return nil, errors.New("offline effect Fleet main source is not independently verified")
	}
	return json.Marshal(OfflineEffectSourceTranscript{
		Schema: "norn.fleet-offline-effect-source/v1", Repository: c.cfg.Repository,
		Branch: c.cfg.DefaultBranch, Commit: sha, Protected: true, ValidationRun: proof.ValidateRun,
	})
}
