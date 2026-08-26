package handler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/store"
)

const stepUpTTL = 2 * time.Minute

func validateP256PublicKey(encoded string) error {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	if len(encoded) > 128 {
		return fmt.Errorf("publicKey must be a P-256 X9.63 public key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(encoded)
	}
	if len(raw) != 65 {
		return fmt.Errorf("publicKey must be a P-256 X9.63 public key")
	}
	if err != nil {
		return fmt.Errorf("publicKey must be base64-encoded P-256 X9.63 data")
	}
	if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw); err != nil {
		return fmt.Errorf("publicKey must be a P-256 X9.63 public key")
	}
	return nil
}

func decodeP256PublicKey(encoded string) (*ecdsa.PublicKey, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, fmt.Errorf("device does not have a step-up public key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, err
	}
	publicKey, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("invalid P-256 public key")
	}
	return publicKey, nil
}

func stepUpPayload(id, nonce, purpose, resource string, expiresAt time.Time) string {
	return strings.Join([]string{
		"norn-step-up/v1", id, nonce, purpose, resource, expiresAt.UTC().Format(time.RFC3339Nano),
	}, "\n")
}

func (h *Handler) CreateStepUpChallenge(w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" || principal.DeviceID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_device_required", "step-up requires an enrolled device token")
		return
	}
	if !principal.Allows(ScopeAppsExec) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "apps:exec scope is required")
		return
	}
	var req struct {
		Purpose  string `json:"purpose"`
		Resource string `json:"resource"`
	}
	if decodeControlJSON(w, r, &req) != nil || req.Purpose != "exec" || !validAppIDRe.MatchString(req.Resource) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_step_up_request", "purpose must be exec and resource must be a valid app ID")
		return
	}
	device, err := h.db.ActiveAccessDevice(r.Context(), principal.DeviceID)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusUnauthorized, "device_not_active", "device is not active")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "device_lookup_failed", "failed to load device")
		return
	}
	if device.PublicKey == "" {
		WriteControlProblem(w, r, http.StatusPreconditionFailed, "step_up_key_required", "device must be enrolled with a P-256 public key")
		return
	}
	nonce, err := randomSecret(32)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "step_up_failed", "failed to create challenge")
		return
	}
	now := time.Now().UTC()
	challenge := &store.StepUpChallenge{
		ID: uuid.NewString(), DeviceID: principal.DeviceID, TokenJTI: principal.TokenID,
		Purpose: req.Purpose, Resource: req.Resource, NonceHash: hashSecret(nonce),
		Status: "pending", CreatedAt: now, ExpiresAt: now.Add(stepUpTTL),
	}
	if err := h.db.CreateStepUpChallenge(r.Context(), challenge); err != nil {
		if err == store.ErrRateLimited {
			w.Header().Set("Retry-After", "600")
			WriteControlProblem(w, r, http.StatusTooManyRequests, "step_up_rate_limited", "too many recent step-up challenges")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "step_up_failed", "failed to persist challenge")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"id": challenge.ID, "purpose": challenge.Purpose, "resource": challenge.Resource,
		"nonce": nonce, "signaturePayload": stepUpPayload(challenge.ID, nonce, challenge.Purpose, challenge.Resource, challenge.ExpiresAt),
		"expiresAt": challenge.ExpiresAt,
	})
}

func (h *Handler) VerifyStepUpChallenge(w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" || principal.DeviceID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_device_required", "step-up requires an enrolled device token")
		return
	}
	var req struct {
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if decodeControlJSON(w, r, &req) != nil || req.Nonce == "" || len(req.Nonce) > 128 || req.Signature == "" || len(req.Signature) > 256 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_step_up_proof", "nonce and signature are required")
		return
	}
	challenge, err := h.db.GetStepUpChallenge(r.Context(), chi.URLParam(r, "id"))
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "step_up_challenge_not_found", "challenge was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "step_up_lookup_failed", "failed to load challenge")
		return
	}
	if challenge.DeviceID != principal.DeviceID || challenge.TokenJTI != principal.TokenID || challenge.Status != "pending" || time.Now().After(challenge.ExpiresAt) {
		WriteControlProblem(w, r, http.StatusConflict, "step_up_challenge_inactive", "challenge is expired or no longer pending")
		return
	}
	providedHash, expectedHash := []byte(hashSecret(req.Nonce)), []byte(challenge.NonceHash)
	if len(providedHash) != len(expectedHash) || subtle.ConstantTimeCompare(providedHash, expectedHash) != 1 {
		WriteControlProblem(w, r, http.StatusUnauthorized, "invalid_step_up_proof", "challenge nonce is invalid")
		return
	}
	device, err := h.db.ActiveAccessDevice(r.Context(), principal.DeviceID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusUnauthorized, "device_not_active", "device is not active")
		return
	}
	publicKey, err := decodeP256PublicKey(device.PublicKey)
	if err != nil {
		WriteControlProblem(w, r, http.StatusPreconditionFailed, "step_up_key_required", err.Error())
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(req.Signature)
	if err != nil {
		signature, err = base64.StdEncoding.DecodeString(req.Signature)
	}
	payload := stepUpPayload(challenge.ID, req.Nonce, challenge.Purpose, challenge.Resource, challenge.ExpiresAt)
	digest := sha256.Sum256([]byte(payload))
	if err != nil || !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		WriteControlProblem(w, r, http.StatusUnauthorized, "invalid_step_up_proof", "signature verification failed")
		return
	}
	if err := h.db.VerifyStepUpChallenge(r.Context(), challenge.ID, principal.DeviceID); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "step_up_challenge_inactive", "challenge could not be verified")
		return
	}
	now := time.Now().UTC()
	expires := now.Add(stepUpTTL)
	if expires.After(challenge.ExpiresAt) {
		expires = challenge.ExpiresAt
	}
	claims := tokenClaims{
		Sub: principal.Subject, Did: principal.DeviceID, Jti: "stepup_" + uuid.NewString(),
		Iss: "norn", Aud: "norn-exec-step-up", Use: "step-up",
		Iat: now.Unix(), Exp: expires.Unix(), Purpose: "exec",
		Resource: challenge.Resource, ChallengeID: challenge.ID,
	}
	token, err := signToken(h.cfg.APIToken, claims)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "step_up_failed", "failed to issue step-up token")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{
		"stepUpToken": token, "purpose": claims.Purpose, "resource": claims.Resource,
		"challengeId": claims.ChallengeID, "expiresAt": expires,
	})
}

func (h *Handler) verifyExecStepUp(token string, principal AccessPrincipal, appID string) (*tokenClaims, error) {
	claims, err := verifyToken(h.cfg.APIToken, token, time.Time{})
	if err != nil {
		return nil, err
	}
	if claims.Iss != "norn" || claims.Aud != "norn-exec-step-up" || claims.Use != "step-up" || claims.Purpose != "exec" || claims.Resource != appID ||
		claims.Did == "" || claims.Did != principal.DeviceID || claims.ChallengeID == "" {
		return nil, fmt.Errorf("step-up token is not valid for this device and app")
	}
	return claims, nil
}
