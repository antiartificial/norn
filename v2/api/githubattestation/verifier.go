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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	numericIDRe  = regexp.MustCompile(`^[1-9][0-9]*$`)
	shaRe        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Config intentionally contains no broad repository grant. Each minted token
// is narrowed to the candidate repository after the normal Norn OIDC policy
// has bound its immutable numeric identity. RegistryAuthFile is an independent
// read-only registry credential; a GitHub App attestation token is not a
// substitute for private image registry authentication.
type Config struct {
	AppID            string
	InstallationID   int64
	PrivateKeyFile   string
	RegistryAuthFile string
	APIBaseURL       string
	GHPath           string
}

// Configured reports whether an operator attempted to enable this independent
// credential. A partial configuration must fail startup rather than fall back
// to public Sigstore verification.
func Configured(cfg Config) bool {
	return strings.TrimSpace(cfg.AppID) != "" || cfg.InstallationID != 0 || strings.TrimSpace(cfg.PrivateKeyFile) != "" || strings.TrimSpace(cfg.RegistryAuthFile) != "" || strings.TrimSpace(cfg.GHPath) != ""
}

func ValidateConfig(cfg Config) error {
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.PrivateKeyFile = strings.TrimSpace(cfg.PrivateKeyFile)
	cfg.RegistryAuthFile = strings.TrimSpace(cfg.RegistryAuthFile)
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	cfg.GHPath = strings.TrimSpace(cfg.GHPath)
	if cfg.AppID == "" || cfg.InstallationID <= 0 || cfg.PrivateKeyFile == "" || cfg.RegistryAuthFile == "" || cfg.GHPath == "" {
		return fmt.Errorf("release attestation GitHub App ID, installation ID, private key file, registry auth file, and gh verifier path are required")
	}
	base, err := url.Parse(cfg.APIBaseURL)
	if err != nil || base == nil || base.User != nil || base.Scheme != "https" || base.Hostname() != "api.github.com" || base.Path != "" {
		return fmt.Errorf("release attestation GitHub API base URL must be https://api.github.com")
	}
	if err := secureRegularFile(cfg.PrivateKeyFile); err != nil {
		return fmt.Errorf("release attestation GitHub App private key is unavailable")
	}
	if err := secureRegularFile(cfg.RegistryAuthFile); err != nil {
		return fmt.Errorf("release attestation registry auth file is unavailable")
	}
	if !filepath.IsAbs(cfg.GHPath) {
		return fmt.Errorf("release attestation gh verifier path must be absolute")
	}
	if info, err := os.Lstat(cfg.GHPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("release attestation gh verifier is unavailable or not executable")
	}
	return nil
}

