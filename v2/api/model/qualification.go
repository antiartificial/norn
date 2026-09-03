package model

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

const ReleaseQualificationSchema = "norn.release-qualification/v2"
const ReleaseQualificationPayloadType = "application/vnd.norn.release-qualification.v2+json"

// VerifyReleaseQualificationSignature authenticates a durable staging receipt
// using the production control plane's *current* trusted staging public keys.
// It deliberately does not apply the promotion freshness window: a rollback
// may target an older successful promotion, but it may never use evidence
// signed by a key that is no longer trusted.
func VerifyReleaseQualificationSignature(keys []string, receipt ReleaseQualification) error {
	if receipt.SchemaVersion != ReleaseQualificationSchema || receipt.Environment != "staging" || strings.TrimSpace(receipt.ID) == "" || strings.TrimSpace(receipt.App) == "" || strings.TrimSpace(receipt.DeploymentID) == "" {
		return fmt.Errorf("qualification is malformed")
	}
	if receipt.DSSE.PayloadType != ReleaseQualificationPayloadType || len(receipt.DSSE.Signatures) != 1 || receipt.DSSE.Payload == "" {
		return fmt.Errorf("qualification DSSE envelope is malformed")
	}
	if receipt.KeyID != receipt.DSSE.Signatures[0].KeyID || receipt.Signature != receipt.DSSE.Signatures[0].Sig {
		return fmt.Errorf("qualification display signature does not match authenticated DSSE envelope")
	}
	payload, err := base64.RawStdEncoding.DecodeString(receipt.DSSE.Payload)
	if err != nil {
		return fmt.Errorf("qualification DSSE payload is malformed")
	}
	if string(payload) != CanonicalReleaseQualificationPayload(receipt) {
		return fmt.Errorf("qualification DSSE payload does not match receipt")
	}
	for _, encodedKey := range keys {
		public, err := parseQualificationPublicKey(encodedKey)
		if err != nil {
			continue
		}
		keyID := ReleaseQualificationKeyID(public)
		for _, signature := range receipt.DSSE.Signatures {
			if signature.KeyID != keyID {
				continue
			}
			raw, err := base64.RawStdEncoding.DecodeString(signature.Sig)
			if err == nil && ed25519.Verify(public, DSSEPAE(receipt.DSSE.PayloadType, payload), raw) {
				return nil
			}
		}
	}
	return fmt.Errorf("qualification signature is not trusted by this production control plane")
}

// CanonicalReleaseQualificationPayload is the stable payload that staging
// signs and production verifies. PostgreSQL timestamptz rounds to microseconds,
// so timestamps are canonicalized before signing and after retrieval.
func CanonicalReleaseQualificationPayload(receipt ReleaseQualification) string {
	canonical := struct {
		SchemaVersion, ID, App, Environment, DeploymentID, SourceSHA, Artifact, IssuedAt, ExpiresAt string
		Candidate                                                                                   ReleaseCandidate
	}{ReleaseQualificationSchema, receipt.ID, receipt.App, receipt.Environment, receipt.DeploymentID, receipt.SourceSHA, receipt.Artifact, canonicalQualificationTime(receipt.IssuedAt), canonicalQualificationTime(receipt.ExpiresAt), receipt.Candidate}
	encoded, _ := json.Marshal(canonical)
	return string(encoded)
}

func canonicalQualificationTime(value time.Time) string {
	return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

func DSSEPAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

func ReleaseQualificationKeyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

func parseQualificationPublicKey(raw string) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		public, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key is not Ed25519")
		}
		return public, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("key must be base64 Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}
