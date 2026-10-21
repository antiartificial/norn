package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
)

const enrollmentTTL = 10 * time.Minute
const deviceTokenTTL = 30 * 24 * time.Hour
const browserDeviceTokenTTL = 30 * time.Minute

type enrollmentStartRequest struct {
	DeviceName      string   `json:"deviceName"`
	Platform        string   `json:"platform"`
	Model           string   `json:"model"`
	AppVersion      string   `json:"appVersion"`
	PublicKey       string   `json:"publicKey"`
	RequestedScopes []string `json:"requestedScopes"`
}

// NewEtcdOfflineEnrollmentHandler reuses the existing pairing protocol with
// the etcd identity aggregate. Its scope policy permits only the dedicated
// offline attempt device, so it cannot bootstrap a general control token.
func NewEtcdOfflineEnrollmentHandler(cfg *config.Config, identities store.AuthStore) *Handler {
	return &Handler{cfg: cfg, deviceIdentities: identities, offlineEnrollmentOnly: true}
}

// NewEtcdFleetExecutorEnrollmentHandler serves the executor pairing protocol
// from the etcd identity aggregate. Every enrollment and token issuance path
// is restricted to the fleet-executor platform and fleet:execute alone.
func NewEtcdFleetExecutorEnrollmentHandler(cfg *config.Config, identities store.AuthStore) *Handler {
	return &Handler{cfg: cfg, deviceIdentities: identities, executorEnrollOnly: true}
}

func (h *Handler) deviceIdentityStore() store.AuthStore {
	if h == nil {
		return nil
	}
	if h.deviceIdentities != nil {
		return h.deviceIdentities
	}
	if h.db != nil {
		return h.db
	}
	return nil
}

func (h *Handler) StartDeviceEnrollment(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedPairingTransport(w, r) {
		return
	}
	var req enrollmentStartRequest
	if err := decodeControlJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.DeviceName == "" || len(req.DeviceName) > 120 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_device_name", "deviceName is required and must not exceed 120 characters")
		return
	}
	scopes, err := h.deviceEnrollmentScopesForPlatform(req.Platform, req.RequestedScopes)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	for _, scope := range scopes {
		if scope == ScopeAdmin {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scope", "device enrollments cannot request admin")
			return
		}
	}
	if err := validateP256PublicKey(req.PublicKey); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_public_key", err.Error())
		return
	}
	code, err := randomEnrollmentCode()
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_failed", "failed to create enrollment")
		return
	}
	verifier, err := randomSecret(32)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_failed", "failed to create enrollment")
		return
	}
	now := time.Now().UTC()
	enrollment := &store.AccessEnrollment{
		ID: uuid.NewString(), CodeHash: hashSecret(normalizeCode(code)), VerifierHash: hashSecret(verifier),
		DeviceName: req.DeviceName, Platform: bounded(req.Platform, 80), Model: bounded(req.Model, 120),
		AppVersion: bounded(req.AppVersion, 80), PublicKey: req.PublicKey, RequestedScopes: scopes, Status: "pending",
		SourceHash: enrollmentRequestSourceHash(h.cfg.APIToken, r), CreatedAt: now, ExpiresAt: now.Add(enrollmentTTL),
	}
	identities := h.deviceIdentityStore()
	if identities == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "enrollment_store_unavailable", "device enrollment is unavailable")
		return
	}
	if err := identities.CreateAccessEnrollment(r.Context(), enrollment); err != nil {
		if err == store.ErrRateLimited {
			w.Header().Set("Retry-After", "600")
			WriteControlProblem(w, r, http.StatusTooManyRequests, "enrollment_rate_limited", "too many recent enrollment requests")
			return
		}
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "enrollment_store_unavailable", "device enrollment is unavailable")
		return
	}
	preventSensitiveResponseCaching(w)
	verificationPath, pollPath := "/api/v1/enrollments/approve", "/api/v1/enrollments/"+enrollment.ID+"/exchange"
	if h.offlineEnrollmentOnly {
		verificationPath, pollPath = "/api/v1/fleet/offline/enrollments/approve", "/api/v1/fleet/offline/enrollments/"+enrollment.ID+"/exchange"
	}
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id": enrollment.ID, "userCode": code, "verifier": verifier,
		"expiresAt": enrollment.ExpiresAt, "verificationPath": verificationPath,
		"pollPath": pollPath,
	})
}