// secureRegularFile deliberately uses Lstat: resolving a symlink would allow
// a later swap of the private key or registry credential after startup.
func secureRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("not an owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("not an owner-only regular file")
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
// GitHub CLI performs private trust-root/signature/timestamp verification;
// Norn then checks the returned verified statements against its immutable OIDC
// evidence. Any missing or unexpected binding fails closed.
func (v *Verifier) Verify(ctx context.Context, image, sourceSHA, signerWorkflowRef string, candidate model.ReleaseCandidate) error {
	if v == nil {
		return fmt.Errorf("private GitHub attestation verifier is unavailable")
	}
	digest, ok := imageDigest(image)
	if !ok || !shaRe.MatchString(sourceSHA) || !repositoryRe.MatchString(candidate.Repository) || !numericIDRe.MatchString(candidate.RepositoryID) || !numericIDRe.MatchString(candidate.OwnerID) || !shaRe.MatchString(candidate.SignerWorkflowSHA) || candidate.Ref == "" || candidate.Attestation.SubjectDigest != digest || candidate.Attestation.MaterialSHA != sourceSHA || !privateVisibility(candidate.RepositoryVisibility) {
		return fmt.Errorf("private GitHub attestation candidate is not bound to this artifact and source")
	}
	signer, signerSHA, ok := strings.Cut(signerWorkflowRef, "@")
	if !ok || signer == "" || signerSHA != candidate.SignerWorkflowSHA || candidate.SignerWorkflowRef != signerWorkflowRef {
		return fmt.Errorf("private GitHub attestation signer workflow is invalid")
	}
	token, err := v.installationToken(ctx, candidate.Repository, candidate.RepositoryID)
	if err != nil {
		return err
	}
	for _, predicate := range []string{provenancePredicate, spdxPredicate} {
		output, err := v.runVerify(ctx, token, image, sourceSHA, signerWorkflowRef, candidate.Repository, candidate.Ref, predicate)
		if err != nil {
			return err
		}
		if err := verifyOutput(output, predicate, digest, sourceSHA, candidate, signer); err != nil {
			return err
		}
	}
	return nil
}

func privateVisibility(visibility string) bool {
	switch strings.ToLower(strings.TrimSpace(visibility)) {
	case "private", "internal":
		return true
	default:
		return false
	}
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
		"--cert-identity", "https://github.com/" + signerWorkflowRef,
		"--source-digest", sourceSHA,
		"--source-ref", ref,
		"--predicate-type", predicate,
		"--cert-oidc-issuer", githubOIDCIssuer,
		"--no-public-good",
		"--format", "json",
	}
	home, err := os.MkdirTemp("", "norn-gh-attestation-")
	if err != nil {
		return nil, fmt.Errorf("create isolated GitHub verifier home: %w", err)
	}
	defer os.RemoveAll(home)
	if err := os.Mkdir(filepath.Join(home, ".docker"), 0o700); err != nil {
		return nil, fmt.Errorf("create isolated registry config: %w", err)
	}
	if err := secureRegularFile(v.cfg.RegistryAuthFile); err != nil {
		return nil, fmt.Errorf("read isolated registry config failed")
	}
	registryConfig, err := os.ReadFile(v.cfg.RegistryAuthFile)
	if err != nil || len(registryConfig) == 0 || len(registryConfig) > 1<<20 {
		return nil, fmt.Errorf("read isolated registry config failed")
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), registryConfig, 0o600); err != nil {
		return nil, fmt.Errorf("write isolated registry config failed")
	}
	// Do not inherit the process environment: GH_TOKEN is the ephemeral,
	// repository-restricted IAT; registry credentials arrive only through the
	// private Docker config. Neither value is included in returned errors.
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

func (v *Verifier) installationToken(ctx context.Context, repository, repositoryID string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now().UTC()
	cacheKey := repository + "@" + repositoryID
	if token, ok := v.tokens[cacheKey]; ok && token.reuseUntil.After(now) {
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
	var response installationTokenResponse
	repositoryNumericID, err := strconv.ParseInt(repositoryID, 10, 64)
	if err != nil || repositoryNumericID <= 0 {
		return "", fmt.Errorf("GitHub release repository ID is invalid")
	}
	body := map[string]any{"repository_ids": []int64{repositoryNumericID}, "permissions": map[string]string{"attestations": "read"}}
	if err := v.request(ctx, appJWT, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", v.cfg.InstallationID), body, &response); err != nil {
		return "", fmt.Errorf("mint release attestation installation token failed")
	}
	if response.Token == "" || !response.ExpiresAt.After(now.Add(time.Minute)) || !response.matches(repository, repositoryID) {
		return "", fmt.Errorf("GitHub returned an invalid release attestation installation token")
	}
	reuseUntil := response.ExpiresAt.Add(-installationTokenSkew)
	if ceiling := now.Add(installationTokenCeil); reuseUntil.After(ceiling) {
		reuseUntil = ceiling
	}
	v.tokens[cacheKey] = cachedToken{value: response.Token, reuseUntil: reuseUntil}
	return response.Token, nil
}

type installationTokenResponse struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
	Repositories        []struct {
		ID       json.Number `json:"id"`
		FullName string      `json:"full_name"`
	} `json:"repositories"`
}

