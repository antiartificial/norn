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

const PrivateInvocationEnvelopeSchema = "norn.private-invocation/v1"

var privateInvocationKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// PrivateInvocationInput is request material that must never appear in signed
// operation/effect payloads or public control evidence.
type PrivateInvocationInput struct {
	Body   string `json:"body"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// PrivateInvocationBinding makes a sealed record unusable for any other
// authority, operation, app, or process, including after backup/restore.
type PrivateInvocationBinding struct {
	Authority   string `json:"authority"`
	OperationID string `json:"operationId"`
	App         string `json:"app"`
	Process     string `json:"process"`
}

type PrivateInvocationEnvelope struct {
	Schema         string `json:"schema"`
	KeyID          string `json:"keyId"`
	WrapNonce      []byte `json:"wrapNonce"`
	WrappedDataKey []byte `json:"wrappedDataKey"`
	DataNonce      []byte `json:"dataNonce"`
	Ciphertext     []byte `json:"ciphertext"`
}

// PrivateInvocationKeyRing is a distinct, explicitly supplied encryption key
// ring. It never derives keys from audit signing or app-secret material.
type PrivateInvocationKeyRing struct {
	current string
	keys    map[string][]byte
}

func NewPrivateInvocationKeyRing(current string, supplied map[string][]byte) (*PrivateInvocationKeyRing, error) {
	if !privateInvocationKeyIDPattern.MatchString(current) {
		return nil, errors.New("invalid private invocation current key ID")
	}
	keys := make(map[string][]byte, len(supplied))
	for id, key := range supplied {
		if !privateInvocationKeyIDPattern.MatchString(id) || len(key) != 32 {
			return nil, fmt.Errorf("invalid private invocation key %q", id)
		}
		keys[id] = bytes.Clone(key)
	}
	if _, ok := keys[current]; !ok {
		return nil, errors.New("private invocation current key is unavailable")
	}
	return &PrivateInvocationKeyRing{current: current, keys: keys}, nil
}

func privateInvocationAAD(binding PrivateInvocationBinding) ([]byte, error) {
	if binding.Authority == "" || binding.OperationID == "" || binding.App == "" || binding.Process == "" {
		return nil, errors.New("private invocation binding is incomplete")
	}
	return json.Marshal(struct {
		Schema string `json:"schema"`
		PrivateInvocationBinding
	}{PrivateInvocationEnvelopeSchema, binding})
}

func privateInvocationAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (r *PrivateInvocationKeyRing) Seal(binding PrivateInvocationBinding, input PrivateInvocationInput) (PrivateInvocationEnvelope, error) {
	if r == nil {
		return PrivateInvocationEnvelope{}, errors.New("private invocation key ring is unavailable")
	}
	aad, err := privateInvocationAAD(binding)
	if err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	plain, err := json.Marshal(input)
	if err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	defer clear(plain)
	if len(plain) > 64<<10 {
		return PrivateInvocationEnvelope{}, errors.New("private invocation request exceeds configured limit")
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	defer clear(dataKey)
	dataAEAD, err := privateInvocationAEAD(dataKey)
	if err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	dataNonce := make([]byte, dataAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, dataNonce); err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	ciphertext := dataAEAD.Seal(nil, dataNonce, plain, aad)
	wrapAEAD, err := privateInvocationAEAD(r.keys[r.current])
	if err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	wrapNonce := make([]byte, wrapAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, wrapNonce); err != nil {
		return PrivateInvocationEnvelope{}, err
	}
	wrapped := wrapAEAD.Seal(nil, wrapNonce, dataKey, aad)
	return PrivateInvocationEnvelope{Schema: PrivateInvocationEnvelopeSchema, KeyID: r.current, WrapNonce: wrapNonce, WrappedDataKey: wrapped, DataNonce: dataNonce, Ciphertext: ciphertext}, nil
}

func (r *PrivateInvocationKeyRing) Open(binding PrivateInvocationBinding, envelope PrivateInvocationEnvelope) (PrivateInvocationInput, error) {
	if r == nil || envelope.Schema != PrivateInvocationEnvelopeSchema {
		return PrivateInvocationInput{}, errors.New("private invocation envelope is unavailable or unsupported")
	}
	key, ok := r.keys[envelope.KeyID]
	if !ok {
		return PrivateInvocationInput{}, fmt.Errorf("private invocation key %q is unavailable", envelope.KeyID)
	}
	aad, err := privateInvocationAAD(binding)
	if err != nil {
		return PrivateInvocationInput{}, err
	}
	wrapAEAD, err := privateInvocationAEAD(key)
	if err != nil {
		return PrivateInvocationInput{}, err
	}
	if len(envelope.WrapNonce) != wrapAEAD.NonceSize() {
		return PrivateInvocationInput{}, errors.New("private invocation wrap nonce is invalid")
	}
	dataKey, err := wrapAEAD.Open(nil, envelope.WrapNonce, envelope.WrappedDataKey, aad)
	if err != nil {
		return PrivateInvocationInput{}, errors.New("private invocation wrapped key authentication failed")
	}
	defer clear(dataKey)
	dataAEAD, err := privateInvocationAEAD(dataKey)
	if err != nil {
		return PrivateInvocationInput{}, err
	}
	if len(envelope.DataNonce) != dataAEAD.NonceSize() {
		return PrivateInvocationInput{}, errors.New("private invocation data nonce is invalid")
	}
	plain, err := dataAEAD.Open(nil, envelope.DataNonce, envelope.Ciphertext, aad)
	if err != nil {
		return PrivateInvocationInput{}, errors.New("private invocation ciphertext authentication failed")
	}
	defer clear(plain)
	var input PrivateInvocationInput
	if err := json.Unmarshal(plain, &input); err != nil {
		return PrivateInvocationInput{}, errors.New("private invocation plaintext is invalid")
	}
	return input, nil
}

// PrivateInvocationDigest binds the exact encrypted record to public signed
// intent without revealing the plaintext or a deterministic plaintext hash.
func PrivateInvocationDigest(envelope PrivateInvocationEnvelope) (string, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// RequirePrivateInvocationKeys is a restore/startup preflight. Callers pass
// key IDs referenced by every unresolved or retained private record.
func (r *PrivateInvocationKeyRing) RequirePrivateInvocationKeys(ids []string) error {
	if r == nil {
		return errors.New("private invocation key ring is unavailable")
	}
	for _, id := range ids {
		if _, ok := r.keys[id]; !ok {
			return fmt.Errorf("private invocation key %q is unavailable", id)
		}
	}
	return nil
}
