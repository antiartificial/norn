package main

// This endpoint reserves one reviewed work-staging contraction under the
// shared Fleet target fence. It cannot issue credentials or run an effect.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineFleetTrust struct {
	keyID           string
	public          ed25519.PublicKey
	runnerSubject   string
	verifierSubject string
}

func loadOfflineFleetTrust(cfg *config.Config) (*offlineFleetTrust, error) {
	if cfg == nil {
		return nil, nil
	}
	keyID, path := strings.TrimSpace(cfg.FleetOfflineOwnerKeyID), strings.TrimSpace(cfg.FleetOfflineOwnerPublicKeyFile)
	subject, verifier := strings.TrimSpace(cfg.FleetOfflineRunnerSubject), strings.TrimSpace(cfg.FleetOfflineVerifierSubject)
	if keyID == "" && path == "" && subject == "" && verifier == "" {
		return nil, nil
	}
	if !cfg.FleetAuthorityOnly || cfg.Production() || cfg.ProfileID() != "development" || cfg.EnvironmentID() != "staging" || keyID == "" || path == "" || subject == "" || verifier == "" || subject == verifier || strings.ContainsAny(keyID, " \t\n\r") || strings.ContainsAny(subject, " \t\n\r") || strings.ContainsAny(verifier, " \t\n\r") {
		return nil, fmt.Errorf("offline trust requires authority-only staging and distinct complete pinned runner and verifier identities")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("offline owner key path must be absolute")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open offline owner public key: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat offline owner public key: %w", err)
	}
	stat, ownerOK := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 256 || !ownerOK || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return nil, fmt.Errorf("offline owner public key must be a non-writable regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return nil, fmt.Errorf("read offline owner public key: %w", err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("offline owner public key must be raw-base64 Ed25519")
	}
	return &offlineFleetTrust{keyID: keyID, public: ed25519.PublicKey(decoded), runnerSubject: subject, verifierSubject: verifier}, nil
}

type offlineReserveRequest struct {
	Binding  json.RawMessage `json:"binding"`
	Approval json.RawMessage `json:"approval"`
}

type offlineIdentityStore interface {
	store.IdentityStore
	ActiveAccessDevice(context.Context, string) (*store.AccessDevice, error)
}