func (h *Handler) ListDeviceEnrollments(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	enrollments, err := h.deviceIdentityStore().ListAccessEnrollments(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_list_failed", "failed to list enrollments")
		return
	}
	if h.offlineEnrollmentOnly {
		offlineOnly := make([]store.AccessEnrollment, 0, len(enrollments))
		for _, enrollment := range enrollments {
			if strings.EqualFold(strings.TrimSpace(enrollment.Platform), FleetOfflineRunnerPlatform) {
				offlineOnly = append(offlineOnly, enrollment)
			}
		}
		enrollments = offlineOnly
	}
	if h.executorEnrollOnly {
		executorOnly := make([]store.AccessEnrollment, 0, len(enrollments))
		for _, enrollment := range enrollments {
			if isFleetExecutorPlatform(enrollment.Platform) {
				executorOnly = append(executorOnly, enrollment)
			}
		}
		enrollments = executorOnly
	}
	writeJSON(w, map[string]interface{}{"enrollments": enrollments})
}

func (h *Handler) ApproveDeviceEnrollment(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedPairingTransport(w, r) {
		return
	}
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	var req struct {
		UserCode string   `json:"userCode"`
		Scopes   []string `json:"scopes"`
	}
	if decodeControlJSON(w, r, &req) != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid approval request")
		return
	}
	enrollment, err := h.deviceIdentityStore().GetAccessEnrollmentByCodeHash(r.Context(), hashSecret(normalizeCode(req.UserCode)))
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrIdentityNotFound) || errors.Is(err, etcdstore.ErrNotFound) {
		WriteControlProblem(w, r, http.StatusNotFound, "enrollment_not_found", "enrollment code was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_lookup_failed", "failed to load enrollment")
		return
	}
	if enrollment.Status != "pending" || time.Now().After(enrollment.ExpiresAt) {
		WriteControlProblem(w, r, http.StatusConflict, "enrollment_not_pending", "enrollment is expired or no longer pending")
		return
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = enrollment.RequestedScopes
	}
	scopes, err = h.deviceEnrollmentScopesForPlatform(enrollment.Platform, scopes)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	for _, scope := range scopes {
		if scope == ScopeAdmin {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scope", "device enrollments cannot grant admin")
			return
		}
		if !containsScope(enrollment.RequestedScopes, scope) {
			WriteControlProblem(w, r, http.StatusBadRequest, "scope_not_requested", "approval scopes must be a subset of requestedScopes")
			return
		}
	}
	deviceID := uuid.NewString()
	device := &store.AccessDevice{ID: deviceID, Name: enrollment.DeviceName, Platform: enrollment.Platform, Model: enrollment.Model, AppVersion: enrollment.AppVersion, PublicKey: enrollment.PublicKey, CreatedAt: time.Now().UTC()}
	approved, err := h.deviceIdentityStore().ApproveAccessEnrollmentWithDevice(r.Context(), enrollment.ID, device, scopes)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "enrollment_not_pending", "enrollment could not be approved")
		return
	}
	writeJSON(w, approved)
}

