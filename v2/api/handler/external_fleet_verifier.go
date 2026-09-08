package handler

// This adapter deliberately speaks only to two fixed authorities: GitHub and
// the separately deployed, read-only Fleet evidence service. It never follows
// receipt-supplied URLs, never sends the raw Norn nonce off-host, and keeps
// credentials in owner-only files rather than the database or operation log.

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/config"
)

const (
	externalFleetEvidenceMaxBody = 1 << 20
	externalFleetHTTPTimeout     = 12 * time.Second
)

// ExternalFleetVerificationError is deliberately terse. The handler only
// returns errors carrying this type, preventing upstream credentials, nonce
// hashes, or raw substrate responses from escaping to a client.
type ExternalFleetVerificationError struct{ Code string }

func (e *ExternalFleetVerificationError) Error() string {
	return "external Fleet verification failed: " + e.Code
}
func (e *ExternalFleetVerificationError) SafeExternalVerificationError() string {
	if e == nil || e.Code == "" {
		return "independent evidence was rejected"
	}
	return "independent evidence rejected (" + e.Code + ")"
}

func externalVerifierErr(code string) error { return &ExternalFleetVerificationError{Code: code} }

// ExternalFleetAttestationVerifier verifies the GitHub-hosted SLSA and SPDX
// statements. Command execution is direct (never a shell) and receives only
// values which have already passed the immutable receipt grammar.
type ExternalFleetAttestationVerifier interface {
	Verify(context.Context, string, ExternalFleetDeploymentVerificationRequest) error
}

type ExternalFleetGitHubRunVerifier interface {
	Verify(context.Context, string, CIIdentity) error
}

type externalFleetCommandAttestationVerifier struct{ path string }

type externalFleetLimitedWriter struct{ remaining int }

func (w *externalFleetLimitedWriter) Write(value []byte) (int, error) {
	if len(value) > w.remaining {
		w.remaining = 0
		return 0, errors.New("attestation output exceeds limit")
	}
	w.remaining -= len(value)
	return len(value), nil
}

type externalFleetGitHubRunVerifier struct{ client *http.Client }

