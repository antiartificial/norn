package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

type lifecycleAuthStore struct {
	store.AuthStore
	rotatedFrom string
	issued      *store.AccessToken
	revoked     string
}

func (s *lifecycleAuthStore) RotateAccessToken(_ context.Context, previous string, token *store.AccessToken) ([]string, error) {
	s.rotatedFrom, s.issued = previous, token
	return []string{"exec-one"}, nil
}

func (s *lifecycleAuthStore) RevokeAccessToken(_ context.Context, jti string) ([]string, error) {
	s.revoked = jti
	return []string{"exec-two"}, nil
}

func TestManagedTokenLifecycleUsesAuthStore(t *testing.T) {
	cfg := &config.Config{APIToken: "0123456789abcdef0123456789abcdef"}
	auth := &lifecycleAuthStore{}
	principal := &AccessPrincipal{Subject: "operator", TokenID: "old-jti", DeviceID: "device-one", Scopes: []string{ScopeAPIRead}, Source: AccessPrincipalSourceManagedToken}
	var closed [][]string
	closeConnections := func(ids []string) { closed = append(closed, ids) }

	rotateReq := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), principal)
	rotateRec := httptest.NewRecorder()
	RotateManagedToken(cfg, auth, closeConnections, rotateRec, rotateReq)
	if rotateRec.Code != http.StatusOK || auth.rotatedFrom != "old-jti" || auth.issued == nil || auth.issued.RotatedFrom != "old-jti" {
		t.Fatalf("rotation did not use the auth aggregate: status=%d previous=%q token=%+v", rotateRec.Code, auth.rotatedFrom, auth.issued)
	}
	var rotated struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rotateRec.Body.Bytes(), &rotated); err != nil || rotated.Token == "" {
		t.Fatalf("rotation did not deliver a signed token: %v", err)
	}
	if len(closed) != 1 || len(closed[0]) != 1 || closed[0][0] != "exec-one" {
		t.Fatalf("rotation did not close cancelled session transports: %v", closed)
	}

	revokeReq := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/revoke", nil), principal)
	revokeRec := httptest.NewRecorder()
	RevokeManagedToken(auth, closeConnections, revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent || auth.revoked != "old-jti" || len(closed) != 2 || closed[1][0] != "exec-two" {
		t.Fatalf("revocation did not use the auth aggregate: status=%d revoked=%q closed=%v", revokeRec.Code, auth.revoked, closed)
	}
}

func TestManagedTokenRotationRejectsCIIdentity(t *testing.T) {
	principal := &AccessPrincipal{Subject: "runner", TokenID: "ci-jti", CI: &CIIdentity{Provider: "github"}, Source: AccessPrincipalSourceManagedToken}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), principal)
	rec := httptest.NewRecorder()
	RotateManagedToken(&config.Config{APIToken: "0123456789abcdef0123456789abcdef"}, &lifecycleAuthStore{}, nil, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("CI token rotation status = %d, want conflict", rec.Code)
	}
}
