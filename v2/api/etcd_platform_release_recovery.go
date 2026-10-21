package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type platformRecoveryMemberObservation struct {
	Name              string    `json:"name"`
	APISHA            string    `json:"apiSha"`
	UISHA             string    `json:"uiSha"`
	ServiceActive     bool      `json:"serviceActive"`
	ObservedSourceSHA string    `json:"observedSourceSha"`
	Health            string    `json:"health"`
	ObservedAt        time.Time `json:"observedAt"`
	EvidenceSHA256    string    `json:"evidenceSha256"`
}

type platformRecoveryStopAttestation struct {
	SchemaVersion            string                              `json:"schemaVersion"`
	IntentName               string                              `json:"intentName"`
	IntentDigest             string                              `json:"intentDigest"`
	AttemptID                string                              `json:"attemptId"`
	OldFenceEpoch            int64                               `json:"oldFenceEpoch"`
	OldOwnerID               string                              `json:"oldOwnerId"`
	OldOwnerSession          string                              `json:"oldOwnerSession"`
	OldLeaseExpiresAt        time.Time                           `json:"oldLeaseExpiresAt"`
	SupervisorID             string                              `json:"supervisorId"`
	StoppedAt                time.Time                           `json:"stoppedAt"`
	ProcessAbsent            bool                                `json:"processAbsent"`
	CgroupAbsent             bool                                `json:"cgroupAbsent"`
	ManagerUnitInactive      bool                                `json:"managerUnitInactive"`
	GrantDeadlineBound       time.Time                           `json:"grantDeadlineBound"`
	MemberObservations       []platformRecoveryMemberObservation `json:"memberObservations"`
	MemberObservationsSHA256 string                              `json:"memberObservationsSha256"`
	RecoveryNonce            string                              `json:"recoveryNonce"`
}

