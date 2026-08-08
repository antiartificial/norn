package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/store"
)

const enrollmentTTL = 10 * time.Minute
const deviceTokenTTL = 30 * 24 * time.Hour

type enrollmentStartRequest struct {
	DeviceName      string   `json:"deviceName"`
	Platform        string   `json:"platform"`
	Model           string   `json:"model"`
	AppVersion      string   `json:"appVersion"`
	PublicKey       string   `json:"publicKey"`
	RequestedScopes []string `json:"requestedScopes"`
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
	if req.DeviceName == "" || len(req.DeviceName) > 120 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_device_name", "deviceName is required and must not exceed 120 characters")
		return
	}
	scopes, err := normalizeAccessTokenScopes(req.RequestedScopes)
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
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "enrollment_store_unavailable", "device enrollment is unavailable")
		return
	}
	if err := h.db.CreateAccessEnrollment(r.Context(), enrollment); err != nil {
		if err == store.ErrRateLimited {
			w.Header().Set("Retry-After", "600")
			WriteControlProblem(w, r, http.StatusTooManyRequests, "enrollment_rate_limited", "too many recent enrollment requests")
			return
		}
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "enrollment_store_unavailable", "device enrollment is unavailable")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id": enrollment.ID, "userCode": code, "verifier": verifier,
		"expiresAt": enrollment.ExpiresAt, "verificationPath": "/api/v1/enrollments/approve",
		"pollPath": "/api/v1/enrollments/" + enrollment.ID + "/exchange",
	})
}

func (h *Handler) ListDeviceEnrollments(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	enrollments, err := h.db.ListAccessEnrollments(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_list_failed", "failed to list enrollments")
		return
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
	enrollment, err := h.db.GetAccessEnrollmentByCodeHash(r.Context(), hashSecret(normalizeCode(req.UserCode)))
	if err == pgx.ErrNoRows {
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
	scopes, err = normalizeAccessTokenScopes(scopes)
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
	approved, err := h.db.ApproveAccessEnrollmentWithDevice(r.Context(), enrollment.ID, device, scopes)
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
	enrollment, err := h.db.GetAccessEnrollment(r.Context(), id)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "enrollment_not_found", "enrollment was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "enrollment_lookup_failed", "failed to load enrollment")
		return
	}
	provided, expected := []byte(hashSecret(req.Verifier)), []byte(enrollment.VerifierHash)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
		failureErr := h.db.RecordAccessEnrollmentFailure(r.Context(), id)
		if failureErr != nil && failureErr != store.ErrEnrollmentLocked && failureErr != pgx.ErrNoRows {
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
	token, record, err := h.issueDeviceToken(enrollment.DeviceID, enrollment.DeviceName, enrollment.ApprovedScopes, "")
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to issue device token")
		return
	}
	if err := h.db.ExchangeAccessEnrollmentWithToken(r.Context(), id, record); err != nil {
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
	devices, err := h.db.ListAccessDevices(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "device_list_failed", "failed to list devices")
		return
	}
	writeJSON(w, map[string]interface{}{"devices": devices})
}

func (h *Handler) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	sessions, err := h.db.RevokeAccessDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == pgx.ErrNoRows {
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
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_token_required", "rotation requires a managed device token")
		return
	}
	token, record, err := h.issueDeviceToken(principal.DeviceID, principal.Subject, principal.Scopes, principal.TokenID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to rotate token")
		return
	}
	sessions, err := h.db.RotateAccessToken(r.Context(), principal.TokenID, record)
	if err != nil {
		if err == pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusConflict, "token_not_managed", "token is not active in the token registry")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_revoke_failed", "failed to retire previous token")
		return
	}
	h.closeExecSessionConnections(sessions)
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"token": token, "tokenId": record.JTI, "deviceId": record.DeviceID, "scopes": record.Scopes, "expiresAt": record.ExpiresAt})
}

func (h *Handler) RevokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_token_required", "revocation requires a managed token")
		return
	}
	sessions, err := h.db.RevokeAccessToken(r.Context(), principal.TokenID)
	if err != nil {
		if err == pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusConflict, "token_not_managed", "token is not managed by the device registry")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_revoke_failed", "failed to revoke token")
		return
	}
	h.closeExecSessionConnections(sessions)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) issueDeviceToken(deviceID, subject string, scopes []string, rotatedFrom string) (string, *store.AccessToken, error) {
	now := time.Now().UTC()
	expires := now.Add(deviceTokenTTL)
	jti := "norn_" + uuid.NewString()
	claims := tokenClaims{Sub: subject, Iat: now.Unix(), Exp: expires.Unix(), Jti: jti, Did: deviceID, Scopes: scopes}
	claims.Iss, claims.Aud, claims.Use, claims.Managed = "norn", "norn-control", "access", true
	token, err := signToken(h.cfg.APIToken, claims)
	return token, &store.AccessToken{JTI: jti, DeviceID: deviceID, Subject: subject, Scopes: scopes, IssuedAt: now, ExpiresAt: expires, RotatedFrom: rotatedFrom}, err
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
