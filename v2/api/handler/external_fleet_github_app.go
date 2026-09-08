package handler

// Narrow, request-scoped GitHub App client for external admission. It never
// reuses the write-capable Fleet App and never caches or persists a token.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/golang/snappy"
)

const (
	externalFleetMaxAttestations       = 30
	externalFleetMaxAttestationPages   = 4
	externalFleetBundleCompressedMax   = 1 << 20
	externalFleetBundleDecompressedMax = 4 << 20
)

// GitHub returns a short-lived Azure Blob SAS capability. It is deliberately
// opaque: its query is neither parsed, logged, persisted, nor sent back to
// GitHub. The suffix is the reviewed Azure Blob service boundary; tests inject
// an exact host list through the internal configuration.
var externalFleetDefaultStorageHosts = []string{".blob.core.windows.net"}

type externalFleetGitHubAppConfig struct {
	AppID          string
	InstallationID int64
	PrivateKeyFile string
	RepositoryIDs  []string
	APIBaseURL     string
	StorageHosts   []string
}

// externalFleetBundle retains exact decompressed bytes for gh. Parsed fields
// are a separate typed view used solely for local fail-closed binding checks.
type externalFleetBundle struct {
	Raw       json.RawMessage
	SHA256    string
	Predicate string
}

type externalFleetListAttestation struct {
	RepositoryID int64           `json:"repository_id"`
	BundleURL    string          `json:"bundle_url"`
	Initiator    json.RawMessage `json:"initiator"`
}

func (c *externalFleetGitHubApp) attestations(ctx context.Context, token string, receipt ExternalFleetDeploymentReceipt) (map[string]json.RawMessage, error) {
	repo := receipt.Candidate.Repository
	digest := strings.TrimPrefix(receipt.Candidate.Attestation.SubjectDigest, "sha256:")
	repositoryID, err := strconv.ParseInt(receipt.Candidate.RepositoryID, 10, 64)
	if !validExternalRepository(repo) || len(digest) != 64 || err != nil || repositoryID <= 0 || !externalFleetRepositoryIDAllowed(receipt.Candidate.RepositoryID, c.cfg.RepositoryIDs) {
		return nil, errors.New("attestation subject or repository binding invalid")
	}
	want := map[string]string{
		"https://slsa.dev/provenance/v1": receipt.AttestationBundleSHA256,
		"https://spdx.dev/Document/v2.3": receipt.SBOMBundleSHA256,
	}
	if !sha256HexPattern.MatchString(want["https://slsa.dev/provenance/v1"]) || !sha256HexPattern.MatchString(want["https://spdx.dev/Document/v2.3"]) || want["https://slsa.dev/provenance/v1"] == want["https://spdx.dev/Document/v2.3"] {
		return nil, errors.New("attestation receipt digest binding invalid")
	}
	found := make(map[string]bool, len(want))
	seenCapabilities := make(map[string]bool, externalFleetMaxAttestations*externalFleetMaxAttestationPages)
	bundles := make(map[string]json.RawMessage, len(want))
	for _, wantedPredicate := range []struct{ predicate, filter string }{{"https://slsa.dev/provenance/v1", "provenance"}, {"https://spdx.dev/Document/v2.3", "sbom"}} {
		listed := false
		for page := 1; page <= externalFleetMaxAttestationPages; page++ {
			var response struct {
				Attestations []externalFleetListAttestation `json:"attestations"`
			}
			path := "/repos/" + repo + "/attestations/sha256:" + digest + "?per_page=" + strconv.Itoa(externalFleetMaxAttestations) + "&predicate_type=" + wantedPredicate.filter + "&page=" + strconv.Itoa(page)
			if err := c.request(ctx, token, http.MethodGet, path, nil, &response); err != nil {
				return nil, errors.New("attestation list unavailable")
			}
			if len(response.Attestations) > externalFleetMaxAttestations {
				return nil, errors.New("attestation list cardinality invalid")
			}
			if len(response.Attestations) == 0 {
				break
			}
			listed = true
			for _, item := range response.Attestations {
				if item.RepositoryID != repositoryID || !c.validStorageBundleURL(item.BundleURL) || seenCapabilities[item.BundleURL] {
					return nil, errors.New("attestation capability binding invalid")
				}
				seenCapabilities[item.BundleURL] = true
				bundle, err := c.fetchBundle(ctx, item.BundleURL, digest)
				if err != nil {
					return nil, errors.New("attestation bundle unavailable")
				}
				if bundle.Predicate != wantedPredicate.predicate {
					return nil, errors.New("attestation predicate binding invalid")
				}
				if bundle.SHA256 != want[wantedPredicate.predicate] {
					continue
				}
				if found[bundle.Predicate] {
					return nil, errors.New("attestation bundle binding invalid")
				}
				found[bundle.Predicate] = true
				bundles[bundle.Predicate] = bundle.Raw
			}
			if len(response.Attestations) < externalFleetMaxAttestations {
				break
			}
		}
		if !listed {
			return nil, errors.New("attestation list cardinality invalid")
		}
	}
	if !found["https://slsa.dev/provenance/v1"] || !found["https://spdx.dev/Document/v2.3"] {
		return nil, errors.New("attestation receipt digest binding missing")
	}
	return bundles, nil
}

