// Package githubattestation verifies private GitHub Enterprise Cloud artifact
// attestations. It is deliberately separate from githubapp: this client can
// only read attestations and must never acquire Fleet's write permissions.
package githubattestation

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"norn/v2/api/model"
)

const (
	githubAPI             = "https://api.github.com"
	githubOIDCIssuer      = "https://token.actions.githubusercontent.com"
	provenancePredicate   = "https://slsa.dev/provenance/v1"
	spdxPredicate         = "https://spdx.dev/Document/v2.3"
	installationTokenSkew = 2 * time.Minute
	installationTokenCeil = 10 * time.Minute
)

var (
	repositoryRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaRe        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Config intentionally contains no broad repository grant. Each minted token
// is narrowed to the candidate repository after the normal Norn OIDC policy
// has bound its immutable numeric identity.
type Config struct {
	AppID          string
	InstallationID int64
	PrivateKeyFile string
	APIBaseURL     string
	GHPath         string
	Production     bool
}

// Configured reports whether an operator attempted to enable this independent
// credential. A partial configuration must fail startup rather than fall back
// to public Sigstore verification.
func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.AppID) != "" || cfg.InstallationID != 0 || strings.TrimSpace(cfg.PrivateKeyFile) != "" || strings.TrimSpace(cfg.GHPath) != ""
}

func ValidateConfig(cfg Config) error {
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.PrivateKeyFile = strings.TrimSpace(cfg.PrivateKeyFile)
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	cfg.GHPath = strings.TrimSpace(cfg.GHPath)
	if cfg.AppID == "" || cfg.InstallationID <= 0 || cfg.PrivateKeyFile == "" || cfg.GHPath == "" {
		return fmt.Errorf("release attestation GitHub App ID, installation ID, private key file, and gh verifier path are required")
	}
	base, err := url.Parse(cfg.APIBaseURL)
	if err != nil || base == nil || base.User != nil || base.Scheme != "https" || base.Hostname() != "api.github.com" || base.Path != "" {
		return fmt.Errorf("release attestation GitHub API base URL must be https://api.github.com")
	}
	if info, err := os.Stat(cfg.PrivateKeyFile); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("release attestation GitHub App private key is unavailable")
	} else if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("release attestation GitHub App private key must not be group/world accessible")
	}
	if info, err := os.Stat(cfg.GHPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("release attestation gh verifier is unavailable or not executable")
	}
	return nil
}

type cachedToken struct {
	value      string
	reuseUntil time.Time
}

// RunCommand exists for hermetic tests. Production always invokes the exact
// configured executable without a shell.
type RunCommand func(context.Context, string, []string, []string) ([]byte, error)

type Verifier struct {
	cfg        Config
	httpClient *http.Client
	now        func() time.Time
	run        RunCommand
	mu         sync.Mutex
	tokens     map[string]cachedToken
}

func New(cfg Config, client *http.Client) (*Verifier, error) {
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = githubAPI
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Verifier{cfg: cfg, httpClient: client, now: time.Now, run: defaultRunCommand, tokens: map[string]cachedToken{}}, nil
}

func defaultRunCommand(ctx context.Context, program string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = env
	return cmd.Output()
}

// Verify requires two separately verified GitHub private-Sigstore statements.
// GitHub CLI performs the private trust-root/signature/timestamp verification;
// Norn then checks the returned, verified statement is still bound to the
// immutable candidate accepted by its OIDC exchange.
func (v *Verifier) Verify(ctx context.Context, image, sourceSHA, signerWorkflowRef string, candidate model.ReleaseCandidate) error {
	if v == nil {
		return fmt.Errorf("private GitHub attestation verifier is unavailable")
	}
	digest, ok := imageDigest(image)
	if !ok || !shaRe.MatchString(sourceSHA) || !repositoryRe.MatchString(candidate.Repository) || candidate.RepositoryID == "" || candidate.OwnerID == "" || !shaRe.MatchString(candidate.SignerWorkflowSHA) || !strings.HasSuffix(signerWorkflowRef, "@"+candidate.SignerWorkflowSHA) || candidate.Ref == "" || candidate.Attestation.SubjectDigest != digest || candidate.Attestation.MaterialSHA != sourceSHA {
		return fmt.Errorf("private GitHub attestation candidate is not bound to this artifact and source")
	}
	token, err := v.installationToken(ctx, candidate.Repository)
	if err != nil {
		return err
	}
	for _, predicate := range []string{provenancePredicate, spdxPredicate} {
		output, err := v.runVerify(ctx, token, image, sourceSHA, signerWorkflowRef, candidate.Repository, candidate.Ref, predicate)
		if err != nil {
			return err
		}
		if err := verifyOutput(output, predicate, digest); err != nil {
			return err
		}
	}
	return nil
}

