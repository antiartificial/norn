package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const acceptanceSignatureAlgorithm = "hmac-sha256"

// HMACAcceptanceSigner signs with one current audit key and verifies with the
// current or retained historical keys. Key IDs match the existing mutation
// audit convention: the first eight bytes of SHA-256(key), hex encoded.
type HMACAcceptanceSigner struct {
	currentKeyID string
	keys         map[string][]byte
}

func NewHMACAcceptanceSigner(current string, retained ...string) (*HMACAcceptanceSigner, error) {
	if len(current) < 32 {
		return nil, fmt.Errorf("acceptance signing key must be at least 32 bytes")
	}
	signer := &HMACAcceptanceSigner{currentKeyID: acceptanceKeyID(current), keys: map[string][]byte{}}
	for _, key := range append([]string{current}, retained...) {
		if len(key) < 32 {
			return nil, fmt.Errorf("retained acceptance verification key must be at least 32 bytes")
		}
		id := acceptanceKeyID(key)
		if existing, ok := signer.keys[id]; ok && !hmac.Equal(existing, []byte(key)) {
			return nil, fmt.Errorf("acceptance signing key ID collision")
		}
		signer.keys[id] = append([]byte(nil), []byte(key)...)
	}
	return signer, nil
}

func (s *HMACAcceptanceSigner) Sign(ctx context.Context, canonical []byte) (AcceptanceSignature, error) {
	if err := ctx.Err(); err != nil {
		return AcceptanceSignature{}, err
	}
	if s == nil || s.currentKeyID == "" {
		return AcceptanceSignature{}, fmt.Errorf("acceptance signer is unavailable")
	}
	key := s.keys[s.currentKeyID]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return AcceptanceSignature{Algorithm: acceptanceSignatureAlgorithm, KeyID: s.currentKeyID, Value: hex.EncodeToString(mac.Sum(nil))}, nil
}

func (s *HMACAcceptanceSigner) Verify(ctx context.Context, signature AcceptanceSignature, canonical []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || signature.Algorithm != acceptanceSignatureAlgorithm || signature.KeyID == "" || signature.Value == "" {
		return fmt.Errorf("unsupported or incomplete acceptance signature")
	}
	key, ok := s.keys[signature.KeyID]
	if !ok {
		return fmt.Errorf("acceptance verification key %q is unavailable", signature.KeyID)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature.Value)) {
		return fmt.Errorf("acceptance signature mismatch")
	}
	return nil
}

func acceptanceKeyID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:8])
}
