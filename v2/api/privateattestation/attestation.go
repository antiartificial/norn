// Package privateattestation issues and verifies portable DSSE evidence for
// ordinary private repositories without depending on GitHub Enterprise Cloud.
package privateattestation

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/model"
)

const (
	InTotoPayloadType   = "application/vnd.in-toto+json"
	ProvenancePredicate = "https://slsa.dev/provenance/v1"
	SPDXPredicate       = "https://spdx.dev/Document/v2.3"
	// MaxSPDXDocumentBytes bounds caller-supplied SPDX before the document is
	// wrapped in an in-toto statement and base64-encoded by DSSE.
	MaxSPDXDocumentBytes = 2 << 20
	maxStatementBytes    = 3 << 20
)

var helperSigningTimeout = 30 * time.Second

// Signer isolates key custody from the release handler. LocalSigner is the
// pilot implementation; HelperSigner is the stable boundary for a KMS/HSM
// adapter that accepts DSSE PAE on stdin and returns a signature.
type Signer interface {
	KeyID() string
	Sign(context.Context, []byte) (model.DSSESignature, error)
}

type LocalSigner struct {
	private ed25519.PrivateKey
	keyID   string
}

func NewLocalSigner(path string) (*LocalSigner, error) {
	if err := secureOwnerOnlyRegularFile(path, false); err != nil {
		return nil, fmt.Errorf("private signing key: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return nil, fmt.Errorf("private signing key is unavailable")
	}
	private, err := parseEd25519PrivateKey(raw)
	if err != nil {
		return nil, err
	}
	public := private.Public().(ed25519.PublicKey)
	return &LocalSigner{private: private, keyID: KeyID(public)}, nil
}

func (s *LocalSigner) Sign(_ context.Context, payload []byte) (model.DSSESignature, error) {
	if s == nil || len(s.private) != ed25519.PrivateKeySize {
		return model.DSSESignature{}, fmt.Errorf("local release signer is unavailable")
	}
	return model.DSSESignature{KeyID: s.keyID, Sig: base64.RawStdEncoding.EncodeToString(ed25519.Sign(s.private, payload))}, nil
}

func (s *LocalSigner) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}

type HelperSigner struct {
	path  string
	keyID string
}

func NewHelperSigner(path, keyID string) (*HelperSigner, error) {
	path, keyID = strings.TrimSpace(path), strings.TrimSpace(keyID)
	if !filepath.IsAbs(path) || keyID == "" {
		return nil, fmt.Errorf("KMS helper requires an absolute path and expected key ID")
	}
	if err := secureOwnerOnlyRegularFile(path, true); err != nil {
		return nil, fmt.Errorf("KMS helper: %w", err)
	}
	return &HelperSigner{path: path, keyID: keyID}, nil
}

// Sign invokes an operator-owned helper with raw DSSE PAE on stdin. The helper
// owns provider-specific authentication and hashing rules and prints one JSON
// object: {"keyId":"...","sig":"<unpadded-base64>"}.
func (s *HelperSigner) Sign(ctx context.Context, payload []byte) (model.DSSESignature, error) {
	if s == nil {
		return model.DSSESignature{}, fmt.Errorf("KMS release signer is unavailable")
	}
	boundedContext, cancel := context.WithTimeout(ctx, helperSigningTimeout)
	defer cancel()
	cmd := exec.CommandContext(boundedContext, s.path)
	cmd.Stdin = bytes.NewReader(payload)
	// Never pass the control-plane environment (database URL, API tokens,
	// registry credentials, and unrelated provider secrets) across the helper
	// boundary. Provider workload identity belongs to the operator-owned helper
	// and its service sandbox, not ambient Norn process variables.
	cmd.Env = []string{}
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil || out.Len() == 0 || out.Len() > 64<<10 {
		return model.DSSESignature{}, fmt.Errorf("KMS release signing failed")
	}
	var signature model.DSSESignature
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&signature) != nil || signature.KeyID != s.keyID {
		return model.DSSESignature{}, fmt.Errorf("KMS helper returned an invalid key binding")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return model.DSSESignature{}, fmt.Errorf("KMS helper returned more than one response")
	}
	if raw, err := base64.RawStdEncoding.DecodeString(signature.Sig); err != nil || len(raw) == 0 {
		return model.DSSESignature{}, fmt.Errorf("KMS helper returned an invalid signature")
	}
	return signature, nil
}

