package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type platformGenesisCompletion struct {
	SchemaVersion         string    `json:"schemaVersion"`
	Status                string    `json:"status"`
	CompletedAt           time.Time `json:"completedAt"`
	IntentDigest          string    `json:"intentDigest"`
	AttemptID             string    `json:"attemptId"`
	OwnerID               string    `json:"ownerId"`
	FenceEpoch            int64     `json:"fenceEpoch"`
	CheckpointCount       int       `json:"checkpointCount"`
	CheckpointsSHA256     string    `json:"checkpointsSha256"`
	ControlRosterSHA256   string    `json:"controlRosterSha256"`
	OldSourceSHA          string    `json:"oldSourceSha"`
	NewSourceSHA          string    `json:"newSourceSha"`
	SignerPublicKeySHA256 string    `json:"signerPublicKeySha256"`
	TargetCapability      string    `json:"targetCapability"`
}

type platformGenesisCheckpoint struct {
	Ordinal        int64  `json:"ordinal"`
	Phase          string `json:"phase"`
	ControlName    string `json:"controlName"`
	ObservedSHA    string `json:"observedSha"`
	Health         string `json:"health"`
	EvidenceSHA256 string `json:"evidenceSha256"`
}

type platformGenesisImportRequest struct {
	IntentBytesBase64         string          `json:"intentBytesBase64"`
	IntentSignatureBase64     string          `json:"intentSignatureBase64"`
	CompletionBytesBase64     string          `json:"completionBytesBase64"`
	CompletionSignatureBase64 string          `json:"completionSignatureBase64"`
	Checkpoints               json.RawMessage `json:"checkpoints"`
}

func platformGenesisPublicKey(path string) (ed25519.PublicKey, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "PUBLIC KEY" {
		return nil, "", errors.New("genesis public key must be one PKIX PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, "", errors.New("genesis public key must be Ed25519")
	}
	sum := sha256.Sum256(raw)
	return key, hex.EncodeToString(sum[:]), nil
}

// json.Marshal sorts map keys, matching the genesis controller's compact
// sorted-key JSON. Reject any alternate byte representation before verifying
// signatures, so evidence digests have one unambiguous representation.
func platformGenesisCanonical(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, raw) {
		return nil, errors.New("genesis JSON is not canonical")
	}
	return canonical, nil
}