func (v externalFleetGitHubRunVerifier) Verify(ctx context.Context, token string, ci CIIdentity) error {
	if strings.TrimSpace(token) == "" || !validGitHubNumericID(ci.RunID) || !validGitHubNumericID(ci.RunAttempt) || !validExternalRepository(ci.Repository) {
		return externalVerifierErr("github-run-binding")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+ci.Repository+"/actions/runs/"+ci.RunID, nil)
	if err != nil {
		return externalVerifierErr("github-run-request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := v.client.Do(req)
	if err != nil {
		return externalVerifierErr("github-run-unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return externalVerifierErr("github-run-rejected")
	}
	var run struct {
		ID         int64  `json:"id"`
		RunAttempt int64  `json:"run_attempt"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Event      string `json:"event"`
		Path       string `json:"path"`
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		HeadRepository struct {
			FullName string `json:"full_name"`
		} `json:"head_repository"`
	}
	if err := decodeGitHubRunJSON(response.Body, &run); err != nil {
		return externalVerifierErr("github-run-invalid")
	}
	if fmt.Sprintf("%d", run.ID) != ci.RunID || fmt.Sprintf("%d", run.RunAttempt) != ci.RunAttempt || !activeGitHubRunStatus(run.Status, run.Conclusion) || run.Repository.FullName != ci.Repository || run.HeadRepository.FullName != ci.Repository || run.HeadSHA != ci.SHA || !githubRunRefMatches(ci.Ref, run.HeadBranch) || (run.Event != "workflow_dispatch" && run.Event != "workflow_run") || !githubRunWorkflowMatches(ci, run.Path) {
		return externalVerifierErr("github-run-mismatch")
	}
	return nil
}

func (v externalFleetCommandAttestationVerifier) Verify(ctx context.Context, token string, request ExternalFleetDeploymentVerificationRequest) error {
	if strings.TrimSpace(token) == "" {
		return externalVerifierErr("github-token-unavailable")
	}
	candidate := request.Receipt.Candidate
	signerPath, signerSHA, ok := strings.Cut(candidate.SignerWorkflowRef, "@")
	if !ok || signerSHA != candidate.SignerWorkflowSHA || signerPath == "" {
		return externalVerifierErr("github-signer-binding")
	}
	if candidate.RepositoryVisibility != "" && candidate.RepositoryVisibility != "public" {
		return externalVerifierErr("github-private-attestation-adapter-required")
	}
	for _, predicate := range []string{"https://slsa.dev/provenance/v1", "https://spdx.dev/Document/v2.3"} {
		args := []string{"attestation", "verify", "oci://" + request.Receipt.Artifact, "--repo", candidate.Repository, "--hostname", "github.com", "--signer-workflow", "github.com/" + signerPath, "--signer-digest", signerSHA, "--cert-identity", "https://github.com/" + candidate.SignerWorkflowRef, "--source-digest", request.Receipt.SourceSHA, "--source-ref", candidate.Ref, "--predicate-type", predicate, "--cert-oidc-issuer", "https://token.actions.githubusercontent.com"}
		command := exec.CommandContext(ctx, v.path, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "GH_TOKEN=" + token, "GH_PROMPT_DISABLED=1", "NO_COLOR=1"}
		bounded := &externalFleetLimitedWriter{remaining: externalFleetEvidenceMaxBody}
		command.Stdout, command.Stderr = bounded, bounded
		if err := command.Run(); err != nil {
			return externalVerifierErr("github-attestation")
		}
	}
	return nil
}

// ExternalFleetDeploymentVerifierConfig is intentionally not the app config:
// it keeps all adapter inputs explicit and makes construction easy to test.
type ExternalFleetDeploymentVerifierConfig struct {
	EvidenceURL          string
	EvidenceTokenFile    string
	GitHubTokenFile      string
	GitHubCLIPath        string
	PublicBaseURL        string
	EvidenceAllowedCIDRs []string
}

type ExternalFleetDeploymentLiveVerifier struct {
	evidenceURL       *url.URL
	publicURL         *url.URL
	evidenceTokenFile string
	githubTokenFile   string
	httpClient        *http.Client
	attest            ExternalFleetAttestationVerifier
	githubRun         ExternalFleetGitHubRunVerifier
}

// NewExternalFleetDeploymentLiveVerifier refuses partial configuration. The
// evidence URL is a single HTTPS origin chosen by the operator; every request
// is derived from it, not a receipt field, which prevents SSRF.
func NewExternalFleetDeploymentLiveVerifier(cfg ExternalFleetDeploymentVerifierConfig, client *http.Client, attest ExternalFleetAttestationVerifier) (*ExternalFleetDeploymentLiveVerifier, error) {
	evidenceURL, err := externalVerifierURL(cfg.EvidenceURL)
	if err != nil {
		return nil, fmt.Errorf("external Fleet evidence URL: %w", err)
	}
	publicURL, err := externalVerifierURL(cfg.PublicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("external Fleet public URL: %w", err)
	}
	if err := externalVerifierSecretFile(cfg.EvidenceTokenFile); err != nil {
		return nil, fmt.Errorf("external Fleet evidence token: %w", err)
	}
	if err := externalVerifierSecretFile(cfg.GitHubTokenFile); err != nil {
		return nil, fmt.Errorf("external Fleet GitHub token: %w", err)
	}
	if sameExternalVerifierSecret(cfg.EvidenceTokenFile, cfg.GitHubTokenFile) {
		return nil, fmt.Errorf("external Fleet evidence and GitHub credentials must be distinct files")
	}
	if attest == nil {
		if !filepath.IsAbs(cfg.GitHubCLIPath) {
			return nil, fmt.Errorf("external Fleet GitHub CLI path must be absolute")
		}
		info, statErr := os.Lstat(cfg.GitHubCLIPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode()&0o111 == 0 {
			return nil, fmt.Errorf("external Fleet GitHub CLI is unavailable")
		}
		attest = externalFleetCommandAttestationVerifier{path: cfg.GitHubCLIPath}
	}
	if client == nil {
		client, err = newExternalFleetHTTPClient(evidenceURL.Hostname(), cfg.EvidenceAllowedCIDRs)
		if err != nil {
			return nil, err
		}
	}
	return &ExternalFleetDeploymentLiveVerifier{evidenceURL: evidenceURL, publicURL: publicURL, evidenceTokenFile: cfg.EvidenceTokenFile, githubTokenFile: cfg.GitHubTokenFile, httpClient: client, attest: attest, githubRun: externalFleetGitHubRunVerifier{client: client}}, nil
}

func ExternalFleetDeploymentVerifierFromConfig(cfg *config.Config) (*ExternalFleetDeploymentLiveVerifier, error) {
	if cfg == nil {
		return nil, fmt.Errorf("external Fleet verifier is not configured")
	}
	return NewExternalFleetDeploymentLiveVerifier(ExternalFleetDeploymentVerifierConfig{EvidenceURL: cfg.ExternalFleetVerifierURL, EvidenceTokenFile: cfg.ExternalFleetVerifierTokenFile, GitHubTokenFile: cfg.ExternalFleetGitHubTokenFile, GitHubCLIPath: cfg.ExternalFleetGitHubCLIPath, PublicBaseURL: cfg.ExternalFleetPublicBaseURL, EvidenceAllowedCIDRs: cfg.ExternalFleetEvidenceAllowedCIDRs}, nil, nil)
}

func externalVerifierURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be a clean HTTPS URL")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return nil, errors.New("must not use a private or loopback IP")
	}
	return u, nil
}

// newExternalFleetHTTPClient validates the address selected at every TCP dial,
// not only the URL hostname. This makes redirects and DNS rebinding fail
// closed. The evidence host may use only the reviewed private CIDRs; every
// other host (public ingress and api.github.com) must resolve to a public IP.
func newExternalFleetHTTPClient(evidenceHost string, allowedRaw []string) (*http.Client, error) {
	allowed := make([]netip.Prefix, 0, len(allowedRaw))
	for _, raw := range allowedRaw {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !prefix.IsValid() {
			return nil, fmt.Errorf("external Fleet evidence CIDR is invalid")
		}
		allowed = append(allowed, prefix)
	}
	if evidenceHost == "" || len(allowed) == 0 {
		return nil, fmt.Errorf("external Fleet evidence host and allowed CIDRs are required")
	}
	dialer := &net.Dialer{Timeout: externalFleetHTTPTimeout / 2, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: externalFleetHTTPTimeout / 2, ResponseHeaderTimeout: externalFleetHTTPTimeout / 2, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("external host resolution failed")
		}
		for _, ip := range ips {
			if host == evidenceHost {
				for _, prefix := range allowed {
					if prefix.Contains(ip) {
						return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
					}
				}
				continue
			}
			if externalPublicIP(ip) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
		}
		return nil, errors.New("external host resolved to a forbidden address")
	}}
	return &http.Client{Transport: transport, Timeout: externalFleetHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func externalPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, blocked := range []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10")} {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}

func externalVerifierSecretFile(path string) error {
	canonical, err := filepath.EvalSymlinks(strings.TrimSpace(path))
	if err != nil {
		return errors.New("must use a canonical non-symlink path")
	}
	rawInfo, err := os.Lstat(strings.TrimSpace(path))
	if err != nil || rawInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("must use a canonical non-symlink path")
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("must be an owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("must be owned by the API user")
	}
	return nil
}

func sameExternalVerifierSecret(left, right string) bool {
	leftPath, leftErr := filepath.EvalSymlinks(strings.TrimSpace(left))
	rightPath, rightErr := filepath.EvalSymlinks(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return true
	}
	leftInfo, leftErr := os.Stat(leftPath)
	rightInfo, rightErr := os.Stat(rightPath)
	if leftErr != nil || rightErr != nil {
		return true
	}
	leftStat, leftOK := leftInfo.Sys().(*syscall.Stat_t)
	rightStat, rightOK := rightInfo.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev && leftStat.Ino == rightStat.Ino
}

func readExternalVerifierSecret(path string) (string, error) {
	if err := externalVerifierSecretFile(path); err != nil {
		return "", err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var opened syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil || opened.Uid != uint32(os.Getuid()) || opened.Mode&0o077 != 0 {
		return "", errors.New("unavailable")
	}
	value, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(value) == 0 || len(value) > 8192 {
		return "", errors.New("unavailable")
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("invalid")
	}
	return secret, nil
}

type externalFleetEvidenceRequest struct {
	Repository  string                         `json:"repository"`
	Receipt     ExternalFleetDeploymentReceipt `json:"receipt"`
	NonceSHA256 string                         `json:"nonceSha256"`
}

// externalFleetEvidence is the constrained response contract of the runner's
// read-only evidence service. The booleans are intentionally absent: all
// claimed facts are cross-checked as identities and canonical field values.
type externalFleetEvidence struct {
	Repository          string                              `json:"repository"`
	Verification        ExternalFleetDeploymentVerification `json:"verification"`
	FixtureHCLSHA256    map[string]string                   `json:"fixtureHclSha256"`
	Canonical           map[string]string                   `json:"canonical"`
	PlanAttemptID       string                              `json:"planAttemptId"`
	CheckpointAttemptID string                              `json:"checkpointAttemptId"`
	NonceSHA256         string                              `json:"nonceSha256"`
	NonceWrittenAt      time.Time                           `json:"nonceWrittenAt"`
	NonceReadAt         time.Time                           `json:"nonceReadAt"`
}

func (v *ExternalFleetDeploymentLiveVerifier) VerifyExternalFleetDeployment(ctx context.Context, request ExternalFleetDeploymentVerificationRequest) (*ExternalFleetDeploymentVerification, error) {
	ctx, cancel := context.WithTimeout(ctx, externalFleetHTTPTimeout)
	defer cancel()
	if v == nil || v.evidenceURL == nil || v.publicURL == nil || v.attest == nil || v.githubRun == nil {
		return nil, externalVerifierErr("unconfigured")
	}
	if request.CI.Provider != "github-actions" || request.CI.Repository == "" || request.CI.RunID != request.Receipt.Fleet.ApplyRunID || request.CI.RunAttempt != request.Receipt.Fleet.ApplyRunAttempt {
		return nil, externalVerifierErr("ci-binding")
	}
	githubToken, err := readExternalVerifierSecret(v.githubTokenFile)
	if err != nil {
		return nil, externalVerifierErr("github-token-unavailable")
	}
	if err := v.attest.Verify(ctx, githubToken, request); err != nil {
		return nil, err
	}
	if err := v.githubRun.Verify(ctx, githubToken, request.CI); err != nil {
		return nil, err
	}
	nonce, err := externalAdmissionNonceFromReceipt(request.Receipt.Nonce)
	if err != nil {
		return nil, externalVerifierErr("nonce")
	}
	nonceDigest := nonce.sha256()
	evidenceToken, err := readExternalVerifierSecret(v.evidenceTokenFile)
	if err != nil {
		return nil, externalVerifierErr("evidence-token-unavailable")
	}
	copyReceipt := redactExternalFleetReceipt(request.Receipt)
	body, err := json.Marshal(externalFleetEvidenceRequest{Repository: request.CI.Repository, Receipt: copyReceipt, NonceSHA256: nonceDigest})
	if err != nil {
		return nil, externalVerifierErr("request")
	}
	evidence, err := v.postEvidence(ctx, body, evidenceToken)
	if err != nil {
		return nil, err
	}
	if err := v.probePublicVersion(ctx, request.Receipt.SourceSHA); err != nil {
		return nil, err
	}
	if evidence.Verification.PublicHTTPSVersion != v.publicEndpoint("version") {
		return nil, externalVerifierErr("public-version-binding")
	}
	if err := validateExternalFleetEvidence(evidence, request, nonceDigest); err != nil {
		return nil, err
	}
	return &evidence.Verification, nil
}

func (v *ExternalFleetDeploymentLiveVerifier) postEvidence(ctx context.Context, body []byte, token string) (externalFleetEvidence, error) {
	u := *v.evidenceURL
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/external-fleet/evidence"
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return externalFleetEvidence{}, externalVerifierErr("evidence-request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := v.httpClient.Do(req)
	if err != nil {
		return externalFleetEvidence{}, externalVerifierErr("evidence-unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return externalFleetEvidence{}, externalVerifierErr("evidence-rejected")
	}
	decoded, err := decodeExternalFleetJSON(response.Body, externalFleetEvidenceMaxBody, new(externalFleetEvidence))
	if err != nil {
		return externalFleetEvidence{}, externalVerifierErr("evidence-invalid")
	}
	return *decoded.(*externalFleetEvidence), nil
}

func (v *ExternalFleetDeploymentLiveVerifier) publicEndpoint(path string) string {
	u := *v.publicURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + path
	u.RawQuery = ""
	return u.String()
}

func (v *ExternalFleetDeploymentLiveVerifier) probePublicVersion(ctx context.Context, sourceSHA string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.publicEndpoint("version"), nil)
	if err != nil {
		return externalVerifierErr("public-request")
	}
	response, err := v.httpClient.Do(req)
	if err != nil {
		return externalVerifierErr("public-unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return externalVerifierErr("public-proof")
	}
	var body struct {
		Version    string `json:"version"`
		Allocation string `json:"allocation"`
		Region     string `json:"region"`
	}
	if _, err := decodeExternalFleetJSON(response.Body, 4096, &body); err != nil || body.Version != sourceSHA || body.Allocation == "" || body.Region == "" {
		return externalVerifierErr("public-version")
	}
	return nil
}

func decodeExternalFleetJSON(body io.Reader, limit int64, target any) (any, error) {
	decoder := json.NewDecoder(io.LimitReader(body, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing JSON")
	}
	return target, nil
}

// GitHub adds fields to REST responses frequently. Unlike the private Norn
// evidence schema, unknown GitHub fields are not a reason to reject an
// otherwise exact run binding.
func decodeGitHubRunJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, externalFleetEvidenceMaxBody+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing GitHub JSON")
	}
	return nil
}

func validateExternalFleetEvidence(observed externalFleetEvidence, request ExternalFleetDeploymentVerificationRequest, nonceDigest string) error {
	r := request.Receipt
	if observed.Repository != request.CI.Repository || observed.NonceSHA256 != nonceDigest || observed.NonceWrittenAt.IsZero() || observed.NonceReadAt.IsZero() || !observed.NonceReadAt.After(observed.NonceWrittenAt) || observed.PlanAttemptID != r.Fleet.RunnerAttemptID || observed.CheckpointAttemptID != r.Fleet.RunnerAttemptID {
		return externalVerifierErr("evidence-binding")
	}
	if observed.FixtureHCLSHA256["migration"] != request.Config.MigrationHCLSHA256 || observed.FixtureHCLSHA256["runtime"] != request.Config.RuntimeHCLSHA256 {
		return externalVerifierErr("fixture-digest")
	}
	for _, field := range []string{"prepare.tlsRouting", "migration.migration", "runtime.update", "readiness.consulNomad"} {
		if observed.Canonical[field] == "" {
			return externalVerifierErr("canonical-" + strings.ReplaceAll(field, ".", "-"))
		}
	}
	if err := verificationMatchesExternalReceipt(observed.Verification, r, request.Config); err != nil {
		return externalVerifierErr("receipt-mismatch")
	}
	return nil
}

func validExternalRepository(value string) bool {
	owner, repo, ok := strings.Cut(strings.TrimSpace(value), "/")
	return ok && owner != "" && repo != "" && !strings.ContainsAny(owner+repo, " /\\\r\n")
}

func githubRunWorkflowMatches(ci CIIdentity, path string) bool {
	workflowRef := ci.WorkflowRef
	if workflowRef == "" {
		workflowRef = ci.JobWorkflowRef
	}
	workflowPath, _, ok := strings.Cut(workflowRef, "@")
	return ok && strings.TrimPrefix(workflowPath, ci.Repository+"/") == path
}

func activeGitHubRunStatus(status, conclusion string) bool {
	return (status == "queued" || status == "in_progress" || status == "waiting") && conclusion == ""
}

func githubRunRefMatches(ref, branch string) bool {
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/") == strings.TrimSpace(branch) && branch != ""
}

// externalFleetNonceDigest is retained for test contracts without exposing the
// raw nonce in fixture output.
func externalFleetNonceDigest(n externalAdmissionNonce) string {
	sum := sha256.Sum256([]byte(n.String()))
	return hex.EncodeToString(sum[:])
}