func (r installationTokenResponse) matches(repository, repositoryID string) bool {
	if r.Permissions == nil || r.Permissions["attestations"] != "read" {
		return false
	}
	for permission, level := range r.Permissions {
		if (permission != "attestations" && permission != "metadata") || (permission == "metadata" && level != "read") {
			return false
		}
	}
	if r.RepositorySelection != "selected" {
		return false
	}
	return len(r.Repositories) == 1 && r.Repositories[0].FullName == repository && r.Repositories[0].ID.String() == repositoryID
}

func (v *Verifier) privateKey() (*rsa.PrivateKey, error) {
	if err := secureRegularFile(v.cfg.PrivateKeyFile); err != nil {
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
			PredicateType string          `json:"predicateType"`
			Predicate     json.RawMessage `json:"predicate"`
		} `json:"statement"`
		Signature struct {
			Certificate certificate `json:"certificate"`
		} `json:"signature"`
		VerifiedTimestamps []json.RawMessage `json:"verifiedTimestamps"`
	} `json:"verificationResult"`
}

type certificate struct {
	Issuer                              string `json:"issuer"`
	GitHubWorkflowSHA                   string `json:"githubWorkflowSHA"`
	GitHubWorkflowRepository            string `json:"githubWorkflowRepository"`
	GitHubWorkflowRef                   string `json:"githubWorkflowRef"`
	BuildSignerURI                      string `json:"buildSignerURI"`
	BuildSignerDigest                   string `json:"buildSignerDigest"`
	BuildConfigURI                      string `json:"buildConfigURI"`
	BuildConfigDigest                   string `json:"buildConfigDigest"`
	RunnerEnvironment                   string `json:"runnerEnvironment"`
	SourceRepositoryURI                 string `json:"sourceRepositoryURI"`
	SourceRepositoryDigest              string `json:"sourceRepositoryDigest"`
	SourceRepositoryIdentifier          string `json:"sourceRepositoryIdentifier"`
	SourceRepositoryOwnerIdentifier     string `json:"sourceRepositoryOwnerIdentifier"`
	SourceRepositoryVisibilityAtSigning string `json:"sourceRepositoryVisibilityAtSigning"`
	RunInvocationURI                    string `json:"runInvocationURI"`
}

func verifyOutput(output []byte, predicate, digest, sourceSHA string, candidate model.ReleaseCandidate, signer string) error {
	var results []verifiedAttestation
	if err := json.Unmarshal(output, &results); err != nil || len(results) == 0 {
		return fmt.Errorf("GitHub private attestation verifier returned no valid results")
	}
	for _, result := range results {
		statement := result.VerificationResult.Statement
		if statement.PredicateType != predicate || len(result.VerificationResult.VerifiedTimestamps) == 0 || !hasSubjectDigest(statement.Subject, digest) || !certificateMatches(result.VerificationResult.Signature.Certificate, candidate, sourceSHA, signer) {
			continue
		}
		if predicate == provenancePredicate && provenanceMatches(statement.Predicate, candidate, sourceSHA) {
			return nil
		}
		if predicate == spdxPredicate && spdxMatches(statement.Predicate) {
			return nil
		}
	}
	return fmt.Errorf("GitHub private attestation verifier result is not bound to the required %s statement and subject", predicate)
}

func hasSubjectDigest(subjects []struct {
	Digest map[string]string `json:"digest"`
}, digest string) bool {
	for _, subject := range subjects {
		if subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
			return true
		}
	}
	return false
}

