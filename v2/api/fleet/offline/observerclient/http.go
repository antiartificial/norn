package observerclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/fleet/offline"
)

// HTTPConfig pins the exact target Fleet controller reached from the management
// observer. ServerName is the certificate DNS name; Address is a literal,
// independently pinned IP:port. They are deliberately separate so DNS cannot
// silently redirect an observation to another machine. RootCAs nil uses the
// operating system's trust roots. The token belongs to the device-bound
// fleet:observe principal and cannot issue a challenge.
type HTTPConfig struct {
	ServerName     string
	Address        string
	RootCAs        *x509.CertPool
	ObserverBearer string
	Timeout        time.Duration
}

type HTTPClient struct {
	client         *http.Client
	base           string
	observerBearer string
}

var pinnedDNSName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$`)

func NewHTTPClient(c HTTPConfig) (*HTTPClient, error) {
	if !pinnedDNSName.MatchString(c.ServerName) || strings.Contains(c.ServerName, "..") ||
		!strings.Contains(c.ServerName, ".") || net.ParseIP(c.ServerName) != nil ||
		c.ObserverBearer == "" || strings.ContainsAny(c.ObserverBearer, "\r\n\t ") {
		return nil, errors.New("terminal observer HTTP identity is incomplete")
	}
	host, port, err := net.SplitHostPort(c.Address)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("terminal observer target must be a pinned IP and port")
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	if c.Timeout < 0 || c.Timeout > 30*time.Second {
		return nil, errors.New("terminal observer timeout is outside bounded range")
	}
	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName, RootCAs: c.RootCAs},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", c.Address)
		},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 8 * 1024,
	}
	return &HTTPClient{
		client: &http.Client{Transport: transport, Timeout: c.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base:           "https://" + c.ServerName,
		observerBearer: c.ObserverBearer,
	}, nil
}

func attemptPath(plan, attempt string) (string, error) {
	planUUID, planErr := uuid.Parse(plan)
	attemptUUID, attemptErr := uuid.Parse(attempt)
	if planErr != nil || attemptErr != nil || planUUID.String() != plan || attemptUUID.String() != attempt {
		return "", errors.New("terminal observer requires exact UUID plan and attempt")
	}
	return "/api/v1/fleet/offline/attempts/" + plan + "/" + attempt, nil
}

func (c *HTTPClient) ReadConsumedGrant(ctx context.Context, plan, attempt string) (offline.EffectGrant, error) {
	var out struct {
		AuthorizesEffect bool                `json:"authorizesEffect"`
		Grant            offline.EffectGrant `json:"grant"`
	}
	err := c.call(ctx, http.MethodGet, plan, attempt, "/terminal-observer/grant", nil, c.observerBearer, http.StatusOK, "grant", &out)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if out.Grant.PlanID != plan || out.Grant.AttemptID != attempt || out.Grant.Phase != "consumed" || out.Grant.ConsumedAt == nil || out.Grant.SchemaVersion != offline.EffectGrantSchema {
		return offline.EffectGrant{}, offline.ErrTerminalProof
	}
	return out.Grant, nil
}

func (c *HTTPClient) ReadTerminal(ctx context.Context, plan, attempt string) (offline.TerminalEvidence, error) {
	var out struct {
		AuthorizesEffect bool                     `json:"authorizesEffect"`
		TerminalEvidence offline.TerminalEvidence `json:"terminalEvidence"`
	}
	err := c.call(ctx, http.MethodGet, plan, attempt, "/terminal-observer/evidence", nil, c.observerBearer, http.StatusOK, "terminalEvidence", &out)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if out.TerminalEvidence.PlanID != plan || out.TerminalEvidence.AttemptID != attempt || out.TerminalEvidence.SchemaVersion != offline.TerminalSchema {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	return out.TerminalEvidence, nil
}

func (c *HTTPClient) ReadChallenge(ctx context.Context, plan, attempt string) (offline.TerminalChallengeV1, error) {
	var out struct {
		AuthorizesEffect bool                        `json:"authorizesEffect"`
		Challenge        offline.TerminalChallengeV1 `json:"challenge"`
	}
	err := c.call(ctx, http.MethodGet, plan, attempt, "/terminal-observer/challenge", nil, c.observerBearer, http.StatusOK, "challenge", &out)
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	if out.Challenge.PlanID != plan || out.Challenge.AttemptID != attempt || out.Challenge.SchemaVersion != offline.TerminalChallengeSchemaV1 {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalProof
	}
	return out.Challenge, nil
}

func (c *HTTPClient) SubmitAttestation(ctx context.Context, plan, attempt string, signed offline.SignedTerminalAttestationV1) (offline.TerminalEvidence, error) {
	if signed.Attestation.PlanID != plan || signed.Attestation.AttemptID != attempt || uuid.Validate(signed.Attestation.ChallengeID) != nil {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	body, err := json.Marshal(signed)
	if err != nil || len(body) > 32*1024 {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	var out struct {
		AuthorizesEffect bool                     `json:"authorizesEffect"`
		TerminalEvidence offline.TerminalEvidence `json:"terminalEvidence"`
	}
	err = c.call(ctx, http.MethodPut, plan, attempt, "/terminal-challenges/"+signed.Attestation.ChallengeID+"/attestation", body, c.observerBearer, http.StatusOK, "terminalEvidence", &out)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if out.TerminalEvidence.PlanID != plan || out.TerminalEvidence.AttemptID != attempt || out.TerminalEvidence.ChallengeID != signed.Attestation.ChallengeID || out.TerminalEvidence.SchemaVersion != offline.TerminalSchema {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	return out.TerminalEvidence, nil
}

func (c *HTTPClient) call(ctx context.Context, method, plan, attempt, suffix string, body []byte, bearer string, expected int, field string, dest any) error {
	if c == nil || c.client == nil {
		return errors.New("terminal observer HTTP client is unavailable")
	}
	path, err := attemptPath(plan, attempt)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.base + path + suffix)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("terminal observer target request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		// A generic reverse-proxy or missing-route 404 is not an authoritative
		// absence signal. Only the route's exact JSON error envelope is.
		if method == http.MethodGet && resp.StatusCode == http.StatusNotFound &&
			resp.Header.Get("Content-Type") == "application/problem+json" &&
			resp.Header.Get("Cache-Control") == "private, no-store" {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
			var problem map[string]json.RawMessage
			decodeErr := json.Unmarshal(raw, &problem)
			canonical, marshalErr := json.Marshal(problem)
			if readErr == nil && len(raw) <= 4096 && decodeErr == nil &&
				marshalErr == nil && bytes.Equal(bytes.TrimSuffix(raw, []byte("\n")), canonical) && len(problem) >= 3 &&
				string(problem["type"]) == `"https://norn.dev/problems/offline_terminal_not_found"` &&
				string(problem["status"]) == `404` && string(problem["code"]) == `"offline_terminal_not_found"` {
				return ErrNotFound
			}
		}
		return fmt.Errorf("terminal observer target rejected %s: HTTP %d", method, resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "private, no-store" ||
		resp.Header.Get("Content-Encoding") != "" {
		return errors.New("terminal observer target returned unexpected response headers")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(raw) == 0 || len(raw) > 64*1024 || !json.Valid(raw) {
		return errors.New("terminal observer target returned invalid or oversized JSON")
	}
	// Canonical whole-response equality rejects duplicate keys, extra fields,
	// alternate encodings, trailing bytes, and a true authorizesEffect flag.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return errors.New("terminal observer target returned ambiguous JSON")
	}
	canonical, err := json.Marshal(dest)
	if err != nil || !bytes.Equal(bytes.TrimSuffix(raw, []byte("\n")), canonical) || !bytes.Contains(raw, []byte(`"`+field+`":`)) || !bytes.Contains(raw, []byte(`"authorizesEffect":false`)) {
		return errors.New("terminal observer target returned noncanonical JSON")
	}
	return nil
}
