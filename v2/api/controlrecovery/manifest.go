package controlrecovery

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	recoveryBundleFormat = "norn.control-recovery/v1"
	manifestPayloadType  = "application/vnd.norn.control-recovery-manifest.v1+json"
)

type ManifestCatalogEntry struct {
	Version              int64  `json:"version"`
	Name                 string `json:"name"`
	Checksum             string `json:"checksum"`
	MinimumReaderVersion int64  `json:"minimumReaderVersion"`
	MinimumWriterVersion int64  `json:"minimumWriterVersion"`
}

type ManifestTableCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type ManifestSection struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type RecoveryManifest struct {
	Format                    string                 `json:"format"`
	BundleID                  string                 `json:"bundleId"`
	CreatedAt                 string                 `json:"createdAt"`
	Authority                 string                 `json:"authority"`
	SourceDatabaseFingerprint string                 `json:"sourceDatabaseFingerprint"`
	Schema                    string                 `json:"schema"`
	PostgreSQLVersion         string                 `json:"postgresqlVersion"`
	PGDumpVersion             string                 `json:"pgDumpVersion"`
	Catalog                   []ManifestCatalogEntry `json:"catalog"`
	Tables                    []ManifestTableCount   `json:"tables"`
	Sections                  []ManifestSection      `json:"sections"`
	RelationshipContract      string                 `json:"relationshipContract"`
	RelationshipsValid        bool                   `json:"relationshipsValid"`
	RequiredSigningKeyIDs     []string               `json:"requiredSigningKeyIds"`
	EncryptionRecipients      []string               `json:"encryptionRecipients"`
	UnresolvedEffects         int64                  `json:"unresolvedEffects"`
}

type DSSESignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type ManifestEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []DSSESignature `json:"signatures"`
}

type ManifestSigner struct {
	private ed25519.PrivateKey
	keyID   string
}

func NewManifestSigner(encoded []byte) (*ManifestSigner, error) {
	private, err := parseManifestPrivateKey(encoded)
	if err != nil {
		return nil, err
	}
	return &ManifestSigner{private: private, keyID: manifestKeyID(private.Public().(ed25519.PublicKey))}, nil
}

func (s *ManifestSigner) Sign(manifest RecoveryManifest) ([]byte, []byte, error) {
	if s == nil || len(s.private) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("recovery manifest signer is unavailable")
	}
	if err := validateManifest(manifest); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("encode recovery manifest: %w", err)
	}
	signature := ed25519.Sign(s.private, dssePAE(manifestPayloadType, canonical))
	envelope := ManifestEnvelope{
		PayloadType: manifestPayloadType,
		Payload:     base64.RawStdEncoding.EncodeToString(canonical),
		Signatures:  []DSSESignature{{KeyID: s.keyID, Sig: base64.RawStdEncoding.EncodeToString(signature)}},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("encode recovery manifest signature: %w", err)
	}
	return canonical, encoded, nil
}

