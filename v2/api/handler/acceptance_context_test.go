package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"norn/v2/api/auth"
	"norn/v2/api/config"
	"norn/v2/api/store"
)

type staticTokenLineage map[string]string

func (s staticTokenLineage) RootAccessToken(ctx context.Context, current string) (string, error) {
	return resolveRootAccessToken(ctx, current, func(_ context.Context, tokenID string) (string, error) {
		parent, ok := s[tokenID]
		if !ok {
			return "", store.ErrIdentityNotFound
		}
		return parent, nil
	})
}

func requestWithPrincipal(principal AccessPrincipal) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/platform/upgrades", nil)
	return WithAccessPrincipal(req, &principal)
}

func TestVerifiedOperationActorKeepsDuplicateDeviceLabelsDistinct(t *testing.T) {
	h := &Handler{}
	principal := AccessPrincipal{Subject: "MacBook Pro", TokenID: "token-a", DeviceID: "device-a", Source: AccessPrincipalSourceManagedToken}
	a, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	principal.TokenID = "token-b"
	principal.DeviceID = "device-b"
	b, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Subject == b.Subject || a.Issuer != b.Issuer {
		t.Fatalf("duplicate display labels merged: a=%+v b=%+v", a, b)
	}
}

func TestVerifiedOperationActorSurvivesManagedTokenRotation(t *testing.T) {
	h := &Handler{accessTokenLineage: staticTokenLineage{
		"token-root": "", "token-2": "token-root", "token-3": "token-2",
	}}
	principal := AccessPrincipal{Subject: "same display note", TokenID: "token-2", Source: AccessPrincipalSourceManagedToken}
	a, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	principal.TokenID = "token-3"
	b, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Issuer != b.Issuer || a.Subject != b.Subject || a.Subject != "token-root" {
		t.Fatalf("rotated credentials changed actor: a=%+v b=%+v", a, b)
	}
	if a.CredentialID == b.CredentialID {
		t.Fatal("credential evidence should retain the token used for each request")
	}
}

func TestRootAccessTokenRejectsBrokenAndCyclicLineage(t *testing.T) {
	for name, lineage := range map[string]staticTokenLineage{
		"missing parent": {"token-2": "missing"},
		"cycle":          {"token-2": "token-1", "token-1": "token-2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := lineage.RootAccessToken(context.Background(), "token-2")
			if !errors.Is(err, errOperationActorUnverified) {
				t.Fatalf("error=%v, want stable-actor rejection", err)
			}
		})
	}
}

func TestRootAccessTokenRejectsExcessiveDepth(t *testing.T) {
	lineage := staticTokenLineage{}
	for i := 0; i < maxAccessTokenLineageDepth; i++ {
		lineage[fmt.Sprintf("token-%d", i)] = fmt.Sprintf("token-%d", i+1)
	}
	lineage[fmt.Sprintf("token-%d", maxAccessTokenLineageDepth)] = ""
	if _, err := lineage.RootAccessToken(context.Background(), "token-0"); !errors.Is(err, errOperationActorUnverified) {
		t.Fatalf("deep lineage error=%v, want stable-actor rejection", err)
	}
}

func TestVerifiedOperationActorSeparatesMatchingDisplayAcrossIssuers(t *testing.T) {
	h := &Handler{cfg: &config.Config{CFAccessTeamDomain: "team.cloudflareaccess.com"}, accessTokenLineage: staticTokenLineage{"same-subject": ""}}
	managed := AccessPrincipal{Subject: "matching label", TokenID: "same-subject", Source: AccessPrincipalSourceManagedToken}
	a, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(managed), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	cloudflare := AccessPrincipal{Subject: "matching label", Source: AccessPrincipalSourceCloudflareAccess}
	req := requestWithPrincipal(cloudflare)
	req = auth.WithCFAccessClaims(req, &auth.CFAccessClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "same-subject"}})
	b, err := h.resolveVerifiedOperationActor(context.Background(), req, "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Subject != b.Subject || a.Issuer == b.Issuer {
		t.Fatalf("issuer separation failed: managed=%+v cloudflare=%+v", a, b)
	}
}

func TestVerifiedOperationActorGitHubRefreshAndAttemptBoundary(t *testing.T) {
	h := &Handler{}
	ci := &CIIdentity{Provider: "github-actions", RepositoryOwnerID: "owner-1", RepositoryID: "repo-1", RunID: "run-1", RunAttempt: "1"}
	principal := AccessPrincipal{TokenID: "norn-refresh-1", Source: AccessPrincipalSourceManagedToken, CI: ci}
	a, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	principal.TokenID = "norn-refresh-2"
	b, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Issuer != b.Issuer || a.Subject != b.Subject {
		t.Fatalf("same workflow attempt changed actor: a=%+v b=%+v", a, b)
	}
	changed := *ci
	changed.RunAttempt = "2"
	principal.CI = &changed
	c, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(principal), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject == a.Subject {
		t.Fatalf("new workflow attempt reused actor: a=%+v c=%+v", a, c)
	}
}

func TestVerifiedOperationActorModelsSharedCredentialAndRejectsUnmanagedJWT(t *testing.T) {
	h := &Handler{}
	sharedA := AccessPrincipal{Subject: "alice", Legacy: true, Source: AccessPrincipalSourceSharedAPI}
	sharedB := AccessPrincipal{Subject: "bob", Legacy: true, Source: AccessPrincipalSourceSharedAPI}
	a, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(sharedA), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(sharedB), "authority-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Issuer != b.Issuer || a.Subject != b.Subject || a.Subject == "alice" || a.Subject == "bob" {
		t.Fatalf("shared credential invented individual identities: a=%+v b=%+v", a, b)
	}
	unmanaged := AccessPrincipal{Subject: "alice", TokenID: "legacy-jti", Legacy: true, Source: AccessPrincipalSourceUnmanagedLegacy}
	if _, err := h.resolveVerifiedOperationActor(context.Background(), requestWithPrincipal(unmanaged), "authority-1"); !errors.Is(err, errOperationActorUnverified) {
		t.Fatalf("unmanaged legacy error=%v, want fail-closed actor error", err)
	}
}

func TestOperationAcceptanceRequestContextIsPrivateAndCarriesReceipt(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/platform/upgrades", nil)
	want := operationAcceptanceRequestContext{ReceiptID: "receipt-1", RequestID: "request-1", Actor: verifiedOperationActor{Issuer: "issuer", Subject: "subject"}}
	req = withOperationAcceptanceRequestContext(req, want)
	got, ok := operationAcceptanceRequestContextFromRequest(req)
	if !ok || got.ReceiptID != want.ReceiptID || got.RequestID != want.RequestID || got.Actor.Issuer != want.Actor.Issuer || got.Actor.Subject != want.Actor.Subject {
		t.Fatalf("context=(%+v,%v), want %+v", got, ok, want)
	}
}