func registerEtcdFleetOfflineRoutes(router chi.Router, cfg *config.Config, identities offlineIdentityStore, operations *etcdstore.V3OperationStore, trust *offlineFleetTrust, origin ...*githubapp.Client) {
	if trust == nil {
		return
	}
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAdmin)).Post("/api/v1/fleet/offline/reservations", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.Legacy || principal.TokenID == "" || principal.DeviceID == "" || principal.CI != nil || !principal.Allows(handler.ScopeAdmin) || principal.Subject == trust.runnerSubject {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_reservation_principal_invalid", "offline reservation requires a distinct device-bound administrator")
			return
		}
		if !offlineReservationDeviceActive(r.Context(), identities, principal.DeviceID) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_reservation_device_inactive", "offline reservation requires an active registered administrator device")
			return
		}
		var request offlineReserveRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_reservation_invalid", "invalid reservation body")
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_reservation_invalid", "reservation body must contain one value")
			return
		}
		binding, err := offline.DecodeBinding(request.Binding)
		if err != nil || binding.RunnerPrincipalSubject != trust.runnerSubject {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_binding_invalid", "binding is invalid or runner identity differs")
			return
		}
		approval, err := offline.DecodeSignedApproval(request.Approval)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_approval_invalid", "signed approval is invalid")
			return
		}
		now := time.Now().UTC()
		verified, err := offline.VerifySignedApproval(approval, trust.keyID, trust.public, binding, now)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_approval_invalid", "owner approval does not authorize this binding")
			return
		}
		reserved, err := operations.ReserveOfflineDispatch(r.Context(), binding, verified, now)
		if err != nil {
			status := http.StatusConflict
			if errors.Is(err, offline.ErrInvalidBinding) {
				status = http.StatusBadRequest
			}
			handler.WriteControlProblem(w, r, status, "offline_reservation_refused", "offline reservation or shared target fence refused the request")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			Binding          offline.Binding `json:"binding"`
			AuthorizesEffect bool            `json:"authorizesEffect"`
		}{reserved, false})
	})
	// The runner route is deliberately non-effect. It accepts one immutable
	// queued attempt and one prechange observation; no credential or provider
	// endpoint is mounted here.
	runnerAuth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetOperate)
	router.With(runnerAuth).Post("/api/v1/fleet/offline/attempts", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := offlineRunnerPrincipal(r, trust, identities)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_invalid", "a pinned active device-bound runner is required")
			return
		}
		var request struct {
			Claim     offline.AttemptClaim `json:"claim"`
			AttemptID string               `json:"attemptId"`
		}
		if !decodeOfflineRequest(w, r, &request) || uuid.Validate(request.AttemptID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_attempt_invalid", "invalid offline attempt request")
			return
		}
		attempt, err := operations.AdmitOfflineAttempt(r.Context(), request.Claim, etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID}, request.AttemptID, time.Now().UTC())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_attempt_refused", "offline attempt or shared target fence refused the request")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			Attempt          offline.Attempt `json:"attempt"`
			AuthorizesEffect bool            `json:"authorizesEffect"`
		}{attempt, false})
	})
	router.With(runnerAuth).Post("/api/v1/fleet/offline/attempts/{planID}/{attemptID}/prechange-observations", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := offlineRunnerPrincipal(r, trust, identities)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_invalid", "a pinned active device-bound runner is required")
			return
		}
		var request struct {
			EvidenceSHA256   string          `json:"evidenceSHA256"`
			ExpectedSequence uint64          `json:"expectedSequence"`
			Receipt          json.RawMessage `json:"receipt"`
		}
		if !decodeOfflineRequest(w, r, &request) || len(request.Receipt) == 0 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_checkpoint_invalid", "invalid checkpoint request")
			return
		}
		checkpoint, err := operations.RecordOfflineCheckpoint(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID}, "prechange_observed", request.EvidenceSHA256, request.ExpectedSequence, time.Now().UTC(), request.Receipt)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_checkpoint_refused", "offline checkpoint or shared target fence refused the request")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			Checkpoint       offline.Checkpoint `json:"checkpoint"`
			AuthorizesEffect bool               `json:"authorizesEffect"`
		}{checkpoint, false})
	})
	registerEtcdFleetOfflineEffectRoutes(router, runnerAuth, identities, trust, offlineEffectAuthorityForRuntime(cfg, operations, origin...), etcdstore.NewOfflineEffectRecovery(operations))
	registerEtcdFleetOfflineEvidenceReadRoutes(router, cfg, identities, operations, trust)
}

func offlineRunnerPrincipal(r *http.Request, trust *offlineFleetTrust, identities offlineIdentityStore) (handler.AccessPrincipal, bool) {
	principal, ok := handler.AccessPrincipalFromRequest(r)
	if !ok || trust == nil || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.Legacy ||
		principal.Subject != trust.runnerSubject || principal.TokenID == "" || principal.DeviceID == "" || principal.CI != nil ||
		len(principal.Scopes) != 1 || principal.Scopes[0] != handler.ScopeFleetOperate ||
		!offlineReservationDeviceActive(r.Context(), identities, principal.DeviceID) {
		return handler.AccessPrincipal{}, false
	}
	return principal, true
}

func offlineVerifierPrincipal(r *http.Request, trust *offlineFleetTrust, identities offlineIdentityStore) (handler.AccessPrincipal, bool) {
	principal, ok := handler.AccessPrincipalFromRequest(r)
	if !ok || trust == nil || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.Legacy ||
		principal.Subject != trust.verifierSubject || principal.Subject == trust.runnerSubject || principal.TokenID == "" || principal.DeviceID == "" || principal.CI != nil ||
		len(principal.Scopes) != 1 || principal.Scopes[0] != handler.ScopeFleetVerify ||
		!offlineReservationDeviceActive(r.Context(), identities, principal.DeviceID) {
		return handler.AccessPrincipal{}, false
	}
	return principal, true
}

func decodeOfflineRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func offlineReservationDeviceActive(ctx context.Context, identities offlineIdentityStore, deviceID string) bool {
	if identities == nil || deviceID == "" {
		return false
	}
	device, err := identities.ActiveAccessDevice(ctx, deviceID)
	return err == nil && device != nil && device.ID == deviceID && device.RevokedAt == nil
}