func verifyManifest(manifestBytes, envelopeBytes []byte, trusted []ed25519.PublicKey) (RecoveryManifest, error) {
	var manifest RecoveryManifest
	if err := decodeExactJSON(manifestBytes, &manifest); err != nil {
		return RecoveryManifest{}, fmt.Errorf("recovery manifest is malformed")
	}
	var envelope ManifestEnvelope
	if err := decodeExactJSON(envelopeBytes, &envelope); err != nil || envelope.PayloadType != manifestPayloadType || len(envelope.Signatures) != 1 {
		return RecoveryManifest{}, fmt.Errorf("recovery manifest signature envelope is malformed")
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil || !bytes.Equal(payload, manifestBytes) {
		return RecoveryManifest{}, fmt.Errorf("recovery manifest signature payload differs from manifest")
	}
	signature, err := base64.RawStdEncoding.DecodeString(envelope.Signatures[0].Sig)
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("recovery manifest signature is malformed")
	}
	verified := false
	for _, public := range trusted {
		if len(public) != ed25519.PublicKeySize {
			continue
		}
		if envelope.Signatures[0].KeyID == manifestKeyID(public) && ed25519.Verify(public, dssePAE(manifestPayloadType, payload), signature) {
			verified = true
			break
		}
	}
	if !verified {
		return RecoveryManifest{}, fmt.Errorf("recovery manifest signature is untrusted")
	}
	if err := validateManifest(manifest); err != nil {
		return RecoveryManifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest RecoveryManifest) error {
	if manifest.Format != recoveryBundleFormat || manifest.Schema == "" || !manifest.RelationshipsValid || manifest.RelationshipContract != "norn.control-relationships/v1" {
		return fmt.Errorf("recovery manifest is incomplete")
	}
	if manifest.UnresolvedEffects < 0 {
		return fmt.Errorf("recovery manifest unresolved effect count is invalid")
	}
	if _, err := uuid.Parse(manifest.BundleID); err != nil {
		return fmt.Errorf("recovery manifest bundle ID is invalid")
	}
	if _, err := uuid.Parse(manifest.Authority); err != nil {
		return fmt.Errorf("recovery manifest authority is invalid")
	}
	if !validSHA256(manifest.SourceDatabaseFingerprint) {
		return fmt.Errorf("recovery manifest database fingerprint is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return fmt.Errorf("recovery manifest timestamp is invalid")
	}
	if len(manifest.Catalog) != len(inspectionCatalog) || len(manifest.Tables) != len(InspectionRegistry()) || len(manifest.Sections) != 1 || manifest.Sections[0].Name != "control.dump" || manifest.Sections[0].Format != "postgresql-custom" || manifest.Sections[0].Size <= 0 || !validSHA256(manifest.Sections[0].SHA256) {
		return fmt.Errorf("recovery manifest inventory is incomplete")
	}
	for index, known := range inspectionCatalog {
		entry := manifest.Catalog[index]
		if entry.Version != known.version || entry.Name != known.name || entry.Checksum != known.checksum || entry.MinimumReaderVersion != known.minimumReader || entry.MinimumWriterVersion != known.minimumWriter {
			return fmt.Errorf("recovery manifest catalog is unsupported")
		}
	}
	registry := InspectionRegistry()
	for index, table := range registry {
		if manifest.Tables[index].Name != table.Name || manifest.Tables[index].Count < 0 {
			return fmt.Errorf("recovery manifest table inventory is unsupported")
		}
	}
	if !strictlySortedNonEmpty(manifest.RequiredSigningKeyIDs) || !strictlySortedNonEmpty(manifest.EncryptionRecipients) || len(manifest.EncryptionRecipients) == 0 {
		return fmt.Errorf("recovery manifest key inventory is invalid")
	}
	for _, recipient := range manifest.EncryptionRecipients {
		if !validSHA256(recipient) {
			return fmt.Errorf("recovery manifest recipient inventory is invalid")
		}
	}
	return nil
}

func ParseManifestPublicKey(encoded []byte) (ed25519.PublicKey, error) {
	if block, rest := pem.Decode(encoded); block != nil && len(bytes.TrimSpace(rest)) == 0 {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse Ed25519 manifest public key: %w", err)
		}
		public, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("manifest public key is not Ed25519")
		}
		return public, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("manifest public key must be PEM or raw-base64 Ed25519")
	}
	return ed25519.PublicKey(decoded), nil
}

func parseManifestPrivateKey(encoded []byte) (ed25519.PrivateKey, error) {
	if block, rest := pem.Decode(encoded); block != nil && len(bytes.TrimSpace(rest)) == 0 {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse Ed25519 manifest private key: %w", err)
		}
		private, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("manifest private key is not Ed25519")
		}
		return private, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("manifest private key must be PEM or raw-base64 Ed25519")
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) == ed25519.PrivateKeySize {
		private := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(private, decoded) != 1 {
			return nil, fmt.Errorf("manifest private key public half does not match seed")
		}
		return private, nil
	}
	return nil, fmt.Errorf("manifest private key has invalid length")
}

func manifestKeyID(public ed25519.PublicKey) string {
	digest := sha256.Sum256(public)
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func dssePAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

func decodeExactJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func strictlySortedNonEmpty(values []string) bool {
	if !sort.StringsAreSorted(values) {
		return false
	}
	for index, value := range values {
		if value == "" || (index > 0 && value == values[index-1]) {
			return false
		}
	}
	return true
}

func digestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
