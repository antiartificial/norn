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
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if _, err := decodeExternalFleetJSON(response.Body, externalFleetEvidenceMaxBody, &run); err != nil {
		return externalVerifierErr("github-run-invalid")
	}
	if fmt.Sprintf("%d", run.ID) != ci.RunID || fmt.Sprintf("%d", run.RunAttempt) != ci.RunAttempt || run.Status != "completed" || run.Conclusion != "success" || run.Repository.FullName != ci.Repository || (run.Event != "workflow_dispatch" && run.Event != "workflow_run") || !githubRunWorkflowMatches(ci, run.Path) {
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
	for _, predicate := range []string{"https://slsa.dev/provenance/v1", "https://spdx.dev/Document/v2.3"} {
		args := []string{"attestation", "verify", "oci://" + request.Receipt.Artifact, "--repo", candidate.Repository, "--hostname", "github.com", "--signer-workflow", "github.com/" + signerPath, "--signer-digest", signerSHA, "--cert-identity", "https://github.com/" + candidate.SignerWorkflowRef, "--source-digest", request.Receipt.SourceSHA, "--source-ref", candidate.Ref, "--predicate-type", predicate, "--cert-oidc-issuer", "https://token.actions.githubusercontent.com", "--no-public-good"}
		command := exec.CommandContext(ctx, v.path, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "GH_TOKEN=" + token, "GH_PROMPT_DISABLED=1", "NO_COLOR=1"}
		output, err := command.Output()
		if len(output) > externalFleetEvidenceMaxBody || err != nil {
			return externalVerifierErr("github-attestation")
		}
	}
	return nil
}

// ExternalFleetDeploymentVerifierConfig is intentionally not the app config:
// it keeps all adapter inputs explicit and makes construction easy to test.
type ExternalFleetDeploymentVerifierConfig struct {
	EvidenceURL       string
	EvidenceTokenFile string
	GitHubTokenFile   string
	GitHubCLIPath     string
	PublicBaseURL     string
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
		client = &http.Client{Timeout: externalFleetHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &ExternalFleetDeploymentLiveVerifier{evidenceURL: evidenceURL, publicURL: publicURL, evidenceTokenFile: cfg.EvidenceTokenFile, githubTokenFile: cfg.GitHubTokenFile, httpClient: client, attest: attest, githubRun: externalFleetGitHubRunVerifier{client: client}}, nil
}

func ExternalFleetDeploymentVerifierFromConfig(cfg *config.Config) (*ExternalFleetDeploymentLiveVerifier, error) {
	if cfg == nil {
		return nil, fmt.Errorf("external Fleet verifier is not configured")
	}
	return NewExternalFleetDeploymentLiveVerifier(ExternalFleetDeploymentVerifierConfig{EvidenceURL: cfg.ExternalFleetVerifierURL, EvidenceTokenFile: cfg.ExternalFleetVerifierTokenFile, GitHubTokenFile: cfg.ExternalFleetGitHubTokenFile, GitHubCLIPath: cfg.ExternalFleetGitHubCLIPath, PublicBaseURL: cfg.ExternalFleetPublicBaseURL}, nil, nil)
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

func externalVerifierSecretFile(path string) error {
	info, err := os.Lstat(strings.TrimSpace(path))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("must be an owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("must be owned by the API user")
	}
	return nil
}

func readExternalVerifierSecret(path string) (string, error) {
	if err := externalVerifierSecretFile(path); err != nil {
		return "", err
	}
	value, err := os.ReadFile(path)
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

// externalFleetNonceDigest is retained for test contracts without exposing the
// raw nonce in fixture output.
func externalFleetNonceDigest(n externalAdmissionNonce) string {
	sum := sha256.Sum256([]byte(n.String()))
	return hex.EncodeToString(sum[:])
}