func (h *Handler) ExchangeDeviceEnrollment(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedPairingTransport(w, r) {
		return
	}
	var req struct {
		Verifier string `json:"verifier"`
	}
	if decodeControlJSON(w, r, &req) != nil || req.Verifier == "" || len(req.Verifier) > 128 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "verifier is required")
		return
	}
	id := chi.URLParam(r, "id")
	enrollment, err := h.deviceIdentityStore().GetAccessEnrollment(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrIdentityNotFound) || errors.Is(err, etcdstore.ErrNotFound) {
		WriteControlProblem(w, r, http.StatusNotFound, "enrollment_not_found", "enrollment was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_lookup_failed", "failed to load enrollment")
		return
	}
	provided, expected := []byte(hashSecret(req.Verifier)), []byte(enrollment.VerifierHash)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
		failureErr := h.deviceIdentityStore().RecordAccessEnrollmentFailure(r.Context(), id)
		if failureErr != nil && failureErr != store.ErrEnrollmentLocked && !errors.Is(failureErr, pgx.ErrNoRows) && !errors.Is(failureErr, etcdstore.ErrNotFound) {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "enrollment_store_unavailable", "device enrollment is unavailable")
			return
		}
		WriteControlProblem(w, r, http.StatusUnauthorized, "invalid_enrollment_verifier", "enrollment verifier is invalid")
		return
	}
	if enrollment.Status != "approved" || time.Now().After(enrollment.ExpiresAt) {
		WriteControlProblem(w, r, http.StatusConflict, "enrollment_not_approved", "enrollment is not approved or has expired")
		return
	}
	scopes, err := h.deviceEnrollmentScopesForPlatform(enrollment.Platform, enrollment.ApprovedScopes)
	if err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "invalid_scope", "approved device scopes are unavailable on this authority")
		return
	}
	subject := enrollment.DeviceName
	if h.offlineEnrollmentOnly {
		subject = fleetOfflineDeviceSubjectPrefix + enrollment.DeviceID
	}
	token, record, err := issueDeviceTokenForPlatform(h.cfg, enrollment.Platform, enrollment.DeviceID, subject, scopes, "", deviceTokenLifetimeForPlatform(enrollment.Platform))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to issue device token")
		return
	}
	if err := h.deviceIdentityStore().ExchangeAccessEnrollmentWithToken(r.Context(), id, record); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "enrollment_already_exchanged", "enrollment has already been exchanged")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"token": token, "tokenId": record.JTI, "deviceId": record.DeviceID, "scopes": record.Scopes, "expiresAt": record.ExpiresAt})
}

func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	devices, err := h.deviceIdentityStore().ListAccessDevices(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "device_list_failed", "failed to list devices")
		return
	}
	if h.executorEnrollOnly {
		executorOnly := make([]store.AccessDevice, 0, len(devices))
		for _, device := range devices {
			if isFleetExecutorPlatform(device.Platform) {
				executorOnly = append(executorOnly, device)
			}
		}
		devices = executorOnly
	}
	writeJSON(w, map[string]interface{}{"devices": devices})
}

func (h *Handler) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	if h.offlineEnrollmentOnly {
		device, err := h.deviceIdentityStore().ActiveAccessDevice(r.Context(), chi.URLParam(r, "id"))
		if err != nil || device == nil || !strings.EqualFold(strings.TrimSpace(device.Platform), FleetOfflineRunnerPlatform) {
			WriteControlProblem(w, r, http.StatusNotFound, "device_not_found", "offline runner device was not found")
			return
		}
	}
	if h.executorEnrollOnly {
		device, err := h.deviceIdentityStore().ActiveAccessDevice(r.Context(), chi.URLParam(r, "id"))
		if err != nil || device == nil || !isFleetExecutorPlatform(device.Platform) {
			WriteControlProblem(w, r, http.StatusNotFound, "device_not_found", "fleet executor device was not found")
			return
		}
	}
	sessions, err := h.deviceIdentityStore().RevokeAccessDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, etcdstore.ErrNotFound) {
			WriteControlProblem(w, r, http.StatusNotFound, "device_not_found", "device was not found")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "device_revoke_failed", "failed to revoke device")
		return
	}
	h.closeExecSessionConnections(sessions)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RotateCurrentToken(w http.ResponseWriter, r *http.Request) {
	RotateManagedToken(h.cfg, h.db, h.closeExecSessionConnections, w, r)
}

