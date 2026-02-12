package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mustGenKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func jwksJSON(t *testing.T, kid string, pub *rsa.PublicKey) []byte {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	resp := jwksResponse{
		Keys: []jwksKey{{
			Kid: kid,
			Kty: "RSA",
			Alg: "RS256",
			N:   n,
			E:   e,
		}},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid, aud string, exp time.Time) string {
	t.Helper()
	claims := &CFAccessClaims{
		Email: "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{aud},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	s, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func setupValidator(t *testing.T, key *rsa.PrivateKey, kid, aud string) *CFAccessValidator {
	t.Helper()
	data := jwksJSON(t, kid, &key.PublicKey)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(srv.Close)

	v := NewCFAccessValidator("test.cloudflareaccess.com", aud)
	v.keys.certsURL = srv.URL
	v.keys.httpGet = srv.Client().Get
	return v
}

func TestValidateValidToken(t *testing.T) {
	key := mustGenKey(t)
	aud := "test-aud-tag"
	kid := "key-1"

	v := setupValidator(t, key, kid, aud)
	token := signToken(t, key, kid, aud, time.Now().Add(time.Hour))

	claims, err := v.Validate(token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", claims.Email)
	}
}

func TestValidateExpiredToken(t *testing.T) {
	key := mustGenKey(t)
	aud := "test-aud-tag"
	kid := "key-1"

	v := setupValidator(t, key, kid, aud)
	token := signToken(t, key, kid, aud, time.Now().Add(-time.Hour))

	_, err := v.Validate(token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestValidateWrongAudience(t *testing.T) {
	key := mustGenKey(t)
	kid := "key-1"

	v := setupValidator(t, key, kid, "correct-aud")
	token := signToken(t, key, kid, "wrong-aud", time.Now().Add(time.Hour))

	_, err := v.Validate(token)
	if err == nil {
		t.Fatal("expected error for wrong audience")
	}
}

func TestMiddlewarePassesValidToken(t *testing.T) {
	key := mustGenKey(t)
	aud := "test-aud"
	kid := "key-1"

	v := setupValidator(t, key, kid, aud)
	token := signToken(t, key, kid, aud, time.Now().Add(time.Hour))

	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestMiddlewareRejectsInvalidToken(t *testing.T) {
	key := mustGenKey(t)
	aud := "test-aud"
	kid := "key-1"

	v := setupValidator(t, key, kid, aud)

	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "garbage-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestMiddlewarePassesThroughWithoutHeader(t *testing.T) {
	key := mustGenKey(t)
	aud := "test-aud"
	kid := "key-1"

	v := setupValidator(t, key, kid, aud)

	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/apps", nil)
	// No Cf-Access-Jwt-Assertion header
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (pass-through)", rr.Code)
	}
}