func (v *Verifier) runVerify(ctx context.Context, token, image, sourceSHA, signerWorkflowRef, repository, ref, predicate string) ([]byte, error) {
	signer, signerSHA, ok := strings.Cut(signerWorkflowRef, "@")
	if !ok || signer == "" || !shaRe.MatchString(signerSHA) {
		return nil, fmt.Errorf("private GitHub attestation signer workflow is invalid")
	}
	args := []string{
		"attestation", "verify", "oci://" + image,
		"--repo", repository,
		"--hostname", "github.com",
		"--signer-workflow", "github.com/" + signer,
		"--signer-digest", signerSHA,
		"--source-digest", sourceSHA,
		"--source-ref", ref,
		"--predicate-type", predicate,
		"--cert-oidc-issuer", githubOIDCIssuer,
		"--no-public-good",
		"--format", "json",
	}
	// Avoid inheriting any durable gh credential/configuration. GH_TOKEN is the
	// only credential and contains the read-only, repository-restricted IAT.
	// A private 0700 home is removed immediately after the verifier returns.
	home, err := os.MkdirTemp("", "norn-gh-attestation-")
	if err != nil {
		return nil, fmt.Errorf("create isolated GitHub verifier home: %w", err)
	}
	defer os.RemoveAll(home)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GH_TOKEN=" + token, "GH_PROMPT_DISABLED=1", "NO_COLOR=1"}
	output, err := v.run(ctx, v.cfg.GHPath, args, env)
	if err != nil {
		return nil, fmt.Errorf("GitHub private attestation verification failed")
	}
	if len(output) > 4<<20 {
		return nil, fmt.Errorf("GitHub private attestation verification output is too large")
	}
	return output, nil
}

func (v *Verifier) installationToken(ctx context.Context, repository string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now().UTC()
	if token, ok := v.tokens[repository]; ok && token.reuseUntil.After(now) {
		return token.value, nil
	}
	key, err := v.privateKey()
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": v.cfg.AppID}
	appJWT, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign release attestation GitHub App JWT: %w", err)
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	// Deliberately request no Contents, Actions, Packages, Fleet, deployment,
	// or write capability. GitHub metadata is implicit for an installation.
	body := map[string]any{"repositories": []string{strings.SplitN(repository, "/", 2)[1]}, "permissions": map[string]string{"attestations": "read"}}
	if err := v.request(ctx, appJWT, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", v.cfg.InstallationID), body, &response); err != nil {
		return "", fmt.Errorf("mint release attestation installation token failed")
	}
	if response.Token == "" || !response.ExpiresAt.After(now.Add(time.Minute)) {
		return "", fmt.Errorf("GitHub returned an invalid release attestation installation token")
	}
	reuseUntil := response.ExpiresAt.Add(-installationTokenSkew)
	if ceiling := now.Add(installationTokenCeil); reuseUntil.After(ceiling) {
		reuseUntil = ceiling
	}
	v.tokens[repository] = cachedToken{value: response.Token, reuseUntil: reuseUntil}
	return response.Token, nil
}

func (v *Verifier) privateKey() (*rsa.PrivateKey, error) {
	info, err := os.Stat(v.cfg.PrivateKeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("release attestation GitHub App private key is unavailable")
	}
	pem, err := os.ReadFile(v.cfg.PrivateKeyFile)
	if err != nil || len(pem) > 64<<10 {
		return nil, fmt.Errorf("release attestation GitHub App private key is unreadable")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("release attestation GitHub App private key is invalid")
	}
	return key, nil
}

func (v *Verifier) request(ctx context.Context, token, method, endpoint string, body, result any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.cfg.APIBaseURL+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result)
}

type verifiedAttestation struct {
	VerificationResult struct {
		Statement struct {
			Subject []struct {
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
			PredicateType string `json:"predicateType"`
		} `json:"statement"`
		VerifiedTimestamps []json.RawMessage `json:"verifiedTimestamps"`
	} `json:"verificationResult"`
}

func verifyOutput(output []byte, predicate, digest string) error {
	var results []verifiedAttestation
	if err := json.Unmarshal(output, &results); err != nil || len(results) == 0 {
		return fmt.Errorf("GitHub private attestation verifier returned no valid results")
	}
	for _, result := range results {
		statement := result.VerificationResult.Statement
		if statement.PredicateType != predicate || len(result.VerificationResult.VerifiedTimestamps) == 0 {
			continue
		}
		for _, subject := range statement.Subject {
			if subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
				return nil
			}
		}
	}
	return fmt.Errorf("GitHub private attestation verifier result is not bound to the required %s statement and subject", predicate)
}

func imageDigest(image string) (string, bool) {
	index := strings.LastIndex(strings.TrimSpace(image), "@sha256:")
	if index < 0 {
		return "", false
	}
	digest := strings.TrimSpace(image)[index+1:]
	return digest, digestRe.MatchString(digest)
}

// permissionCacheKey is intentionally deterministic for tests and review.
func permissionCacheKey(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(values[key])
		out.WriteByte('\n')
	}
	return out.String()
}
