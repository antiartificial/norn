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
)

const (
	WorkloadReleasePrivateInputSchema = "norn.workload-release-private-input/v1"
	maxWorkloadReleasePrivateInput    = 1 << 20
)

var workloadReleasePlanDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var workloadReleaseInputKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// WorkloadReleaseInputBinding keeps captured SOPS bytes private and binds them
// to one accepted operation, app, authority, and immutable desired plan.
type WorkloadReleaseInputBinding struct {
	Authority  string `json:"authority"`
	Operation  string `json:"operationId"`
	App        string `json:"app"`
	PlanDigest string `json:"planDigest"`
}

// WorkloadReleaseInputEnvelope uses a per-record data key. Only the exact
// encrypted SOPS snapshot is sealed; decrypted application secrets never enter
// this API or its public digest.
type WorkloadReleaseInputEnvelope struct {
	Schema         string `json:"schema"`
	KeyID          string `json:"keyId"`
	WrapNonce      []byte `json:"wrapNonce"`
	WrappedDataKey []byte `json:"wrappedDataKey"`
	DataNonce      []byte `json:"dataNonce"`
	Ciphertext     []byte `json:"ciphertext"`
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