func platformGenesisStrict(raw []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func platformGenesisRosterDigest(roster []etcdstore.PlatformReleaseControl) (string, error) {
	items := make([]map[string]string, 0, len(roster))
	for _, control := range roster {
		items = append(items, map[string]string{"name": control.Name, "privateIp": control.PrivateIP, "tailnetIp": control.TailnetIP})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func verifyPlatformGenesisImport(request platformGenesisImportRequest, name string, intentKey, completionKey ed25519.PublicKey, intentPEMSHA256, completionPEMSHA256 string) (etcdstore.PlatformReleaseGenesisImport, error) {
	invalid := etcdstore.PlatformReleaseGenesisImport{}
	if len(intentKey) != ed25519.PublicKeySize || len(completionKey) != ed25519.PublicKeySize || bytes.Equal(intentKey, completionKey) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	decode := func(value string) ([]byte, error) { return base64.StdEncoding.Strict().DecodeString(value) }
	intentBytes, err := decode(request.IntentBytesBase64)
	if err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	intentSig, err := decode(request.IntentSignatureBase64)
	if err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	completionBytes, err := decode(request.CompletionBytesBase64)
	if err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	completionSig, err := decode(request.CompletionSignatureBase64)
	if err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if len(intentBytes) > 64<<10 || len(completionBytes) > 64<<10 || len(intentSig) != ed25519.SignatureSize || len(completionSig) != ed25519.SignatureSize || !ed25519.Verify(intentKey, intentBytes, intentSig) || !ed25519.Verify(completionKey, completionBytes, completionSig) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if _, err := platformGenesisCanonical(intentBytes); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if _, err := platformGenesisCanonical(completionBytes); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	var intent etcdstore.PlatformReleaseIntent
	if err := platformGenesisStrict(intentBytes, &intent); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if intent.Name != name || intent.SchemaVersion != 1 || intent.Status != "Approved" || intent.Validate() != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	digest, err := etcdstore.PlatformReleaseIntentDigest(intent)
	if err != nil || intent.IntentDigest != digest {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	var completion platformGenesisCompletion
	if err := platformGenesisStrict(completionBytes, &completion); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if completion.SchemaVersion != "norn.fleet-platform-genesis-completion/v1" || completion.Status != "Succeeded" || completion.TargetCapability != "fleet-platform-release-intent-v1" || completion.CompletedAt.IsZero() || completion.CompletedAt.After(time.Now().Add(5*time.Minute)) || completion.IntentDigest != digest || completion.OldSourceSHA != intent.OldSourceSHA || completion.NewSourceSHA != intent.NewSourceSHA || completion.AttemptID == "" || completion.OwnerID == "" || completion.FenceEpoch < 1 || completion.CheckpointCount < 1 || completion.CheckpointCount > 1024 {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if completion.SignerPublicKeySHA256 != completionPEMSHA256 {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	rosterDigest, err := platformGenesisRosterDigest(intent.ControlRoster)
	if err != nil || completion.ControlRosterSHA256 != rosterDigest {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	checkpointBytes, err := platformGenesisCanonical(request.Checkpoints)
	if err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	var checkpoints []platformGenesisCheckpoint
	if err := platformGenesisStrict(checkpointBytes, &checkpoints); err != nil || len(checkpoints) != completion.CheckpointCount {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	validControls := map[string]bool{}
	for _, control := range intent.ControlRoster {
		validControls[control.Name] = true
	}
	for index, item := range checkpoints {
		if item.Ordinal != int64(index+1) || item.Phase == "" || (item.ControlName == "" && item.Phase != "complete") || (item.ControlName != "" && !validControls[item.ControlName]) || (item.Health != "healthy" && item.Health != "failed") || len(item.ObservedSHA) != 40 || len(item.EvidenceSHA256) != 64 {
			return invalid, etcdstore.ErrPlatformReleaseInvalid
		}
	}
	if checkpoints[len(checkpoints)-1].Phase != "complete" || checkpoints[len(checkpoints)-1].Health != "healthy" {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	checkpointDigest := sha256.Sum256(checkpointBytes)
	if completion.CheckpointsSHA256 != hex.EncodeToString(checkpointDigest[:]) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	return etcdstore.PlatformReleaseGenesisImport{Name: name, IntentDigest: digest, IntentBytesBase64: request.IntentBytesBase64, IntentSignatureBase64: request.IntentSignatureBase64, CompletionBytesBase64: request.CompletionBytesBase64, CompletionSignatureBase64: request.CompletionSignatureBase64, CheckpointsJSON: append(json.RawMessage(nil), checkpointBytes...), CheckpointCount: completion.CheckpointCount, CheckpointsSHA256: completion.CheckpointsSHA256, SignerPublicKeySHA256: completion.SignerPublicKeySHA256, IntentPublicKeySHA256: intentPEMSHA256}, nil
}

func registerEtcdPlatformGenesisImportRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations *etcdstore.V3OperationStore) {
	if !cfg.FleetPlatformReleaseIntentEnabled || cfg.FleetPlatformGenesisIntentPublicKeyFile == "" || cfg.FleetPlatformGenesisCompletionPublicKeyFile == "" {
		return
	}
	intentKey, intentPEMSHA256, err := platformGenesisPublicKey(cfg.FleetPlatformGenesisIntentPublicKeyFile)
	if err != nil {
		return
	}
	completionKey, completionPEMSHA256, err := platformGenesisPublicKey(cfg.FleetPlatformGenesisCompletionPublicKeyFile)
	if err != nil {
		return
	} // invalid pin fails closed; readiness validation is separate
	if bytes.Equal(intentKey, completionKey) {
		return
	}
	root := "/api/v1/fleet/platform-releases/{name}/genesis-import"
	router.With(platformReleaseReadOrExecutorAuth(cfg, identities)).Get(root, func(w http.ResponseWriter, r *http.Request) {
		record, err := operations.GetPlatformReleaseGenesisImport(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
	router.With(platformReleaseOperatorApprovalAuth(cfg, identities)).Post(root, func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
		decoder.DisallowUnknownFields()
		var request platformGenesisImportRequest
		if err := decoder.Decode(&request); err != nil {
			platformReleaseError(w, r, etcdstore.ErrPlatformReleaseInvalid)
			return
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); err != io.EOF {
			platformReleaseError(w, r, etcdstore.ErrPlatformReleaseInvalid)
			return
		}
		record, err := verifyPlatformGenesisImport(request, chi.URLParam(r, "name"), intentKey, completionKey, intentPEMSHA256, completionPEMSHA256)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		principal, _ := handler.AccessPrincipalFromRequest(r)
		record.OperatorSubject = principal.Subject
		record.OperatorDeviceID = principal.DeviceID
		stored, created, err := operations.ImportPlatformReleaseGenesis(r.Context(), record)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		platformReleaseResponse(w, status, stored)
	})
}
