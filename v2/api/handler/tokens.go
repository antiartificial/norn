package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/store"
)

type tokenClaims struct {
	Sub         string   `json:"sub"`
	Iss         string   `json:"iss,omitempty"`
	Aud         string   `json:"aud,omitempty"`
	Use         string   `json:"use,omitempty"`
	Managed     bool     `json:"managed,omitempty"`
	Exp         int64    `json:"exp"`
	Iat         int64    `json:"iat"`
	Jti         string   `json:"jti"`
	Did         string   `json:"did,omitempty"`
	Purpose     string   `json:"purpose,omitempty"`
	Resource    string   `json:"resource,omitempty"`
	ChallengeID string   `json:"challengeId,omitempty"`
	Scopes      []string `json:"scp,omitempty"`
}

const (
	ScopeAPIRead         = "api:read"
	ScopeAPIWrite        = "api:write"
	ScopeEventsRead      = "events:read"
	ScopeAppsExec        = "apps:exec"
	ScopePlatformOperate = "platform:operate"
	ScopeHostOperate     = "host:operate"
	ScopeFleetOperate    = "fleet:operate"
	ScopeAdmin           = "admin"
)

var accessTokenScopes = map[string]struct{}{
	ScopeAPIRead: {}, ScopeAPIWrite: {}, ScopeEventsRead: {}, ScopeAppsExec: {},
	ScopePlatformOperate: {}, ScopeHostOperate: {}, ScopeAdmin: {},
	ScopeFleetOperate: {},
}

func AccessTokenScopeNames() []string {
	return []string{ScopeAPIRead, ScopeAPIWrite, ScopeEventsRead, ScopeAppsExec, ScopePlatformOperate, ScopeHostOperate, ScopeFleetOperate, ScopeAdmin}
}

type AccessPrincipal struct {
	Subject   string    `json:"subject,omitempty"`
	TokenID   string    `json:"tokenId,omitempty"`
	DeviceID  string    `json:"deviceId,omitempty"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Legacy    bool      `json:"legacy,omitempty"`
}

type accessPrincipalContextKey struct{}

func WithAccessPrincipal(r *http.Request, principal *AccessPrincipal) *http.Request {
	if principal == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), accessPrincipalContextKey{}, *principal))
}

func AccessPrincipalFromRequest(r *http.Request) (AccessPrincipal, bool) {
	principal, ok := r.Context().Value(accessPrincipalContextKey{}).(AccessPrincipal)
	return principal, ok
}

func (p AccessPrincipal) Allows(scope string) bool {
	if scope == "" || p.Legacy {
		return true
	}
	for _, candidate := range p.Scopes {
		if candidate == ScopeAdmin || candidate == scope {
			return true
		}
	}
	return false
}

func normalizeAccessTokenScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{ScopeAPIRead, ScopeEventsRead}, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if _, ok := accessTokenScopes[scope]; !ok {
			return nil, fmt.Errorf("unsupported scope %q", scope)
		}
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out, nil
}

func signToken(secret string, claims tokenClaims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := header + "." + enc
	mac := hmac.New(sha256.New, tokenSigningKey(secret))
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig, nil
}

func tokenSigningKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("norn.jwt-signing/v1"))
	return mac.Sum(nil)
}

func verifyToken(secret, token string, legacySigningUntil time.Time) (*tokenClaims, error) {
	if len(token) == 0 || len(token) > 8192 {
		return nil, fmt.Errorf("malformed token")
	}
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token")
	}
	validSignature := func(key []byte) bool {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
		expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(expected), []byte(parts[2]))
	}
	if !validSignature(tokenSigningKey(secret)) {
		if legacySigningUntil.IsZero() || !time.Now().Before(legacySigningUntil) || !validSignature([]byte(secret)) {
			return nil, fmt.Errorf("invalid signature")
		}
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid header encoding")
	}
	var headerFields struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if json.Unmarshal(header, &headerFields) != nil || headerFields.Algorithm != "HS256" || headerFields.Type != "JWT" {
		return nil, fmt.Errorf("invalid token header")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding")
	}
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid claims")
	}
	now := time.Now().Unix()
	if claims.Exp <= 0 || now >= claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	if claims.Iat > now+300 {
		return nil, fmt.Errorf("token issued in the future")
	}
	return &claims, nil
}

func (h *Handler) CreateAccessToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "token_registry_unavailable", "managed token issuance is unavailable")
		return
	}
	var req struct {
		TTL    string   `json:"ttl"`
		Note   string   `json:"note"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeControlJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if req.TTL == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_token_ttl", "ttl is required")
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Note) > 120 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_token_note", "note must not exceed 120 characters")
		return
	}
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil || ttl <= 0 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_token_ttl", "invalid ttl duration")
		return
	}
	if ttl > 72*time.Hour {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_token_ttl", "ttl must not exceed 72h")
		return
	}
	scopes, err := normalizeAccessTokenScopes(req.Scopes)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	now := time.Now().UTC()
	claims := tokenClaims{
		Sub:     req.Note,
		Iss:     "norn",
		Aud:     "norn-control",
		Use:     "access",
		Managed: true,
		Iat:     now.Unix(),
		Exp:     now.Add(ttl).Unix(),
		Jti:     "norn_" + uuid.NewString(),
		Scopes:  scopes,
	}
	token, err := signToken(h.cfg.APIToken, claims)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to create token")
		return
	}
	if err := h.db.RecordAccessToken(r.Context(), &store.AccessToken{
		JTI: claims.Jti, Subject: claims.Sub, Scopes: scopes,
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to record token")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"token":     token,
		"expiresAt": now.Add(ttl).Format(time.RFC3339),
		"note":      req.Note,
		"scopes":    scopes,
	})
}

func (h *Handler) VerifyAccessToken(token string) (*AccessPrincipal, bool) {
	if h.cfg.APIToken == "" {
		return nil, false
	}
	claims, err := verifyToken(h.cfg.APIToken, token, h.cfg.LegacyTokenSigningUntil)
	if err != nil {
		return nil, false
	}
	modern := claims.Managed || claims.Iss != "" || claims.Aud != "" || claims.Use != ""
	if modern && (claims.Iss != "norn" || claims.Aud != "norn-control" || claims.Use != "access" || !claims.Managed) {
		return nil, false
	}
	if claims.Managed && h.db == nil {
		return nil, false
	}
	if h.db != nil && claims.Jti != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		active, lookupErr := h.db.AccessTokenActive(ctx, claims.Jti)
		if lookupErr == nil && !active {
			return nil, false
		}
		if claims.Managed && lookupErr == pgx.ErrNoRows {
			return nil, false
		}
		if lookupErr != nil && lookupErr != pgx.ErrNoRows {
			return nil, false
		}
		if lookupErr == nil && claims.Did != "" {
			_ = h.db.TouchAccessDevice(ctx, claims.Did)
		}
	}
	return &AccessPrincipal{
		Subject: claims.Sub, TokenID: claims.Jti, DeviceID: claims.Did, Scopes: claims.Scopes,
		ExpiresAt: time.Unix(claims.Exp, 0).UTC(), Legacy: !modern && len(claims.Scopes) == 0,
	}, true
}