// RotateManagedToken serves the credential aggregate through its backend-neutral
// boundary. The returned session IDs were cancelled atomically with rotation;
// closeConnections only tears down their in-memory transports afterward.
func RotateManagedToken(cfg *config.Config, auth store.AuthStore, closeConnections func([]string), w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_token_required", "rotation requires a managed device token")
		return
	}
	// CI credentials must be refreshed through the verified OIDC exchange.
	if principal.CI != nil {
		WriteControlProblem(w, r, http.StatusConflict, "github_actions_rotation_unsupported", "GitHub Actions tokens must be refreshed through the OIDC exchange")
		return
	}
	// The attempt-bound credential (plan-5-7 E5) expires with its grant and is
	// never extended: it is refused here by its scope, not left to fail only
	// because it carries no device claim.
	for _, scope := range principal.Scopes {
		if scope == ScopeFleetAttempt {
			WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the attempt-bound credential cannot be rotated")
			return
		}
	}
	if principal.DeviceID == "" {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "only a device token can be rotated")
		return
	}
	device, err := auth.ActiveAccessDevice(r.Context(), principal.DeviceID)
	if err != nil || device == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "device_lookup_failed", "device token lifetime could not be determined")
		return
	}
	token, record, err := issueDeviceTokenForPlatform(cfg, device.Platform, principal.DeviceID, principal.Subject, principal.Scopes, principal.TokenID, deviceTokenLifetimeForPlatform(device.Platform))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to rotate token")
		return
	}
	sessions, err := auth.RotateAccessToken(r.Context(), principal.TokenID, record)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrIdentityNotFound) {
			WriteControlProblem(w, r, http.StatusConflict, "token_not_managed", "token is not active in the token registry")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_revoke_failed", "failed to retire previous token")
		return
	}
	if closeConnections != nil {
		closeConnections(sessions)
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"token": token, "tokenId": record.JTI, "deviceId": record.DeviceID, "scopes": record.Scopes, "expiresAt": record.ExpiresAt})
}

func (h *Handler) RevokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	RevokeManagedToken(h.db, h.closeExecSessionConnections, w, r)
}

// RevokeManagedToken uses AuthStore so every backend atomically cancels the
// credential's exec sessions before the HTTP request can succeed.
func RevokeManagedToken(auth store.AuthStore, closeConnections func([]string), w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_token_required", "revocation requires a managed token")
		return
	}
	sessions, err := auth.RevokeAccessToken(r.Context(), principal.TokenID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrIdentityNotFound) {
			WriteControlProblem(w, r, http.StatusConflict, "token_not_managed", "token is not managed by the device registry")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_revoke_failed", "failed to revoke token")
		return
	}
	if closeConnections != nil {
		closeConnections(sessions)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) issueDeviceToken(deviceID, subject string, scopes []string, rotatedFrom string) (string, *store.AccessToken, error) {
	return h.issueDeviceTokenWithTTL(deviceID, subject, scopes, rotatedFrom, deviceTokenTTL)
}

func (h *Handler) issueDeviceTokenWithTTL(deviceID, subject string, scopes []string, rotatedFrom string, lifetime time.Duration) (string, *store.AccessToken, error) {
	return issueDeviceTokenWithTTL(h.cfg, deviceID, subject, scopes, rotatedFrom, lifetime)
}

func issueDeviceTokenWithTTL(cfg *config.Config, deviceID, subject string, scopes []string, rotatedFrom string, lifetime time.Duration) (string, *store.AccessToken, error) {
	return issueDeviceTokenForPlatform(cfg, "", deviceID, subject, scopes, rotatedFrom, lifetime)
}

// issueDeviceTokenForPlatform is issueDeviceTokenWithTTL with the device's
// persisted platform, which fleet:execute validation needs.
func issueDeviceTokenForPlatform(cfg *config.Config, platform, deviceID, subject string, scopes []string, rotatedFrom string, lifetime time.Duration) (string, *store.AccessToken, error) {
	if cfg != nil && cfg.IsFleetAuthorityOnly() || containsScope(scopes, ScopeFleetExecute) || containsScope(scopes, ScopeFleetOfflineAttempt) {
		var err error
		scopes, err = (&Handler{cfg: cfg}).deviceEnrollmentScopesForPlatform(platform, scopes)
		if err != nil {
			return "", nil, err
		}
	}
	if lifetime <= 0 {
		lifetime = deviceTokenTTL
	}
	now := time.Now().UTC()
	expires := now.Add(lifetime)
	jti := "norn_" + uuid.NewString()
	claims := tokenClaims{Sub: subject, Iat: now.Unix(), Exp: expires.Unix(), Jti: jti, Did: deviceID, Scopes: scopes}
	claims.Iss, claims.Aud, claims.Use, claims.Managed = "norn", "norn-control", "access", true
	token, err := signToken(cfg.APIToken, claims)
	return token, &store.AccessToken{JTI: jti, DeviceID: deviceID, Subject: subject, Scopes: scopes, IssuedAt: now, ExpiresAt: expires, RotatedFrom: rotatedFrom}, err
}

