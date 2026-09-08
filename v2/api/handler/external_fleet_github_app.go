package handler

// Narrow, request-scoped GitHub App client for external admission. It never
// reuses the write-capable Fleet App and never caches or persists a token.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type externalFleetGitHubAppConfig struct {
	AppID          string
	InstallationID int64
	PrivateKeyFile string
	RepositoryIDs  []string
	APIBaseURL     string
}

type externalFleetGitHubApp struct {
	cfg    externalFleetGitHubAppConfig
	client *http.Client
}

func newExternalFleetGitHubApp(cfg externalFleetGitHubAppConfig, client *http.Client) (*externalFleetGitHubApp, error) {
	if strings.TrimSpace(cfg.AppID) == "" || cfg.InstallationID <= 0 || len(cfg.RepositoryIDs) == 0 || strings.TrimSpace(cfg.PrivateKeyFile) == "" || externalVerifierSecretFile(cfg.PrivateKeyFile) != nil {
		return nil, errors.New("read-only GitHub App configuration is incomplete")
	}
	if client == nil {
		client = &http.Client{Timeout: externalFleetHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.github.com"
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/")
	return &externalFleetGitHubApp{cfg, client}, nil
}

func (c *externalFleetGitHubApp) appJWT() (string, error) {
	fd, err := syscall.Open(c.cfg.PrivateKeyFile, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("read-only GitHub App key unavailable")
	}
	file := os.NewFile(uintptr(fd), c.cfg.PrivateKeyFile)
	defer file.Close()
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0o077 != 0 {
		return "", errors.New("read-only GitHub App key unavailable")
	}
	pem, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(pem) > 64<<10 {
		return "", errors.New("read-only GitHub App key unavailable")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return "", errors.New("read-only GitHub App key invalid")
	}
	now := time.Now().UTC()
	return jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": c.cfg.AppID}).SignedString(key)
}

func (c *externalFleetGitHubApp) request(ctx context.Context, token, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, externalFleetEvidenceMaxBody)).Decode(out)
}

func (c *externalFleetGitHubApp) token(ctx context.Context) (string, error) {
	jwtValue, err := c.appJWT()
	if err != nil {
		return "", err
	}
	var installation struct {
		RepositorySelection string            `json:"repository_selection"`
		Permissions         map[string]string `json:"permissions"`
	}
	if err = c.request(ctx, jwtValue, http.MethodGet, "/app/installations/"+strconv.FormatInt(c.cfg.InstallationID, 10), nil, &installation); err != nil {
		return "", err
	}
	if installation.RepositorySelection != "selected" || !externalFleetReadOnlyPermissions(installation.Permissions) {
		return "", errors.New("read-only GitHub App installation policy rejected")
	}
	var repos struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	var minted struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err = c.request(ctx, jwtValue, http.MethodPost, "/app/installations/"+strconv.FormatInt(c.cfg.InstallationID, 10)+"/access_tokens", map[string]any{"permissions": map[string]string{"metadata": "read", "actions": "read", "attestations": "read"}}, &minted); err != nil {
		return "", err
	}
	if minted.Token == "" || !minted.ExpiresAt.After(time.Now().Add(time.Minute)) {
		return "", errors.New("read-only GitHub App mint rejected")
	}
	if err = c.request(ctx, minted.Token, http.MethodGet, "/installation/repositories", nil, &repos); err != nil {
		return "", err
	}
	if repos.TotalCount != len(c.cfg.RepositoryIDs) || !externalFleetRepositoryIDsMatch(repos.Repositories, c.cfg.RepositoryIDs) {
		return "", errors.New("read-only GitHub App repository selection rejected")
	}
	return minted.Token, nil
}

func externalFleetReadOnlyPermissions(p map[string]string) bool {
	return p["metadata"] == "read" && p["actions"] == "read" && (p["attestations"] == "read" || p["artifact_metadata"] == "read") && len(p) >= 3 && p["contents"] == "" && p["pull_requests"] == "" && p["checks"] == "" && p["administration"] == ""
}
func externalFleetRepositoryIDsMatch(got []struct {
	ID int64 `json:"id"`
}, want []string) bool {
	seen := map[string]bool{}
	for _, r := range got {
		seen[strconv.FormatInt(r.ID, 10)] = true
	}
	if len(seen) != len(want) {
		return false
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}
