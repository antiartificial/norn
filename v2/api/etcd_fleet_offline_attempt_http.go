package main

// Admission and readback for an offline Fleet attempt. This surface records a
// queued attempt only; it has no credential, phase advance or provider route.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineAttemptAdmissionStore interface {
	AdmitOfflineAttemptWithRunnerProof(context.Context, offline.AttemptClaim, etcdstore.OfflineRunnerProof, string, time.Time) (offline.Attempt, error)
	GetOfflineAttempt(context.Context, string) (offline.Attempt, error)
}

type offlineAttemptDeviceStore interface {
	ActiveAccessDevice(context.Context, string) (*store.AccessDevice, error)
	ProveOfflineRunner(context.Context, string, time.Time) (etcdstore.OfflineRunnerProof, error)
}

func registerEtcdFleetOfflineRoutes(router chi.Router, cfg *config.Config, identities *etcdstore.AuthStore, attempts *etcdstore.V3OperationStore,
	ownerPublicKey ed25519.PublicKey, reservationVerifier OfflineReservationEvidenceVerifier, effectSource ...OfflineEffectEvidenceSourceV2) {
	offlineAuth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetOfflineAttempt)
	router.With(offlineAuth).Post("/api/v1/fleet/offline/plans/{planID}/attempt", etcdOfflineAttemptCreate(attempts, identities))
	router.With(offlineAuth).Get("/api/v1/fleet/offline/plans/{planID}/attempt", etcdOfflineAttemptGet(attempts, identities))
	registerEtcdFleetOfflineEffectGrantRoutes(router, offlineAuth, attempts, identities, effectSource...)
	registerEtcdFleetOfflineVerifierRoute(router, cfg, identities, attempts)
	enrollment := handler.NewEtcdOfflineEnrollmentHandler(cfg, identities)
	adminAuth := etcdManagedTokenAuth(cfg, identities, handler.ScopeAdmin)
	router.Post("/api/v1/fleet/offline/enrollments", enrollment.StartDeviceEnrollment)
	router.With(adminAuth).Get("/api/v1/fleet/offline/enrollments", enrollment.ListDeviceEnrollments)
	router.With(adminAuth).Post("/api/v1/fleet/offline/enrollments/approve", enrollment.ApproveDeviceEnrollment)
	router.Post("/api/v1/fleet/offline/enrollments/{id}/exchange", enrollment.ExchangeDeviceEnrollment)
	router.With(adminAuth).Delete("/api/v1/fleet/offline/devices/{id}", enrollment.RevokeDevice)
	if cfg.FleetOfflineReservationEnabled {
		router.With(adminAuth).Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(attempts, identities, reservationVerifier, cfg.FleetOfflineOwnerKeyID, ownerPublicKey))
		router.With(adminAuth).Get("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationGet(attempts))
	}
}

func offlineAttemptPrincipal(w http.ResponseWriter, r *http.Request, devices offlineAttemptDeviceStore) (handler.AccessPrincipal, bool) {
	p, ok := handler.AccessPrincipalFromRequest(r)
	if !ok || !handler.IsFleetOfflineAttemptPrincipal(p) || devices == nil {
		handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_required", "a dedicated offline runner device token is required")
		return handler.AccessPrincipal{}, false
	}
	device, err := devices.ActiveAccessDevice(r.Context(), p.DeviceID)
	if err != nil || device == nil || device.ID != p.DeviceID || device.RevokedAt != nil || !strings.EqualFold(strings.TrimSpace(device.Platform), handler.FleetOfflineRunnerPlatform) {
		handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_required", "a live offline runner device is required")
		return handler.AccessPrincipal{}, false
	}
	return p, true
}

func etcdOfflineAttemptCreate(attempts offlineAttemptAdmissionStore, devices offlineAttemptDeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := offlineAttemptPrincipal(w, r, devices)
		if !ok {
			return
		}
		planID := chi.URLParam(r, "planID")
		if uuid.Validate(planID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
			return
		}
		if attempts == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_attempt_store_unavailable", "offline attempt store is unavailable")
			return
		}
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
		claim, claimErr := offline.DecodeAttemptClaim(raw)
		if readErr != nil || claimErr != nil || claim.PlanID != planID || claim.RunnerPrincipalSubject != p.Subject {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_attempt", "claim must exactly match the path and authenticated principal")
			return
		}
		now := time.Now().UTC()
		proof, err := devices.ProveOfflineRunner(r.Context(), p.Subject, now)
		if err != nil || proof.TokenID != p.TokenID || proof.DeviceID != p.DeviceID {
			handler.WriteControlProblem(w, r, http.StatusPreconditionFailed, "offline_runner_not_current", "the authenticated offline runner token is not the reserved live credential")
			return
		}
		attempt, err := attempts.AdmitOfflineAttemptWithRunnerProof(r.Context(), claim, proof, uuid.NewString(), now)
		if err != nil {
			status, code := http.StatusConflict, "offline_attempt_conflict"
			if errors.Is(err, etcdstore.ErrNotFound) {
				status, code = http.StatusNotFound, "offline_dispatch_not_found"
			}
			if !errors.Is(err, offline.ErrAttemptMismatch) && !errors.Is(err, etcdstore.ErrOfflineAttemptConflict) && !errors.Is(err, etcdstore.ErrNotFound) {
				status, code = http.StatusServiceUnavailable, "offline_attempt_admission_uncertain"
			}
			handler.WriteControlProblem(w, r, status, code, "offline attempt was not admitted; read exact state before retry")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(attempt)
	}
}

// etcdOfflineAttemptGet is the canonical nonauthorizing readback for both
// queued and completed attempts. It requires a currently authenticated runner;
// POST completion replay is guaranteed only while that HTTP credential remains
// current. Store-level exact-tuple recovery remains independent of later fence,
// epoch and credential-revision changes.
func etcdOfflineAttemptGet(attempts offlineAttemptAdmissionStore, devices offlineAttemptDeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := offlineAttemptPrincipal(w, r, devices)
		if !ok {
			return
		}
		planID := chi.URLParam(r, "planID")
		if uuid.Validate(planID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
			return
		}
		if attempts == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_attempt_store_unavailable", "offline attempt store is unavailable")
			return
		}
		attempt, err := attempts.GetOfflineAttempt(r.Context(), planID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_attempt_not_found", "offline attempt not found")
			return
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_attempt_read_failed", "offline attempt could not be read")
			return
		}
		if attempt.PrincipalSubject != p.Subject {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_attempt_not_found", "offline attempt not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(attempt)
	}
}
