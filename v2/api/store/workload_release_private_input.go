package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"norn/v2/api/model"
)

const (
	WorkloadReleasePrivateInputSchema   = "norn.workload-release-private-input/v1"
	WorkloadReleasePinnedMaterialSchema = "norn.workload-release-pinned-material/v1"
	maxWorkloadReleasePrivateInput      = 1 << 20
)

var workloadReleasePlanDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var workloadReleaseInputKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// WorkloadReleaseInputBinding keeps pinned workload material private and
// binds it to one accepted operation, app, authority, and immutable plan.
type WorkloadReleaseInputBinding struct {
	Authority  string `json:"authority"`
	Operation  string `json:"operationId"`
	App        string `json:"app"`
	PlanDigest string `json:"planDigest"`
}

// WorkloadReleaseInputEnvelope uses a per-record data key to seal the pinned
// app spec and encrypted SOPS snapshot; neither enters the operation payload.
type WorkloadReleaseInputEnvelope struct {
	Schema         string `json:"schema"`
	KeyID          string `json:"keyId"`
	WrapNonce      []byte `json:"wrapNonce"`
	WrappedDataKey []byte `json:"wrappedDataKey"`
	DataNonce      []byte `json:"dataNonce"`
	Ciphertext     []byte `json:"ciphertext"`
}

// WorkloadReleasePinnedMaterial contains only the job-driving app source and
// the already-encrypted SOPS snapshot. The entire value is sealed by the
// workload-release key ring before it enters durable storage; in particular,
// source Env fields are never copied into the signed operation payload.
type WorkloadReleasePinnedMaterial struct {
	Schema                string `json:"schema"`
	SourceSpecYAML        []byte `json:"sourceSpecYaml"`
	EncryptedSOPSSnapshot []byte `json:"encryptedSopsSnapshot"`
}

