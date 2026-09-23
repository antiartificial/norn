package controlrecovery

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
)

func TestManifestSignatureRejectsTrailingJSONAndInvalidTrustKeys(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewManifestSigner([]byte(base64.RawStdEncoding.EncodeToString(private)))
	if err != nil {
		t.Fatal(err)
	}
	manifest := validTestManifest([]byte("dump"))
	manifestBytes, envelopeBytes, err := signer.Sign(manifest)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifyManifest(manifestBytes, envelopeBytes, []ed25519.PublicKey{{1, 2}, public})
	if err != nil || verified.BundleID != manifest.BundleID {
		t.Fatalf("verify manifest: %#v %v", verified, err)
	}
	if _, err := verifyManifest(append(append([]byte(nil), manifestBytes...), []byte(` {}`)...), envelopeBytes, []ed25519.PublicKey{public}); err == nil {
		t.Fatal("manifest with trailing JSON was accepted")
	}
	var envelope ManifestEnvelope
	if err := decodeExactJSON(envelopeBytes, &envelope); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.RawStdEncoding.DecodeString(envelope.Payload)
	envelope.Payload = base64.RawStdEncoding.EncodeToString(append(payload, []byte(` true`)...))
	changed, _ := jsonMarshal(envelope)
	if _, err := verifyManifest(manifestBytes, changed, []ed25519.PublicKey{public}); err == nil {
		t.Fatal("signature envelope with trailing payload was accepted")
	}
}

func TestVerifiedBundleKeepsCiphertextOpenForDump(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewManifestSigner([]byte(base64.RawStdEncoding.EncodeToString(private)))
	if err != nil {
		t.Fatal(err)
	}
	dump := []byte("binary-dump-" + inspectionCanary)
	manifest := validTestManifest(dump)
	manifest.RequiredSigningKeyIDs = []string{"audit-key-one"}
	manifest.EncryptionRecipients = []string{digestHex([]byte(identity.Recipient().String()))}
	manifestBytes, envelopeBytes, err := signer.Sign(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.age")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := age.Encrypt(file, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(encrypted)
	for _, entry := range []struct {
		name string
		data []byte
	}{{dumpEntryName, dump}, {manifestEntryName, manifestBytes}, {envelopeEntryName, envelopeBytes}} {
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	bundle, err := VerifyBundle(path, []age.Identity{identity}, []ed25519.PublicKey{public}, []string{"audit-key-one"})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := bundle.OpenDump()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if !bytes.Equal(got, dump) {
		t.Fatalf("dump = %q, want %q", got, dump)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.OpenDump(); err == nil {
		t.Fatal("closed bundle reopened dump")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(inspectionCanary)) {
		t.Fatal("encrypted bundle exposed canary plaintext")
	}
	if _, err := VerifyBundle(path, []age.Identity{identity}, []ed25519.PublicKey{public}, nil); err == nil {
		t.Fatal("bundle verified without required signing-key inventory")
	}
}

func validTestManifest(dump []byte) RecoveryManifest {
	manifest := RecoveryManifest{
		Format: recoveryBundleFormat, BundleID: uuid.NewString(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Authority: uuid.NewString(), SourceDatabaseFingerprint: digestHex([]byte("source")), Schema: "public",
		PostgreSQLVersion: "160015", PGDumpVersion: "pg_dump (PostgreSQL) 16.15",
		RelationshipContract: "norn.control-relationships/v1", RelationshipsValid: true,
		EncryptionRecipients: []string{digestHex([]byte("recipient"))},
		Sections:             []ManifestSection{{Name: dumpEntryName, Format: "postgresql-custom", Size: int64(len(dump)), SHA256: digestHex(dump)}},
	}
	for _, migration := range inspectionCatalog {
		manifest.Catalog = append(manifest.Catalog, ManifestCatalogEntry{migration.version, migration.name, migration.checksum, migration.minimumReader, migration.minimumWriter})
	}
	for _, table := range InspectionRegistry() {
		manifest.Tables = append(manifest.Tables, ManifestTableCount{Name: table.Name})
	}
	return manifest
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
