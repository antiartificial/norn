package savedplaneffect

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	PreparationProjectionSchema = "norn.work-staging-saved-plan-preparation-projection/v1"
	ProjectionRepository        = "antiartificial/norn-fleet"
	PreparationWorkflowRef      = ProjectionRepository + "/.github/workflows/apply.yml@refs/heads/main"
)

// PreparationProjection is the only pre-run signed input. It carries no
// saved-plan, provider, state, node or delete authority; those facts are
// separately verified in PlanBinding, Claim, and the one-use ApplyGrant.
type PreparationProjection struct {
	SchemaVersion       string    `json:"schemaVersion"`
	Repository          string    `json:"repository"`
	FleetEnvironment    string    `json:"fleetEnvironment"`
	PlanID              string    `json:"planId"`
	PlanSHA256          string    `json:"planSha256"`
	ProtectedCommit     string    `json:"protectedCommit"`
	GitHubRunID         int64     `json:"githubRunId"`
	GitHubRunAttempt    int       `json:"githubRunAttempt"`
	GitHubJobID         int64     `json:"githubJobId"`
	GitHubJobName       string    `json:"githubJobName"`
	GitHubWorkflowRef   string    `json:"githubWorkflowRef"`
	GitHubWorkflowSHA   string    `json:"githubWorkflowSha"`
	PlanRunID           int64     `json:"planRunId"`
	PlanArtifactSHA256  string    `json:"planArtifactSha256"`
	DispatchNonceSHA256 string    `json:"dispatchNonceSha256"`
	CreatedAt           time.Time `json:"createdAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

// SignedPreparationProjection returns exact bytes to write to the Fleet inbox.
type SignedPreparationProjection struct {
	CanonicalBytes   []byte `json:"canonicalBytesBase64"`
	Signature        []byte `json:"signatureBase64"`
	PublicKeyPEM     []byte `json:"publicKeyPemBase64"`
	PublicKeySHA256  string `json:"publicKeySha256"`
	ProjectionSHA256 string `json:"projectionSha256"`
}

type ProjectionSigner struct {
	private      ed25519.PrivateKey
	publicPEM    []byte
	publicSHA256 string
}

func NewProjectionSigner(private ed25519.PrivateKey) (*ProjectionSigner, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, ErrInvalid
	}
	der, err := x509.MarshalPKIXPublicKey(private.Public())
	if err != nil {
		return nil, err
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	sum := sha256.Sum256(public)
	return &ProjectionSigner{private: append(ed25519.PrivateKey(nil), private...), publicPEM: public, publicSHA256: hex.EncodeToString(sum[:])}, nil
}

// LoadProjectionSigner requires a no-follow, owner-only key owned by the
// configured service UID. Audit HMAC, Actions, and provider keys are excluded.
func LoadProjectionSigner(path, expectedPublicSHA256 string, expectedUID int) (*ProjectionSigner, error) {
	path = strings.TrimSpace(path)
	if path == "" || expectedUID < 0 || os.Geteuid() != expectedUID || !hex64.MatchString(expectedPublicSHA256) || filepath.Clean(path) != path {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID {
		return nil, ErrInvalid
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return nil, ErrInvalid
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalid
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalid
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, ErrInvalid
	}
	signer, err := NewProjectionSigner(private)
	if err != nil || signer.publicSHA256 != expectedPublicSHA256 {
		return nil, ErrInvalid
	}
	return signer, nil
}

func NewPreparationProjection(planID, planSHA256, commit string, dispatch DispatchBinding, now time.Time) (PreparationProjection, error) {
	if uuid.Validate(planID) != nil || !hex64.MatchString(planSHA256) || !hex40.MatchString(commit) || dispatch.Validate() != nil || dispatch.PlanID != planID || dispatch.PlanSHA256 != planSHA256 || dispatch.ProtectedCommit != commit {
		return PreparationProjection{}, ErrInvalid
	}
	now = now.UTC()
	return PreparationProjection{SchemaVersion: PreparationProjectionSchema, Repository: ProjectionRepository, FleetEnvironment: "staging/nyc3", PlanID: planID, PlanSHA256: planSHA256, ProtectedCommit: commit, GitHubRunID: dispatch.GitHubRunID, GitHubRunAttempt: dispatch.GitHubRunAttempt, GitHubJobID: dispatch.GitHubJobID, GitHubJobName: dispatch.GitHubJobName, GitHubWorkflowRef: dispatch.GitHubWorkflowRef, GitHubWorkflowSHA: dispatch.GitHubWorkflowSHA, PlanRunID: dispatch.PlanRunID, PlanArtifactSHA256: dispatch.PlanArtifactSHA256, DispatchNonceSHA256: dispatch.DispatchNonceSHA256, CreatedAt: now, ExpiresAt: now.Add(MaxClaimLifetime)}, nil
}

func (p PreparationProjection) Validate() error {
	if p.SchemaVersion != PreparationProjectionSchema || p.Repository != ProjectionRepository || p.FleetEnvironment != "staging/nyc3" || uuid.Validate(p.PlanID) != nil || !hex64.MatchString(p.PlanSHA256) || !hex40.MatchString(p.ProtectedCommit) || p.GitHubRunID < 1 || p.GitHubRunAttempt != 1 || p.GitHubJobID < 1 || p.GitHubJobName != "apply" || p.GitHubWorkflowRef != PreparationWorkflowRef || p.GitHubWorkflowSHA != p.ProtectedCommit || p.PlanRunID < 1 || !hex64.MatchString(p.PlanArtifactSHA256) || !hex64.MatchString(p.DispatchNonceSHA256) || p.CreatedAt.IsZero() || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(p.CreatedAt) || p.ExpiresAt.Sub(p.CreatedAt) > MaxClaimLifetime {
		return ErrInvalid
	}
	return nil
}

func (p PreparationProjection) CanonicalBytes() ([]byte, error) {
	if p.Validate() != nil {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, ErrInvalid
	}
	value := map[string]any{}
	if json.Unmarshal(raw, &value) != nil {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return nil, ErrInvalid
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func (s *ProjectionSigner) SignPreparation(p PreparationProjection) (SignedPreparationProjection, error) {
	if s == nil || len(s.private) != ed25519.PrivateKeySize {
		return SignedPreparationProjection{}, ErrInvalid
	}
	canonical, err := p.CanonicalBytes()
	if err != nil {
		return SignedPreparationProjection{}, err
	}
	digest := sha256.Sum256(canonical)
	return SignedPreparationProjection{CanonicalBytes: canonical, Signature: ed25519.Sign(s.private, canonical), PublicKeyPEM: append([]byte(nil), s.publicPEM...), PublicKeySHA256: s.publicSHA256, ProjectionSHA256: hex.EncodeToString(digest[:])}, nil
}

func (s SignedPreparationProjection) Validate() error {
	if len(s.CanonicalBytes) == 0 || len(s.Signature) != ed25519.SignatureSize || len(s.PublicKeyPEM) == 0 || !hex64.MatchString(s.PublicKeySHA256) || !hex64.MatchString(s.ProjectionSHA256) {
		return ErrInvalid
	}
	block, rest := pem.Decode(s.PublicKeyPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return ErrInvalid
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return ErrInvalid
	}
	public, ok := key.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(public, s.CanonicalBytes, s.Signature) {
		return ErrInvalid
	}
	publicSum, projectionSum := sha256.Sum256(s.PublicKeyPEM), sha256.Sum256(s.CanonicalBytes)
	if s.PublicKeySHA256 != hex.EncodeToString(publicSum[:]) || s.ProjectionSHA256 != hex.EncodeToString(projectionSum[:]) {
		return ErrInvalid
	}
	var p PreparationProjection
	if json.Unmarshal(s.CanonicalBytes, &p) != nil || p.Validate() != nil {
		return ErrInvalid
	}
	canonical, err := p.CanonicalBytes()
	if err != nil || !bytes.Equal(canonical, s.CanonicalBytes) {
		return ErrInvalid
	}
	return nil
}

func (s SignedPreparationProjection) Preparation() (PreparationProjection, error) {
	if s.Validate() != nil {
		return PreparationProjection{}, ErrInvalid
	}
	var p PreparationProjection
	if json.Unmarshal(s.CanonicalBytes, &p) != nil {
		return PreparationProjection{}, ErrInvalid
	}
	return p, nil
}