func (s *HelperSigner) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}

type Verifier struct {
	keys map[string]crypto.PublicKey
}

func NewVerifier(encodedKeys []string) (*Verifier, error) {
	keys := make(map[string]crypto.PublicKey)
	for _, encoded := range encodedKeys {
		key, err := parsePublicKey([]byte(strings.TrimSpace(encoded)))
		if err != nil {
			return nil, fmt.Errorf("trusted private attestation key: %w", err)
		}
		switch key.(type) {
		case ed25519.PublicKey, *ecdsa.PublicKey, *rsa.PublicKey:
		default:
			return nil, fmt.Errorf("trusted private attestation key uses an unsupported algorithm")
		}
		keys[KeyID(key)] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("at least one trusted private attestation key is required")
	}
	return &Verifier{keys: keys}, nil
}

func (v *Verifier) Trusts(keyID string) bool {
	return v != nil && v.keys[strings.TrimSpace(keyID)] != nil
}

type subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type statement struct {
	Type          string          `json:"_type"`
	Subject       []subject       `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

type provenancePredicate struct {
	BuildDefinition struct {
		BuildType            string                    `json:"buildType"`
		ExternalParameters   privateExternalParameters `json:"externalParameters"`
		ResolvedDependencies []resolvedDependency      `json:"resolvedDependencies"`
	} `json:"buildDefinition"`
	RunDetails struct {
		Builder struct {
			ID string `json:"id"`
		} `json:"builder"`
	} `json:"runDetails"`
}

type privateExternalParameters struct {
	App               string `json:"app"`
	Repository        string `json:"repository"`
	RepositoryID      string `json:"repositoryId"`
	OwnerID           string `json:"ownerId"`
	RunID             string `json:"runId"`
	RunAttempt        string `json:"runAttempt"`
	WorkflowRef       string `json:"workflowRef"`
	WorkflowSHA       string `json:"workflowSha"`
	SignerWorkflowRef string `json:"signerWorkflowRef"`
	SignerWorkflowSHA string `json:"signerWorkflowSha"`
	Ref               string `json:"ref"`
}

type resolvedDependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

func Issue(ctx context.Context, signer Signer, app, sourceSHA, artifact string, sbom json.RawMessage, candidate model.ReleaseCandidate) (*model.ReleaseAttestationBundle, error) {
	if signer == nil || strings.TrimSpace(app) == "" || !validSHA(sourceSHA) || !model.IsContentAddressedImage(artifact) || len(sbom) == 0 || len(sbom) > MaxSPDXDocumentBytes || !json.Valid(sbom) {
		return nil, fmt.Errorf("private attestation request is incomplete")
	}
	if err := validateSPDX(sbom); err != nil {
		return nil, err
	}
	digest := strings.TrimPrefix(artifact[strings.LastIndex(artifact, "@")+1:], "sha256:")
	artifactName := artifact[:strings.LastIndex(artifact, "@")]
	predicate := provenancePredicate{}
	predicate.BuildDefinition.BuildType = "https://norn.dev/build-types/github-actions/v1"
	predicate.BuildDefinition.ExternalParameters = privateExternalParameters{App: app, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, OwnerID: candidate.OwnerID, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, WorkflowRef: candidate.WorkflowRef, WorkflowSHA: candidate.WorkflowSHA, SignerWorkflowRef: candidate.SignerWorkflowRef, SignerWorkflowSHA: candidate.SignerWorkflowSHA, Ref: candidate.Ref}
	predicate.BuildDefinition.ResolvedDependencies = []resolvedDependency{{URI: "git+https://github.com/" + candidate.Repository + "@" + candidate.Ref, Digest: map[string]string{"gitCommit": sourceSHA}}}
	predicate.RunDetails.Builder.ID = "https://norn.dev/builders/github-actions"
	provenance, _ := json.Marshal(predicate)
	provenanceEnvelope, err := signStatement(ctx, signer, statement{Type: "https://in-toto.io/Statement/v1", Subject: []subject{{Name: artifactName, Digest: map[string]string{"sha256": digest}}}, PredicateType: ProvenancePredicate, Predicate: provenance})
	if err != nil {
		return nil, err
	}
	sbomEnvelope, err := signStatement(ctx, signer, statement{Type: "https://in-toto.io/Statement/v1", Subject: []subject{{Name: artifactName, Digest: map[string]string{"sha256": digest}}}, PredicateType: SPDXPredicate, Predicate: append(json.RawMessage(nil), sbom...)})
	if err != nil {
		return nil, err
	}
	if provenanceEnvelope.Signatures[0].KeyID != sbomEnvelope.Signatures[0].KeyID {
		return nil, fmt.Errorf("private evidence signer changed between statements")
	}
	return &model.ReleaseAttestationBundle{SchemaVersion: model.NornPrivateAttestationSchema, KeyID: provenanceEnvelope.Signatures[0].KeyID, Provenance: provenanceEnvelope, SBOM: sbomEnvelope}, nil
}

func (v *Verifier) Verify(_ context.Context, artifact, sourceSHA, app string, candidate model.ReleaseCandidate) error {
	bundle := candidate.Attestation.Bundle
	separator := strings.LastIndex(artifact, "@sha256:")
	if v == nil || separator <= 0 || bundle == nil || bundle.SchemaVersion != model.NornPrivateAttestationSchema || bundle.KeyID == "" || candidate.Attestation.Mode != "norn-signed-private" || candidate.Attestation.SubjectDigest != artifact[separator+1:] || candidate.Attestation.MaterialSHA != sourceSHA {
		return fmt.Errorf("Norn private attestation bundle is not bound to this release")
	}
	provenance, err := v.verifyEnvelope(bundle.Provenance, bundle.KeyID, ProvenancePredicate, artifact)
	if err != nil {
		return fmt.Errorf("Norn private provenance rejected: %w", err)
	}
	var predicate provenancePredicate
	if json.Unmarshal(provenance.Predicate, &predicate) != nil {
		return fmt.Errorf("Norn private provenance predicate is malformed")
	}
	want := privateExternalParameters{App: app, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, OwnerID: candidate.OwnerID, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, WorkflowRef: candidate.WorkflowRef, WorkflowSHA: candidate.WorkflowSHA, SignerWorkflowRef: candidate.SignerWorkflowRef, SignerWorkflowSHA: candidate.SignerWorkflowSHA, Ref: candidate.Ref}
	if predicate.BuildDefinition.BuildType != "https://norn.dev/build-types/github-actions/v1" || predicate.BuildDefinition.ExternalParameters != want || predicate.RunDetails.Builder.ID != "https://norn.dev/builders/github-actions" || len(predicate.BuildDefinition.ResolvedDependencies) != 1 {
		return fmt.Errorf("Norn private provenance identity does not match candidate")
	}
	dependency := predicate.BuildDefinition.ResolvedDependencies[0]
	if dependency.URI != "git+https://github.com/"+candidate.Repository+"@"+candidate.Ref || dependency.Digest["gitCommit"] != sourceSHA {
		return fmt.Errorf("Norn private provenance source does not match candidate")
	}
	sbomStatement, err := v.verifyEnvelope(bundle.SBOM, bundle.KeyID, SPDXPredicate, artifact)
	if err != nil {
		return fmt.Errorf("Norn private SPDX evidence rejected: %w", err)
	}
	return validateSPDX(sbomStatement.Predicate)
}

func signStatement(ctx context.Context, signer Signer, value statement) (model.DSSEEnvelope, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return model.DSSEEnvelope{}, err
	}
	signature, err := signer.Sign(ctx, model.DSSEPAE(InTotoPayloadType, payload))
	if err != nil {
		return model.DSSEEnvelope{}, err
	}
	return model.DSSEEnvelope{PayloadType: InTotoPayloadType, Payload: base64.RawStdEncoding.EncodeToString(payload), Signatures: []model.DSSESignature{signature}}, nil
}

func (v *Verifier) verifyEnvelope(envelope model.DSSEEnvelope, keyID, predicateType, artifact string) (statement, error) {
	if envelope.PayloadType != InTotoPayloadType || len(envelope.Signatures) != 1 || envelope.Signatures[0].KeyID != keyID {
		return statement{}, fmt.Errorf("DSSE envelope is malformed")
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil || len(payload) == 0 || len(payload) > maxStatementBytes {
		return statement{}, fmt.Errorf("DSSE payload is invalid")
	}
	signature, err := base64.RawStdEncoding.DecodeString(envelope.Signatures[0].Sig)
	if err != nil || !verifySignature(v.keys[keyID], model.DSSEPAE(envelope.PayloadType, payload), signature) {
		return statement{}, fmt.Errorf("DSSE signature is not trusted")
	}
	var value statement
	if json.Unmarshal(payload, &value) != nil || value.Type != "https://in-toto.io/Statement/v1" || value.PredicateType != predicateType || !statementBindsArtifact(value, artifact) {
		return statement{}, fmt.Errorf("in-toto statement is not bound to the artifact")
	}
	return value, nil
}

func statementBindsArtifact(value statement, artifact string) bool {
	index := strings.LastIndex(artifact, "@sha256:")
	return index > 0 && len(value.Subject) == 1 && value.Subject[0].Name == artifact[:index] && value.Subject[0].Digest["sha256"] == artifact[index+8:]
}

func validateSPDX(raw json.RawMessage) error {
	var document struct {
		SPDXVersion       string `json:"spdxVersion"`
		SPDXID            string `json:"SPDXID"`
		DocumentNamespace string `json:"documentNamespace"`
	}
	if json.Unmarshal(raw, &document) != nil || document.SPDXVersion != "SPDX-2.3" || document.SPDXID != "SPDXRef-DOCUMENT" || document.DocumentNamespace == "" {
		return fmt.Errorf("SPDX document is malformed")
	}
	return nil
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func parseEd25519PrivateKey(raw []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(raw); block != nil {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		private, ok := parsed.(ed25519.PrivateKey)
		if err != nil || !ok {
			return nil, fmt.Errorf("private signing key must be PKCS#8 Ed25519")
		}
		return private, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("private signing key is not base64")
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private signing key must be Ed25519")
	}
	private := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(private, decoded) != 1 {
		return nil, fmt.Errorf("private signing key public half does not match its seed")
	}
	return private, nil
}

func parsePublicKey(raw []byte) (crypto.PublicKey, error) {
	if block, _ := pem.Decode(raw); block != nil {
		return x509.ParsePKIXPublicKey(block.Bytes)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("public key is not base64")
	}
	if len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	return x509.ParsePKIXPublicKey(decoded)
}

func KeyID(public crypto.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

func verifySignature(public crypto.PublicKey, message, signature []byte) bool {
	switch key := public.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(key, message, signature)
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(message)
		return ecdsa.VerifyASN1(key, digest[:], signature)
	case *rsa.PublicKey:
		digest := sha256.Sum256(message)
		return rsa.VerifyPSS(key, crypto.SHA256, digest[:], signature, nil) == nil
	default:
		return false
	}
}

func secureOwnerOnlyRegularFile(path string, executable bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || (executable && info.Mode().Perm()&0o111 == 0) {
		return errors.New("must be a non-writable regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != os.Getuid() && stat.Uid != 0) {
		return errors.New("must be owned by the service user or root")
	}
	if !executable && info.Mode().Perm()&0o077 != 0 {
		return errors.New("must be owner-only")
	}
	return nil
}

func ValidateOwnerOnlyFile(path string) error {
	return secureOwnerOnlyRegularFile(strings.TrimSpace(path), false)
}

// GenerateLocalKey is intentionally kept out of runtime configuration. It is
// useful to operator tooling and tests without ever exporting key material.
func GenerateLocalKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}
