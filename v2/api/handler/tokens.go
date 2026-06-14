package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type tokenClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
	Jti string `json:"jti"`
}

func signToken(secret string, claims tokenClaims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := header + "." + enc
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig, nil
}

func verifyToken(secret, token string) (*tokenClaims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, fmt.Errorf("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding")
	}
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid claims")
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}

func (h *Handler) CreateAccessToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTL  string `json:"ttl"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TTL == "" {
		writeError(w, http.StatusBadRequest, "ttl is required")
		return
	}
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil || ttl <= 0 {
		writeError(w, http.StatusBadRequest, "invalid ttl duration")
		return
	}
	if ttl > 72*time.Hour {
		writeError(w, http.StatusBadRequest, "ttl must not exceed 72h")
		return
	}
	now := time.Now().UTC()
	claims := tokenClaims{
		Sub: req.Note,
		Iat: now.Unix(),
		Exp: now.Add(ttl).Unix(),
		Jti: fmt.Sprintf("norn_%d", now.UnixNano()),
	}
	token, err := signToken(h.cfg.APIToken, claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create token")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"token":     token,
		"expiresAt": now.Add(ttl).Format(time.RFC3339),
		"note":      req.Note,
	})
}

func (h *Handler) VerifyAccessToken(token string) bool {
	if h.cfg.APIToken == "" {
		return false
	}
	_, err := verifyToken(h.cfg.APIToken, token)
	return err == nil
}
