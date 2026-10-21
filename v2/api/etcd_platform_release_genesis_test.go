package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
)

func TestVerifyPlatformGenesisPythonFixture(t *testing.T) {
	directory := os.Getenv("NORN_TEST_GENESIS_FIXTURE_DIR")
	if directory == "" {
		t.Skip("NORN_TEST_GENESIS_FIXTURE_DIR is not set")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	intentKey, intentKeyHash, err := platformGenesisPublicKey(filepath.Join(directory, "fixture-intent-public.pem"))
	if err != nil {
		t.Fatal(err)
	}
	completionKey, completionKeyHash, err := platformGenesisPublicKey(filepath.Join(directory, "fixture-completion-public.pem"))
	if err != nil {
		t.Fatal(err)
	}
	intentBytes := read("intent.json")
	var intent etcdstore.PlatformReleaseIntent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatal(err)
	}
	request := platformGenesisImportRequest{IntentBytesBase64: base64.StdEncoding.EncodeToString(intentBytes), IntentSignatureBase64: base64.StdEncoding.EncodeToString(read("intent.sig")), CompletionBytesBase64: base64.StdEncoding.EncodeToString(read("completion.json")), CompletionSignatureBase64: base64.StdEncoding.EncodeToString(read("completion.sig")), Checkpoints: read("checkpoints.json")}
	if _, err := verifyPlatformGenesisImport(request, intent.Name, intentKey, completionKey, intentKeyHash, completionKeyHash); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformGenesisImportRouteRequiresPinnedKey(t *testing.T) {
	for _, path := range []string{"", "/missing/genesis-public.pem"} {
		router := chi.NewRouter()
		registerEtcdPlatformGenesisImportRoutes(router, &config.Config{FleetPlatformReleaseIntentEnabled: true, FleetPlatformGenesisIntentPublicKeyFile: path, FleetPlatformGenesisCompletionPublicKeyFile: path}, nil, nil)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/platform-releases/example/genesis-import", nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("untrusted key path %q mounted import route: %d", path, response.Code)
		}
	}
}

func TestVerifyPlatformGenesisImportSignedBindings(t *testing.T) {
	intentPublic, intentPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	pemHash := sha256.Sum256(pemBytes)
	publicPEMSHA := hex.EncodeToString(pemHash[:])
	intentDER, err := x509.MarshalPKIXPublicKey(intentPublic)
	if err != nil {
		t.Fatal(err)
	}
	intentPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: intentDER})
	intentHash := sha256.Sum256(intentPEM)
	intentPEMSHA := hex.EncodeToString(intentHash[:])
	intent := etcdstore.PlatformReleaseIntent{
		SchemaVersion: 1, Name: "platform-genesis", Generation: 1, Environment: "staging",
		OldSourceSHA: strings.Repeat("a", 40), NewSourceSHA: strings.Repeat("b", 40), OldPublisherRunID: 123, PublisherRunID: 124,
		OldSignedManifestSHA256: strings.Repeat("c", 64), NewSignedManifestSHA256: strings.Repeat("d", 64),
		SignedReleaseTag: "platform-" + strings.Repeat("b", 40),
		ControlRoster: []etcdstore.PlatformReleaseControl{
			{Name: "control-01", TailnetIP: "100.1.2.3", PrivateIP: "10.1.2.3"},
			{Name: "control-02", TailnetIP: "100.1.2.4", PrivateIP: "10.1.2.4"},
			{Name: "control-03", TailnetIP: "100.1.2.5", PrivateIP: "10.1.2.5"},
		}, StateSHA256: strings.Repeat("e", 64), HostKeysSHA256: strings.Repeat("f", 64), Status: "Approved",
	}
	digest, err := etcdstore.PlatformReleaseIntentDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.IntentDigest = digest
	rawIntent, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	var intentMap map[string]interface{}
	if err := json.Unmarshal(rawIntent, &intentMap); err != nil {
		t.Fatal(err)
	}
	delete(intentMap, "createdAt")
	rawIntent, err = json.Marshal(intentMap)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := []map[string]interface{}{{"ordinal": 1, "phase": "complete", "controlName": "", "observedSha": intent.NewSourceSHA, "health": "healthy", "evidenceSha256": strings.Repeat("1", 64)}}
	checkpointBytes, err := json.Marshal(checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	checkpointHash := sha256.Sum256(checkpointBytes)
	rosterHash, err := platformGenesisRosterDigest(intent.ControlRoster)
	if err != nil {
		t.Fatal(err)
	}
	completion := map[string]interface{}{
		"schemaVersion": "norn.fleet-platform-genesis-completion/v1", "status": "Succeeded", "completedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"controlRosterSha256": rosterHash, "intentDigest": digest, "attemptId": "attempt-123", "ownerId": "device-123", "fenceEpoch": 1,
		"checkpointCount": 1, "checkpointsSha256": hex.EncodeToString(checkpointHash[:]), "oldSourceSha": intent.OldSourceSHA, "newSourceSha": intent.NewSourceSHA, "signerPublicKeySha256": publicPEMSHA,
		"targetCapability": "fleet-platform-release-intent-v1",
	}
	rawCompletion, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.StdEncoding.EncodeToString
	request := platformGenesisImportRequest{IntentBytesBase64: encode(rawIntent), IntentSignatureBase64: encode(ed25519.Sign(intentPrivate, rawIntent)), CompletionBytesBase64: encode(rawCompletion), CompletionSignatureBase64: encode(ed25519.Sign(private, rawCompletion)), Checkpoints: checkpointBytes}
	imported, err := verifyPlatformGenesisImport(request, intent.Name, intentPublic, public, intentPEMSHA, publicPEMSHA)
	if err != nil {
		t.Fatal(err)
	}
	if imported.IntentDigest != digest || imported.CheckpointCount != 1 {
		t.Fatalf("unexpected import: %+v", imported)
	}
	tampered := request
	tampered.Checkpoints = []byte(`[]`)
	if _, err := verifyPlatformGenesisImport(tampered, intent.Name, intentPublic, public, intentPEMSHA, publicPEMSHA); err == nil {
		t.Fatal("accepted changed checkpoints")
	}
	tampered = request
	tampered.CompletionSignatureBase64 = encode(make([]byte, ed25519.SignatureSize))
	if _, err := verifyPlatformGenesisImport(tampered, intent.Name, intentPublic, public, intentPEMSHA, publicPEMSHA); err == nil {
		t.Fatal("accepted bad signature")
	}
	if _, err := verifyPlatformGenesisImport(request, intent.Name, intentPublic, public, intentPEMSHA, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted unpinned public key hash")
	}
	if _, err := verifyPlatformGenesisImport(request, intent.Name, public, intentPublic, publicPEMSHA, intentPEMSHA); err == nil {
		t.Fatal("accepted swapped approval and completion keys")
	}
	if _, err := verifyPlatformGenesisImport(request, intent.Name, public, public, publicPEMSHA, publicPEMSHA); err == nil {
		t.Fatal("accepted one key for both authorities")
	}
}