func verifyPlatformRecoveryStopAttestation(raw, sig []byte, key ed25519.PublicKey, keyPEMSHA256, supervisorID string, intent *etcdstore.PlatformReleaseIntent, attempt *etcdstore.PlatformReleaseAttempt, checkpoints []etcdstore.PlatformReleaseCheckpoint, now time.Time) (etcdstore.PlatformReleaseRecoveryAuthorization, error) {
	invalid := etcdstore.PlatformReleaseRecoveryAuthorization{}
	if len(key) != ed25519.PublicKeySize || len(raw) == 0 || len(raw) > 64<<10 || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, raw, sig) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if _, err := platformGenesisCanonical(raw); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	var att platformRecoveryStopAttestation
	if err := platformGenesisStrict(raw, &att); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if intent == nil || attempt == nil || att.SchemaVersion != "norn.fleet-platform-recovery-stop/v1" || att.IntentName != intent.Name || att.IntentDigest != intent.IntentDigest || att.AttemptID != attempt.ID || att.OldFenceEpoch != attempt.FenceEpoch || att.OldOwnerID != attempt.OwnerID || att.OldOwnerSession != attempt.OwnerSession || !att.OldLeaseExpiresAt.Equal(attempt.LeaseExpiresAt) || att.SupervisorID != supervisorID || !att.ProcessAbsent || !att.CgroupAbsent || !att.ManagerUnitInactive || len(att.RecoveryNonce) < 32 || len(att.RecoveryNonce) > 128 {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if att.StoppedAt.IsZero() || att.StoppedAt.Before(attempt.LeaseExpiresAt) || now.Before(att.StoppedAt) || now.Sub(att.StoppedAt) > 10*time.Minute || now.Before(att.GrantDeadlineBound) || now.Before(attempt.LeaseExpiresAt) || att.GrantDeadlineBound.After(attempt.LeaseExpiresAt) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	if len(att.MemberObservations) != len(intent.ControlRoster) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	openPromote := make(map[string]bool)
	for index, checkpoint := range checkpoints {
		if checkpoint.Ordinal != int64(index+1) {
			return invalid, etcdstore.ErrPlatformReleaseInvalid
		}
		switch checkpoint.Phase {
		case "promote:begin":
			openPromote[checkpoint.ControlName] = true
		case "promote:done":
			openPromote[checkpoint.ControlName] = false
		}
	}
	for index, item := range att.MemberObservations {
		if item.Name != intent.ControlRoster[index].Name || !platformReleaseSourceSHA(item.APISHA, intent) || !platformReleaseSourceSHA(item.UISHA, intent) || !platformReleaseDigestHex(item.EvidenceSHA256) || item.ObservedAt.IsZero() || item.ObservedAt.Before(att.StoppedAt) || item.ObservedAt.After(now) || now.Sub(item.ObservedAt) > 10*time.Minute {
			return invalid, etcdstore.ErrPlatformReleaseInvalid
		}
		coherent := item.APISHA == item.UISHA && item.ServiceActive && item.Health == "healthy" && item.ObservedSourceSHA == item.APISHA
		interrupted := openPromote[item.Name] && item.Health == "failed" && (!item.ServiceActive || item.APISHA != item.UISHA) && ((item.APISHA == item.UISHA && item.ObservedSourceSHA == item.APISHA) || (item.APISHA != item.UISHA && item.ObservedSourceSHA == "mixed"))
		if !coherent && !interrupted {
			return invalid, etcdstore.ErrPlatformReleaseInvalid
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	members := fields["memberObservations"]
	memberDigest := sha256.Sum256(members)
	if att.MemberObservationsSHA256 != hex.EncodeToString(memberDigest[:]) {
		return invalid, etcdstore.ErrPlatformReleaseInvalid
	}
	stopDigest := sha256.Sum256(raw)
	return etcdstore.PlatformReleaseRecoveryAuthorization{AttemptID: attempt.ID, ExpectedFenceEpoch: attempt.FenceEpoch, IntentDigest: intent.IntentDigest, RecoveryNonce: att.RecoveryNonce, OldOwnerStopAttestationSHA256: hex.EncodeToString(stopDigest[:]), ObservedSignedMemberStateSHA256: att.MemberObservationsSHA256, SupervisorID: supervisorID, SupervisorPublicKeySHA256: keyPEMSHA256, AttestationBytesBase64: base64.StdEncoding.EncodeToString(raw), AttestationSignatureBase64: base64.StdEncoding.EncodeToString(sig)}, nil
}

func platformReleaseSourceSHA(value string, intent *etcdstore.PlatformReleaseIntent) bool {
	return value == intent.OldSourceSHA || value == intent.NewSourceSHA
}

func platformReleaseDigestHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == hex.EncodeToString(decoded)
}

func registerEtcdPlatformReleaseRecoveryRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations *etcdstore.V3OperationStore) {
	if !cfg.FleetPlatformReleaseIntentEnabled || !cfg.FleetPlatformRecoveryEnabled || cfg.FleetPlatformRecoverySupervisorPublicKeyFile == "" || cfg.FleetPlatformRecoverySupervisorID == "" {
		return
	}
	key, keyPEMSHA256, err := platformGenesisPublicKey(cfg.FleetPlatformRecoverySupervisorPublicKeyFile)
	if err != nil {
		return
	}
	// A recovery supervisor must be independent of either genesis authority.
	for _, path := range []string{cfg.FleetPlatformGenesisIntentPublicKeyFile, cfg.FleetPlatformGenesisCompletionPublicKeyFile} {
		if path == "" {
			continue
		}
		other, _, err := platformGenesisPublicKey(path)
		if err != nil || bytes.Equal(key, other) {
			return
		}
	}
	root := "/api/v1/fleet/platform-releases/{name}/attempt"
	router.With(platformReleaseOperatorApprovalAuth(cfg, identities)).Post(root+"/recovery-authorizations", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AttestationBytesBase64     string `json:"attestationBytesBase64"`
			AttestationSignatureBase64 string `json:"attestationSignatureBase64"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(request.AttestationBytesBase64)
		if err != nil {
			platformReleaseError(w, r, etcdstore.ErrPlatformReleaseInvalid)
			return
		}
		signature, err := base64.StdEncoding.Strict().DecodeString(request.AttestationSignatureBase64)
		if err != nil {
			platformReleaseError(w, r, etcdstore.ErrPlatformReleaseInvalid)
			return
		}
		name := chi.URLParam(r, "name")
		intent, err := operations.GetPlatformReleaseIntent(r.Context(), name)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		attempt, err := operations.GetPlatformReleaseAttempt(r.Context(), name)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		checkpoints, err := operations.ListPlatformReleaseCheckpoints(r.Context(), name)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		auth, err := verifyPlatformRecoveryStopAttestation(raw, signature, key, keyPEMSHA256, cfg.FleetPlatformRecoverySupervisorID, intent, attempt, checkpoints, time.Now().UTC())
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		principal, _ := handler.AccessPrincipalFromRequest(r)
		auth.ApprovedBy = principal.Subject
		auth.ApprovedByDeviceID = principal.DeviceID
		record, err := operations.AuthorizePlatformReleaseRecovery(r.Context(), name, auth)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusCreated, record)
	})
	router.With(platformReleaseExecutorAuth(cfg, identities)).Post(root+"/recover", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AttemptID                       string `json:"attemptId"`
			OwnerID                         string `json:"ownerId"`
			OwnerSession                    string `json:"ownerSession"`
			ExpectedFenceEpoch              int64  `json:"expectedFenceEpoch"`
			RecoveryNonce                   string `json:"recoveryNonce"`
			OldOwnerStopAttestationSHA256   string `json:"oldOwnerStopAttestationSha256"`
			ObservedSignedMemberStateSHA256 string `json:"observedSignedMemberStateSha256"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		principal, _ := handler.AccessPrincipalFromRequest(r)
		if principal.DeviceID != request.OwnerID {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "platform_release_owner_mismatch", "attempt owner must be this executor device")
			return
		}
		record, err := operations.RecoverPlatformReleaseAttempt(r.Context(), chi.URLParam(r, "name"), request.AttemptID, request.OwnerID, request.OwnerSession, request.RecoveryNonce, request.OldOwnerStopAttestationSHA256, request.ObservedSignedMemberStateSHA256, request.ExpectedFenceEpoch)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
}