func externalFleetRepositoryIDAllowed(id string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == id {
			return true
		}
	}
	return false
}

func (c *externalFleetGitHubApp) validStorageBundleURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range c.cfg.StorageHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed != "" && (host == strings.TrimPrefix(allowed, ".") || strings.HasSuffix(host, allowed)) {
			return true
		}
	}
	return false
}

func (c *externalFleetGitHubApp) fetchBundle(ctx context.Context, capability, digest string) (externalFleetBundle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, capability, nil)
	if err != nil {
		return externalFleetBundle{}, err
	}
	// Do not inherit Authorization, Accept, or GitHub API headers.
	res, err := c.storageHTTPClient().Do(req)
	if err != nil {
		return externalFleetBundle{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]), "application/x-snappy") {
		return externalFleetBundle{}, errors.New("storage response rejected")
	}
	compressed, err := io.ReadAll(io.LimitReader(res.Body, externalFleetBundleCompressedMax+1))
	if err != nil || len(compressed) > externalFleetBundleCompressedMax {
		return externalFleetBundle{}, errors.New("compressed bundle exceeds limit")
	}
	decodedLen, err := snappy.DecodedLen(compressed)
	if err != nil || decodedLen <= 0 || decodedLen > externalFleetBundleDecompressedMax {
		return externalFleetBundle{}, errors.New("decompressed bundle exceeds limit")
	}
	raw, err := snappy.Decode(make([]byte, 0, decodedLen), compressed)
	if err != nil || len(raw) != decodedLen {
		return externalFleetBundle{}, errors.New("decompressed bundle invalid")
	}
	var parsed struct {
		DSSEEnvelope struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return externalFleetBundle{}, errors.New("bundle JSON invalid")
	}
	payload, err := base64.StdEncoding.DecodeString(parsed.DSSEEnvelope.Payload)
	if err != nil {
		return externalFleetBundle{}, errors.New("bundle DSSE invalid")
	}
	var statement struct {
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}
	if json.Unmarshal(payload, &statement) != nil || statement.PredicateType == "" || !externalFleetStatementSubject(statement.Subject, digest) {
		return externalFleetBundle{}, errors.New("bundle statement invalid")
	}
	canonicalDigest, err := externalFleetCanonicalBundleDigest(raw)
	if err != nil {
		return externalFleetBundle{}, errors.New("bundle canonical form invalid")
	}
	return externalFleetBundle{Raw: append(json.RawMessage(nil), raw...), SHA256: canonicalDigest, Predicate: statement.PredicateType}, nil
}

// externalFleetCanonicalBundleDigest is the stable receipt/handoff binding.
// It parses the complete Sigstore bundle and marshals the same JSON value with
// deterministic object-key ordering before hashing, so insignificant transport
// whitespace (including actions/attest's trailing EOL) cannot change identity.
func externalFleetCanonicalBundleDigest(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func externalFleetStatementSubject(subject []struct {
	Digest map[string]string `json:"digest"`
}, digest string) bool {
	for _, s := range subject {
		if s.Digest["sha256"] == digest {
			return true
		}
	}
	return false
}

type externalFleetGitHubApp struct {
	cfg           externalFleetGitHubAppConfig
	client        *http.Client
	storageClient *http.Client
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
	if len(cfg.StorageHosts) == 0 {
		cfg.StorageHosts = append([]string(nil), externalFleetDefaultStorageHosts...)
	}
	storage, err := newExternalFleetStorageHTTPClient()
	if err != nil {
		return nil, err
	}
	return &externalFleetGitHubApp{cfg: cfg, client: client, storageClient: storage}, nil
}

func newExternalFleetStorageHTTPClient() (*http.Client, error) {
	dialer := &net.Dialer{Timeout: externalFleetHTTPTimeout / 2, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: externalFleetHTTPTimeout / 2, ResponseHeaderTimeout: externalFleetHTTPTimeout / 2, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("storage host resolution failed")
		}
		for _, ip := range ips {
			if externalFleetStoragePublicIP(ip) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
		}
		return nil, errors.New("storage host resolved to a forbidden address")
	}}
	return &http.Client{Transport: transport, Timeout: externalFleetHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func externalFleetStoragePublicIP(ip netip.Addr) bool { return externalPublicIP(ip) }

func (c *externalFleetGitHubApp) storageHTTPClient() *http.Client {
	if c.storageClient != nil {
		clone := *c.storageClient
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &clone
	}
	client, _ := newExternalFleetStorageHTTPClient()
	return client
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

func (c *externalFleetGitHubApp) noRedirectClient() *http.Client {
	clone := *c.client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
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
	res, err := c.noRedirectClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned %d", res.StatusCode)
	}
	return decodeExternalFleetGitHubJSON(res.Body, externalFleetEvidenceMaxBody, out)
}

func decodeExternalFleetGitHubJSON(body io.Reader, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return errors.New("GitHub response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing GitHub JSON")
	}
	return nil
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
	if p["metadata"] != "read" || p["actions"] != "read" || (p["attestations"] != "read" && p["artifact_metadata"] != "read") {
		return false
	}
	for name, value := range p {
		if value != "read" || (name != "metadata" && name != "actions" && name != "attestations" && name != "artifact_metadata") {
			return false
		}
	}
	return true
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