func certificateMatches(cert certificate, candidate model.ReleaseCandidate, sourceSHA, signer string) bool {
	if cert.Issuer != githubOIDCIssuer || cert.GitHubWorkflowSHA != sourceSHA || cert.GitHubWorkflowRepository != candidate.Repository || cert.GitHubWorkflowRef != candidate.Ref || cert.SourceRepositoryIdentifier != candidate.RepositoryID || cert.SourceRepositoryOwnerIdentifier != candidate.OwnerID || cert.SourceRepositoryDigest != sourceSHA || strings.ToLower(cert.SourceRepositoryVisibilityAtSigning) != strings.ToLower(candidate.RepositoryVisibility) || !privateVisibility(cert.SourceRepositoryVisibilityAtSigning) || !strings.EqualFold(cert.RunnerEnvironment, "github-hosted") {
		return false
	}
	if !repositoryURIMatches(cert.SourceRepositoryURI, candidate.Repository) || cert.BuildSignerURI != "https://github.com/"+signer+"@"+candidate.SignerWorkflowSHA || cert.BuildSignerDigest != candidate.SignerWorkflowSHA || cert.BuildConfigURI != "https://github.com/"+candidate.WorkflowRef || cert.BuildConfigDigest != candidate.WorkflowSHA {
		return false
	}
	return cert.RunInvocationURI == fmt.Sprintf("https://github.com/%s/actions/runs/%s/attempts/%s", candidate.Repository, candidate.RunID, candidate.RunAttempt)
}

func repositoryURIMatches(uri, repository string) bool {
	return uri == "https://github.com/"+repository || uri == "git+https://github.com/"+repository || uri == "git+https://github.com/"+repository+".git"
}

func provenanceMatches(predicate json.RawMessage, candidate model.ReleaseCandidate, sourceSHA string) bool {
	var body struct {
		BuildDefinition struct {
			ResolvedDependencies []struct {
				URI    string `json:"uri"`
				Digest struct {
					GitCommit string `json:"gitCommit"`
				} `json:"digest"`
			} `json:"resolvedDependencies"`
			InternalParameters struct {
				GitHub struct {
					RepositoryID      string `json:"repository_id"`
					RepositoryOwnerID string `json:"repository_owner_id"`
					RunnerEnvironment string `json:"runner_environment"`
				} `json:"github"`
			} `json:"internalParameters"`
		} `json:"buildDefinition"`
		RunDetails struct {
			Metadata struct {
				InvocationID string `json:"invocationId"`
			} `json:"metadata"`
		} `json:"runDetails"`
	}
	if json.Unmarshal(predicate, &body) != nil || body.BuildDefinition.InternalParameters.GitHub.RepositoryID != candidate.RepositoryID || body.BuildDefinition.InternalParameters.GitHub.RepositoryOwnerID != candidate.OwnerID || !strings.EqualFold(body.BuildDefinition.InternalParameters.GitHub.RunnerEnvironment, "github-hosted") || body.RunDetails.Metadata.InvocationID != fmt.Sprintf("https://github.com/%s/actions/runs/%s/attempts/%s", candidate.Repository, candidate.RunID, candidate.RunAttempt) {
		return false
	}
	for _, material := range body.BuildDefinition.ResolvedDependencies {
		if repositoryURIMatches(strings.TrimSuffix(material.URI, "@"+candidate.Ref), candidate.Repository) && material.Digest.GitCommit == sourceSHA {
			return true
		}
	}
	return false
}

func spdxMatches(predicate json.RawMessage) bool {
	var body struct {
		SPDXVersion       string          `json:"spdxVersion"`
		SPDXID            string          `json:"SPDXID"`
		Name              string          `json:"name"`
		DocumentNamespace string          `json:"documentNamespace"`
		CreationInfo      json.RawMessage `json:"creationInfo"`
	}
	return json.Unmarshal(predicate, &body) == nil && body.SPDXVersion == "SPDX-2.3" && body.SPDXID == "SPDXRef-DOCUMENT" && body.Name != "" && body.DocumentNamespace != "" && len(body.CreationInfo) > 2
}

func imageDigest(image string) (string, bool) {
	index := strings.LastIndex(strings.TrimSpace(image), "@sha256:")
	if index < 0 {
		return "", false
	}
	digest := strings.TrimSpace(image)[index+1:]
	return digest, digestRe.MatchString(digest)
}