func deviceTokenLifetimeForPlatform(platform string) time.Duration {
	if strings.EqualFold(strings.TrimSpace(platform), "web") {
		return browserDeviceTokenTTL
	}
	if isFleetExecutorPlatform(platform) {
		return fleetExecutorTokenTTL
	}
	return deviceTokenTTL
}

// Rotation trusts the persisted device platform, not a client-provided claim.
func (h *Handler) rotatedDeviceTokenLifetime(ctx context.Context, deviceID string) (time.Duration, error) {
	device, err := h.db.ActiveAccessDevice(ctx, deviceID)
	if err != nil {
		return 0, err
	}
	return deviceTokenLifetimeForPlatform(device.Platform), nil
}

// Human devices on a Fleet authority can observe or request reviewed changes.
// Runner checkpoint authority is never issued through human enrollment.
func (h *Handler) deviceEnrollmentScopes(requested []string) ([]string, error) {
	if h.cfg == nil || !h.cfg.IsFleetAuthorityOnly() {
		return normalizeAccessTokenScopes(requested)
	}
	if len(requested) == 0 {
		return []string{ScopeAPIRead}, nil
	}
	scopes, err := normalizeAccessTokenScopes(requested)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if scope != ScopeAPIRead && scope != ScopeAPIWrite {
			return nil, fmt.Errorf("Fleet authority device enrollment permits only api:read and api:write")
		}
	}
	return scopes, nil
}

func randomEnrollmentCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	bytes := make([]byte, 8)
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		bytes[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(bytes[:4]) + "-" + string(bytes[4:]), nil
}

func randomSecret(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func normalizeCode(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	value = strings.ReplaceAll(value, "-", "")
	return strings.ToUpper(value)
}
func bounded(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func containsScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func enrollmentRequestSourceHash(secret string, r *http.Request) string {
	source := r.RemoteAddr
	host, _, err := net.SplitHostPort(source)
	if err == nil {
		source = host
	}
	direct := net.ParseIP(strings.TrimSpace(source))
	if direct != nil && direct.IsLoopback() {
		if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); forwarded != nil {
			source = forwarded.String()
		} else if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			if forwarded := net.ParseIP(strings.TrimSpace(strings.Split(forwardedFor, ",")[0])); forwarded != nil {
				source = forwarded.String()
			}
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("norn.enrollment-source/v1\x00" + source))
	return hex.EncodeToString(mac.Sum(nil))
}

func requireTrustedPairingTransport(w http.ResponseWriter, r *http.Request) bool {
	if pairingTransportAllowed(r) {
		return true
	}
	WriteControlProblem(w, r, http.StatusUpgradeRequired, "pairing_tls_required", "device pairing requires HTTPS outside loopback")
	return false
}

func pairingTransportAllowed(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	direct := r.RemoteAddr
	if host, _, err := net.SplitHostPort(direct); err == nil {
		direct = host
	}
	directIP := net.ParseIP(strings.TrimSpace(direct))
	if directIP == nil || !directIP.IsLoopback() {
		return false
	}
	forwarded := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" || strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != ""
	if !forwarded {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	var visitor struct {
		Scheme string `json:"scheme"`
	}
	return json.Unmarshal([]byte(r.Header.Get("CF-Visitor")), &visitor) == nil && strings.EqualFold(visitor.Scheme, "https")
}
