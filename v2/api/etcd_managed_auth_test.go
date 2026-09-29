package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type activeManagedIdentity struct{ store.IdentityStore }

func (activeManagedIdentity) AccessTokenActive(context.Context, string) (bool, error) {
	return true, nil
}

func TestEtcdManagedTokenAuthAllowsSelfRetirementRegardlessOfScope(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	record, err := handler.NewManagedAccessTokenRecord("runner", []string{handler.ScopeFleetOperate}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := handler.SignManagedAccessToken(secret, record)
	if err != nil {
		t.Fatal(err)
	}
	identities := activeManagedIdentity{}
	served := false
	endpoint := etcdManagedTokenAuth(&config.Config{APIToken: secret}, identities)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		if principal, ok := handler.AccessPrincipalFromRequest(r); !ok || principal.TokenID != record.JTI {
			t.Errorf("managed principal was not carried to the handler")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	endpoint.ServeHTTP(rec, req)
	if !served || rec.Code != http.StatusNoContent {
		t.Fatalf("self retirement was rejected: served=%t status=%d", served, rec.Code)
	}
}