// EncodeWorkloadReleasePinnedMaterial captures the exact spec used to derive
// a workload target alongside its encrypted SOPS input. The expected digest
// binds the source bytes to the signed workload plan before sealing.
func EncodeWorkloadReleasePinnedMaterial(spec *model.InfraSpec, expectedSpecDigest string, encryptedSOPSSnapshot []byte) ([]byte, error) {
	if spec == nil || strings.TrimSpace(spec.App) == "" || len(encryptedSOPSSnapshot) == 0 ||
		!workloadReleasePlanDigestPattern.MatchString(expectedSpecDigest) {
		return nil, errors.New("workload release pinned material is incomplete")
	}
	source, err := yaml.Marshal(spec)
	if err != nil {
		return nil, errors.New("workload release source spec could not be serialized")
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != expectedSpecDigest {
		return nil, errors.New("workload release source spec differs from its accepted digest")
	}
	parsed, err := model.ParseInfraSpec(source)
	if err != nil || parsed == nil || parsed.App != spec.App {
		return nil, errors.New("workload release source spec cannot be restored")
	}
	parsedDigest, err := model.InfraSpecDigest(parsed)
	if err != nil || parsedDigest != expectedSpecDigest {
		return nil, errors.New("workload release source spec changes when restored")
	}
	material := WorkloadReleasePinnedMaterial{Schema: WorkloadReleasePinnedMaterialSchema,
		SourceSpecYAML: source, EncryptedSOPSSnapshot: bytes.Clone(encryptedSOPSSnapshot)}
	encoded, err := json.Marshal(material)
	if err != nil || len(encoded) > maxWorkloadReleasePrivateInput {
		clear(source)
		clear(material.EncryptedSOPSSnapshot)
		return nil, errors.New("workload release pinned material is invalid or exceeds its limit")
	}
	clear(source)
	clear(material.EncryptedSOPSSnapshot)
	return encoded, nil
}

// DecodeWorkloadReleasePinnedMaterial authenticates the source representation
// against the accepted app and target spec digest after the outer key ring
// envelope has been opened.
func DecodeWorkloadReleasePinnedMaterial(encoded []byte, app, expectedSpecDigest string) (*model.InfraSpec, []byte, error) {
	if len(encoded) == 0 || len(encoded) > maxWorkloadReleasePrivateInput || strings.TrimSpace(app) == "" ||
		!workloadReleasePlanDigestPattern.MatchString(expectedSpecDigest) {
		return nil, nil, errors.New("workload release pinned material is unavailable")
	}
	var material WorkloadReleasePinnedMaterial
	if err := json.Unmarshal(encoded, &material); err != nil || material.Schema != WorkloadReleasePinnedMaterialSchema ||
		len(material.SourceSpecYAML) == 0 || len(material.EncryptedSOPSSnapshot) == 0 {
		clear(material.SourceSpecYAML)
		clear(material.EncryptedSOPSSnapshot)
		return nil, nil, errors.New("workload release pinned material is invalid")
	}
	encryptedSOPSSnapshot := bytes.Clone(material.EncryptedSOPSSnapshot)
	clear(material.EncryptedSOPSSnapshot)
	spec, err := model.ParseInfraSpec(material.SourceSpecYAML)
	clear(material.SourceSpecYAML)
	if err != nil || spec == nil || spec.App != app {
		clear(encryptedSOPSSnapshot)
		return nil, nil, errors.New("workload release pinned source spec is invalid")
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != expectedSpecDigest {
		clear(encryptedSOPSSnapshot)
		return nil, nil, errors.New("workload release pinned source spec digest differs from its accepted plan")
	}
	return spec, encryptedSOPSSnapshot, nil
}

// WorkloadReleaseInputKeyRing is intentionally a distinct key namespace from
// function invocation material and app SOPS keys.
type WorkloadReleaseInputKeyRing struct {
	current string
	keys    map[string][]byte
}

func NewWorkloadReleaseInputKeyRing(current string, supplied map[string][]byte) (*WorkloadReleaseInputKeyRing, error) {
	if !workloadReleaseInputKeyIDPattern.MatchString(current) {
		return nil, errors.New("invalid workload release input current key ID")
	}
	keys := make(map[string][]byte, len(supplied))
	for id, key := range supplied {
		if !workloadReleaseInputKeyIDPattern.MatchString(id) || len(key) != 32 {
			return nil, fmt.Errorf("invalid workload release input key %q", id)
		}
		keys[id] = bytes.Clone(key)
	}
	if _, ok := keys[current]; !ok {
		return nil, errors.New("workload release input current key is unavailable")
	}
	return &WorkloadReleaseInputKeyRing{current: current, keys: keys}, nil
}

func workloadReleaseInputAAD(binding WorkloadReleaseInputBinding) ([]byte, error) {
	if binding.Authority == "" || binding.Operation == "" || binding.App == "" || !workloadReleasePlanDigestPattern.MatchString(binding.PlanDigest) {
		return nil, errors.New("workload release input binding is incomplete")
	}
	return json.Marshal(struct {
		Schema string `json:"schema"`
		WorkloadReleaseInputBinding
	}{WorkloadReleasePrivateInputSchema, binding})
}

func workloadReleaseInputAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (r *WorkloadReleaseInputKeyRing) Seal(binding WorkloadReleaseInputBinding, ciphertext []byte) (WorkloadReleaseInputEnvelope, error) {
	if r == nil || len(ciphertext) == 0 || len(ciphertext) > maxWorkloadReleasePrivateInput {
		return WorkloadReleaseInputEnvelope{}, errors.New("workload release input is unavailable or exceeds its limit")
	}
	aad, err := workloadReleaseInputAAD(binding)
	if err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	defer clear(dataKey)
	dataAEAD, err := workloadReleaseInputAEAD(dataKey)
	if err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	dataNonce := make([]byte, dataAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, dataNonce); err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	sealed := dataAEAD.Seal(nil, dataNonce, ciphertext, aad)
	wrapAEAD, err := workloadReleaseInputAEAD(r.keys[r.current])
	if err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	wrapNonce := make([]byte, wrapAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, wrapNonce); err != nil {
		return WorkloadReleaseInputEnvelope{}, err
	}
	wrapped := wrapAEAD.Seal(nil, wrapNonce, dataKey, aad)
	return WorkloadReleaseInputEnvelope{Schema: WorkloadReleasePrivateInputSchema, KeyID: r.current, WrapNonce: wrapNonce, WrappedDataKey: wrapped, DataNonce: dataNonce, Ciphertext: sealed}, nil
}

// Open authenticates the binding before returning the encrypted SOPS bytes.
// The caller owns the returned plaintext buffer and should clear it after use.
func (r *WorkloadReleaseInputKeyRing) Open(binding WorkloadReleaseInputBinding, envelope WorkloadReleaseInputEnvelope) ([]byte, error) {
	if r == nil || envelope.Schema != WorkloadReleasePrivateInputSchema ||
		len(envelope.Ciphertext) == 0 || len(envelope.Ciphertext) > maxWorkloadReleasePrivateInput+16 ||
		len(envelope.WrappedDataKey) != 32+16 {
		return nil, errors.New("workload release input envelope is unavailable or unsupported")
	}
	key, ok := r.keys[envelope.KeyID]
	if !ok {
		return nil, fmt.Errorf("workload release input key %q is unavailable", envelope.KeyID)
	}
	aad, err := workloadReleaseInputAAD(binding)
	if err != nil {
		return nil, err
	}
	wrapAEAD, err := workloadReleaseInputAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(envelope.WrapNonce) != wrapAEAD.NonceSize() {
		return nil, errors.New("workload release input wrap nonce is invalid")
	}
	dataKey, err := wrapAEAD.Open(nil, envelope.WrapNonce, envelope.WrappedDataKey, aad)
	if err != nil {
		return nil, errors.New("workload release input wrapped key authentication failed")
	}
	defer clear(dataKey)
	dataAEAD, err := workloadReleaseInputAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	if len(envelope.DataNonce) != dataAEAD.NonceSize() {
		return nil, errors.New("workload release input data nonce is invalid")
	}
	plain, err := dataAEAD.Open(nil, envelope.DataNonce, envelope.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("workload release input ciphertext authentication failed")
	}
	if len(plain) == 0 || len(plain) > maxWorkloadReleasePrivateInput {
		clear(plain)
		return nil, errors.New("workload release input plaintext is invalid")
	}
	return plain, nil
}

// WorkloadReleaseInputDigest safely identifies the randomized private
// envelope in signed acceptance without publishing a raw SOPS file digest.
func WorkloadReleaseInputDigest(envelope WorkloadReleaseInputEnvelope) (string, error) {
	if envelope.Schema != WorkloadReleasePrivateInputSchema || envelope.KeyID == "" || len(envelope.Ciphertext) == 0 {
		return "", errors.New("workload release input envelope is invalid")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// RequireWorkloadReleaseInputKeys prevents restore/startup when a retained
// private release record references a key that is no longer configured.
func (r *WorkloadReleaseInputKeyRing) RequireWorkloadReleaseInputKeys(ids []string) error {
	if r == nil {
		return errors.New("workload release input key ring is unavailable")
	}
	for _, id := range ids {
		if _, ok := r.keys[id]; !ok {
			return fmt.Errorf("workload release input key %q is unavailable", id)
		}
	}
	return nil
}
