package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/auth"
	"norn/v2/api/config"
	"norn/v2/api/handler"
)

func TestFileServerServesRootAndIndexFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>Norn</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	fileServer(r, dir)

	for _, path := range []string{"/", "/nested/route"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "<html>Norn</html>" {
			t.Fatalf("%s body = %q, want index", path, rec.Body.String())
		}
	}
}

func TestFileServerCannotEscapeUIRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	secretName := "outside-" + filepath.Base(dir)
	secretPath := filepath.Join(filepath.Dir(dir), secretName)
	if err := os.WriteFile(secretPath, []byte("must-not-be-served"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	r := chi.NewRouter()
	fileServer(r, dir)
	req := httptest.NewRequest(http.MethodGet, "/../"+secretName, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "must-not-be-served") {
		t.Fatal("file server exposed a path outside the configured UI root")
	}
}

func TestBearerAuthAllowsServiceManifestDiscovery(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil, false)(next)

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "manifest is public", path: "/api/services/manifest", wantStatus: http.StatusNoContent},
		{name: "control endpoint remains protected", path: "/api/apps", wantStatus: http.StatusUnauthorized},
		{name: "control endpoint accepts token", path: "/api/apps", token: "control-plane-token", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.RemoteAddr = "100.64.0.2:1234"
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			authenticated.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestControlPlaneTokenCarriesAdminPrincipal(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || !principal.Allows(handler.ScopeAdmin) || principal.Subject != "control-plane" {
			http.Error(w, "missing admin principal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	req.RemoteAddr = "100.64.0.2:1234"
	req.Header.Set("Authorization", "Bearer control-plane-token")
	rec := httptest.NewRecorder()
	bearerAuth("control-plane-token", nil, false)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLoopbackFallbackCannotApproveDevices(t *testing.T) {
	h := handler.New(nil, nil, nil, nil, &config.Config{APIToken: strings.Repeat("x", 32)}, nil, nil, nil, nil, nil, nil)
	protected := bearerAuth(strings.Repeat("x", 32), h, false)(http.HandlerFunc(h.ApproveDeviceEnrollment))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/approve", strings.NewReader(`{"userCode":"ABCD-EFGH"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("loopback admin status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBearerAuthOnlyTrustsDirectLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil, false)(next)

	for _, tt := range []struct {
		name       string
		remoteAddr string
		cfIP       string
		want       int
	}{
		{name: "direct loopback", remoteAddr: "127.0.0.1:1234", want: http.StatusNoContent},
		{name: "local proxy requires auth", remoteAddr: "127.0.0.1:1234", cfIP: "203.0.113.20", want: http.StatusUnauthorized},
		{name: "remote cannot spoof loopback", remoteAddr: "100.64.0.20:1234", cfIP: "127.0.0.1", want: http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.cfIP != "" {
				req.Header.Set("CF-Connecting-IP", tt.cfIP)
			}
			rec := httptest.NewRecorder()
			authenticated.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestBearerAuthCanRequireExplicitCredentialsOnLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil, true)(next)
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	authenticated.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestExplicitAuthProtectsInventoryAndMetrics(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil, true)(next)

	for _, path := range []string{"/api/services/manifest", "/metrics", "/api/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		authenticated.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusUnauthorized)
		}

		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer control-plane-token")
		rec = httptest.NewRecorder()
		authenticated.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("authenticated %s status = %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
}

func TestStrictAuthAcceptsValidatedCloudflarePrincipal(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Subject != "operator@example.test" || !principal.Allows(handler.ScopeAdmin) {
			http.Error(w, "missing Cloudflare principal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req = auth.WithCFAccessClaims(req, &auth.CFAccessClaims{Email: "operator@example.test"})
	rec := httptest.NewRecorder()

	bearerAuth("control-plane-token", nil, true)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestClientIPOnlyTrustsForwardingFromLoopback(t *testing.T) {
	proxied := httptest.NewRequest(http.MethodGet, "/", nil)
	proxied.RemoteAddr = "127.0.0.1:1234"
	proxied.Header.Set("CF-Connecting-IP", "203.0.113.20")
	if got := clientIPFromRequest(proxied); got != "203.0.113.20" {
		t.Fatalf("proxied client IP = %q", got)
	}

	remote := httptest.NewRequest(http.MethodGet, "/", nil)
	remote.RemoteAddr = "100.64.0.20:1234"
	remote.Header.Set("CF-Connecting-IP", "127.0.0.1")
	if got := clientIPFromRequest(remote); got != "100.64.0.20" {
		t.Fatalf("remote client IP = %q", got)
	}
}

func TestControlSecurityConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name    string
		config  *config.Config
		wantErr bool
	}{
		{name: "local development", config: &config.Config{BindAddr: "127.0.0.1"}},
		{name: "local development does not require managed workflow pinning", config: &config.Config{BindAddr: "127.0.0.1", GitHubActionsAllowedWorkflowRefs: []string{"owner/repo/.github/workflows/release.yml@refs/heads/main"}}},
		{name: "remote without auth", config: &config.Config{BindAddr: "0.0.0.0"}, wantErr: true},
		{name: "weak token", config: &config.Config{BindAddr: "127.0.0.1", APIToken: "short"}, wantErr: true},
		{name: "remote strong token", config: &config.Config{BindAddr: "0.0.0.0", APIToken: strings.Repeat("x", 32)}},
		{name: "partial Cloudflare Access", config: &config.Config{BindAddr: "127.0.0.1", CFAccessTeamDomain: "team.example.test"}, wantErr: true},
		{name: "strict auth without provider", config: &config.Config{BindAddr: "127.0.0.1", RequireExplicitAuth: true}, wantErr: true},
		{name: "strict auth with Cloudflare Access", config: &config.Config{BindAddr: "127.0.0.1", RequireExplicitAuth: true, CFAccessTeamDomain: "team.example.test", CFAccessAUD: "audience"}},
		{name: "unknown profile", config: &config.Config{Profile: "mystery", BindAddr: "127.0.0.1"}, wantErr: true},
		{name: "production lane requires production profile", config: &config.Config{Environment: "production", BindAddr: "127.0.0.1"}, wantErr: true},
		{name: "fleet GitHub bridge requires explicit environment", config: &config.Config{BindAddr: "127.0.0.1", FleetGitHubRepository: "acme/fleet"}, wantErr: true},
		{name: "fleet GitHub bridge cannot cross control plane lane", config: &config.Config{Environment: "staging", BindAddr: "127.0.0.1", FleetGitHubRepository: "acme/fleet", FleetGitHubEnvironment: "production"}, wantErr: true},
		{name: "production requires explicit auth", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn"}, wantErr: true},
		{name: "production rejects Nomad skip verify", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", NomadTLSSkipVerify: true, ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn", LegacyTokenSigningUntil: time.Now().Add(-time.Hour)}, wantErr: true},
		{name: "production rejects Consul skip verify", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", ConsulTLSSkipVerify: true, DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn", LegacyTokenSigningUntil: time.Now().Add(-time.Hour)}, wantErr: true},
		{name: "production rejects unverified database TLS", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=require", RegistryURL: "registry.example.test/norn", LegacyTokenSigningUntil: time.Now().Add(-time.Hour)}, wantErr: true},
		{name: "production rejects weak previous audit key", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), AuditSigningKey: strings.Repeat("a", 32), AuditPreviousSigningKeys: []string{"short"}, RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn", LegacyTokenSigningUntil: time.Now().Add(-time.Hour)}, wantErr: true},
		{name: "production rejects short audit retention", config: &config.Config{Profile: "production", BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), AuditSigningKey: strings.Repeat("a", 32), AuditRetentionDays: 30, RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn", LegacyTokenSigningUntil: time.Now().Add(-time.Hour)}, wantErr: true},
		{name: "production hardened", config: &config.Config{Profile: "production", Environment: "staging", EnvironmentExplicit: true, BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32), AuditSigningKey: strings.Repeat("a", 32), AuditRetentionDays: 365, RequireExplicitAuth: true, StrictSecrets: true, NomadAddr: "https://nomad:4646", ConsulAddr: "https://consul:8501", DatabaseURL: "postgres://db/norn?sslmode=verify-full", RegistryURL: "registry.example.test/norn", ArtifactSigningPublicKey: "/etc/norn/cosign.pub", ArtifactDenySeverities: []string{"HIGH", "CRITICAL"}, CosignPath: "/usr/bin/cosign", TrivyPath: "/usr/bin/trivy", LegacyTokenSigningUntil: time.Now().Add(-time.Hour), QualificationSigningKey: base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("q", 32))), GitHubActionsOIDCAudience: "norn", GitHubActionsReleaseBindings: []string{"demo=acme/demo@1@2"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + strings.Repeat("a", 40)}, GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsDefaultBranch: "main"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateControlSecurity(tt.config); (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNornSignedPrivateSignerAuthorityIsStagingOnly(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile, registryFile := filepath.Join(dir, "release.key"), filepath.Join(dir, "registry.json")
	if err := os.WriteFile(keyFile, []byte(base64.RawStdEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryFile, []byte(`{"auths":{"ghcr.io":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := func(environment string) *config.Config {
		return &config.Config{
			Environment: environment, EnvironmentExplicit: true, BindAddr: "127.0.0.1", APIToken: strings.Repeat("x", 32),
			ReleaseAdmissionMode: "attested", ReleaseAttestationTrustMode: "norn-signed-private", ReleaseAttestationIssuer: "https://token.actions.githubusercontent.com",
			ReleaseAttestationRepositories: []string{"personal-owner/private-app"}, ReleaseAttestationWorkflowRefs: []string{"personal-owner/norn/.github/workflows/norn-app-release.yml@" + strings.Repeat("a", 40)}, ReleaseRequireSBOM: true,
			ReleaseRegistryNodePullReady: true, ReleaseAttestationRegistryAuthFile: registryFile, ReleasePrivateTrustedSigningKeys: []string{base64.RawStdEncoding.EncodeToString(public)},
			GitHubActionsOIDCAudience: "norn-" + environment, GitHubActionsReleaseBindings: []string{"private-app=personal-owner/private-app@101@202"}, GitHubActionsAllowedWorkflowRefs: []string{"personal-owner/norn/.github/workflows/norn-app-release.yml@" + strings.Repeat("a", 40)}, GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedEnvironments: []string{environment}, GitHubActionsDefaultBranch: "main",
		}
	}
	staging := base("staging")
	staging.ReleasePrivateSigningBackend, staging.ReleasePrivateSigningKeyFile = "local", keyFile
	staging.QualificationSigningKey = base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'q'}, ed25519.SeedSize))
	if err := validateControlSecurity(staging); err != nil {
		t.Fatalf("valid staging private signer rejected: %v", err)
	}
	stagingWithPublicRotationKeys := *staging
	stagingWithPublicRotationKeys.TrustedQualificationSigningKeys = []string{releaseTestPublicKeyForMain('q')}
	if err := validateControlSecurity(&stagingWithPublicRotationKeys); err != nil {
		t.Fatalf("staging public qualification rotation keys rejected: %v", err)
	}
	stagingWithMutableWorkflow := *staging
	stagingWithMutableWorkflow.GitHubActionsAllowedWorkflowRefs = []string{"personal-owner/norn/.github/workflows/norn-app-release.yml@refs/heads/main"}
	if err := validateControlSecurity(&stagingWithMutableWorkflow); err == nil || !strings.Contains(err.Error(), "full lowercase commit SHA") {
		t.Fatalf("mutable release workflow startup error = %v", err)
	}

	production := base("production")
	production.Profile = "production"
	production.ReleasePrivateSigningBackend, production.ReleasePrivateSigningKeyFile = "local", keyFile
	if err := validateControlSecurity(production); err == nil || !strings.Contains(err.Error(), "must not be configured with signing authority") {
		t.Fatalf("production signing authority error = %v", err)
	}
	production.ReleasePrivateSigningBackend, production.ReleasePrivateSigningKeyFile = "", ""
	production.QualificationSigningKey = base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'q'}, ed25519.SeedSize))
	production.TrustedQualificationSigningKeys = []string{releaseTestPublicKeyForMain('q')}
	if err := validateControlSecurity(production); err == nil || !strings.Contains(err.Error(), "production must not be configured with the staging qualification signing key") {
		t.Fatalf("production qualification signing authority error = %v", err)
	}

	staging.ReleasePrivateTrustedSigningKeys = []string{base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'z'}, ed25519.PublicKeySize))}
	if err := validateControlSecurity(staging); err == nil || !strings.Contains(err.Error(), "signer key must be present") {
		t.Fatalf("untrusted staging signer error = %v", err)
	}
}

func TestImmutableGitHubWorkflowRef(t *testing.T) {
	for _, value := range []string{
		"acme/norn/.github/workflows/release.yml@" + strings.Repeat("a", 40),
		"acme/norn/.github/workflows/release.yaml@0123456789abcdef0123456789abcdef01234567",
	} {
		if !immutableGitHubWorkflowRef(value) {
			t.Fatalf("immutable workflow ref rejected: %q", value)
		}
	}
	for _, value := range []string{
		"acme/norn/.github/workflows/release.yml@refs/heads/main",
		"acme/norn/.github/workflows/*.yml@" + strings.Repeat("a", 40),
		"acme/norn/.github/workflows/release.yml@" + strings.Repeat("A", 40),
		"acme/norn/release.yml@" + strings.Repeat("a", 40),
		"acme/norn/.github/workflows/release.yml@short",
	} {
		if immutableGitHubWorkflowRef(value) {
			t.Fatalf("mutable or malformed workflow ref accepted: %q", value)
		}
	}
}

func releaseTestPublicKeyForMain(value byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize)).Public().(ed25519.PublicKey))
}

func TestProductionProfileRequiresExplicitSafeReleaseLane(t *testing.T) {
	for _, tt := range []struct {
		name        string
		environment string
		explicit    bool
		wantErr     bool
	}{
		{name: "omitted", wantErr: true},
		{name: "development", environment: "development", explicit: true, wantErr: true},
		{name: "staging", environment: "staging", explicit: true},
		{name: "production", environment: "production", explicit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Profile: "production", Environment: tt.environment, EnvironmentExplicit: tt.explicit}
			_, _, err := validateProfileEnvironment(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateProfileEnvironment() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	if _, _, err := validateProfileEnvironment(&config.Config{Profile: "development", Environment: "production", EnvironmentExplicit: true}); err == nil {
		t.Fatal("production environment was allowed under the development profile")
	}
}

func TestValidateAllowedOrigins(t *testing.T) {
	for _, origin := range []string{"*", "https://user:pass@example.test", "https://example.test/path", "javascript:alert(1)"} {
		if err := validateAllowedOrigins(origin, false); err == nil {
			t.Fatalf("expected %q to be rejected", origin)
		}
	}
	if err := validateAllowedOrigins("https://control.example.test,http://localhost:5173", true); err != nil {
		t.Fatalf("expected secure and loopback origins to validate: %v", err)
	}
	if err := validateAllowedOrigins("http://control.example.test", true); err == nil {
		t.Fatal("expected insecure production origin to be rejected")
	}
}

func TestSecureControlEndpointsRequireNetworkTargets(t *testing.T) {
	for _, endpoint := range []string{"https://nomad.example.test:4646", "https://[2001:db8::1]:4646"} {
		if !secureEndpoint(endpoint) {
			t.Fatalf("expected %q to be accepted", endpoint)
		}
	}
	for _, endpoint := range []string{"https:///missing-host", "https://user:pass@nomad.example.test", "http://nomad.example.test"} {
		if secureEndpoint(endpoint) {
			t.Fatalf("expected %q to be rejected", endpoint)
		}
	}
	if !secureDatabaseDSN("postgres://db.example.test/norn?sslmode=verify-full") {
		t.Fatal("expected verified PostgreSQL DSN to be accepted")
	}
	for _, dsn := range []string{"file:///tmp/norn?sslmode=verify-full", "postgres:///norn?sslmode=verify-full", "postgres://db.example.test/norn?sslmode=require"} {
		if secureDatabaseDSN(dsn) {
			t.Fatalf("expected %q to be rejected", dsn)
		}
	}
}

func TestExplicitAuthConfiguration(t *testing.T) {
	t.Setenv("NORN_REQUIRE_EXPLICIT_AUTH", "true")
	if !config.Load().RequireExplicitAuth {
		t.Fatal("NORN_REQUIRE_EXPLICIT_AUTH=true was not honored")
	}
}

func TestProductionProfileForcesStrictSecrets(t *testing.T) {
	t.Setenv("NORN_PROFILE", "production")
	cfg := config.Load()
	if !cfg.Production() || !cfg.StrictSecrets {
		t.Fatalf("production config = %+v, want production with strict secrets", cfg)
	}
}

func TestBearerAuthProtectsWebSocketsAndEnforcesScopes(t *testing.T) {
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/ws", nil)); got != handler.ScopeEventsRead {
		t.Fatalf("/ws scope = %q", got)
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/api/apps/demo/exec", nil)); got != handler.ScopeAppsExec {
		t.Fatalf("exec scope = %q", got)
	}
	if publicControlPath("/ws") {
		t.Fatal("/ws must not be public when API token auth is enabled")
	}
	if publicControlPath("/api/apps/demo/exec") {
		t.Fatal("exec must not be public when API token auth is enabled")
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/exec-sessions", nil)); got != handler.ScopeAppsExec {
		t.Fatalf("exec session scope = %q", got)
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodPost, "/api/v1/auth/step-up/challenges", nil)); got != handler.ScopeAppsExec {
		t.Fatalf("step-up scope = %q", got)
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/api/v1/host/metrics", nil)); got != handler.ScopeAPIRead {
		t.Fatalf("host metrics scope = %q", got)
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/api/v1/production/readiness", nil)); got != handler.ScopeAPIRead {
		t.Fatalf("production readiness scope = %q", got)
	}
}

func TestExplicitAuthWithCloudflareOnlyRejectsMissingCredentials(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	authenticated := bearerAuth("", nil, true)(next)

	for _, authorization := range []string{"", "Bearer "} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/production/readiness", nil)
		req.Header.Set("Authorization", authorization)
		rec := httptest.NewRecorder()
		authenticated.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/production/readiness", nil)
	req = auth.WithCFAccessClaims(req, &auth.CFAccessClaims{Email: "operator@example.test"})
	rec := httptest.NewRecorder()
	authenticated.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Cloudflare principal status = %d, want 204", rec.Code)
	}
}

func TestCapabilitiesRejectsInvalidSuppliedCredential(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || !principal.Allows(handler.ScopeAdmin) {
			t.Fatal("valid control credential did not attach its principal")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil, true)(next)

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	invalid.Header.Set("Authorization", "Bearer wrong-token")
	invalidResponse := httptest.NewRecorder()
	authenticated.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid capabilities credential status = %d, want 401", invalidResponse.Code)
	}

	valid := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	valid.Header.Set("Authorization", "Bearer control-plane-token")
	validResponse := httptest.NewRecorder()
	authenticated.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid capabilities credential status = %d, want 204", validResponse.Code)
	}
}

func TestControlCapabilitiesAdvertisesHostMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req = handler.WithAccessPrincipal(req, &handler.AccessPrincipal{Subject: "fleet-runner", Scopes: []string{handler.ScopeAPIRead, handler.ScopeFleetOperate}})
	writeControlCapabilities(rec, req)
	var capability struct {
		Features  []string          `json:"features"`
		Endpoints map[string]string `json:"endpoints"`
		Auth      struct {
			Principal struct {
				Authenticated bool     `json:"authenticated"`
				Scopes        []string `json:"scopes"`
			} `json:"principal"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capability); err != nil {
		t.Fatal(err)
	}
	if capability.Endpoints["hostMetrics"] != "/api/v1/host/metrics" {
		t.Fatalf("hostMetrics endpoint = %q", capability.Endpoints["hostMetrics"])
	}
	if capability.Endpoints["hostRuntime"] != "/api/v1/host/runtime" {
		t.Fatalf("hostRuntime endpoint = %q", capability.Endpoints["hostRuntime"])
	}
	if capability.Endpoints["productionReadiness"] != "/api/v1/production/readiness" {
		t.Fatalf("productionReadiness endpoint = %q", capability.Endpoints["productionReadiness"])
	}
	if capability.Endpoints["fleetNodePools"] != "/api/v1/fleet/node-pools" || capability.Endpoints["fleetValidation"] != "/api/v1/fleet/validate" {
		t.Fatalf("fleet endpoints = %v", capability.Endpoints)
	}
	if capability.Endpoints["fleetGitHub"] != "/api/v1/fleet/github" || capability.Endpoints["fleetGitHubDispatch"] == "" {
		t.Fatalf("fleet GitHub endpoints = %v", capability.Endpoints)
	}
	if capability.Endpoints["fleetRunnerAttempts"] == "" || capability.Endpoints["deployments"] == "" || capability.Endpoints["deploymentSteps"] == "" || capability.Endpoints["serviceManifest"] == "" {
		t.Fatalf("new versioned endpoints missing = %v", capability.Endpoints)
	}
	if !capability.Auth.Principal.Authenticated || len(capability.Auth.Principal.Scopes) != 2 || capability.Auth.Principal.Scopes[1] != handler.ScopeFleetOperate {
		t.Fatalf("principal scopes = %+v", capability.Auth.Principal)
	}
	if capability.Endpoints["mutationAudit"] != "/api/v1/audit/mutations" || capability.Endpoints["recoveryDrills"] != "/api/v1/production/drills" {
		t.Fatalf("production evidence endpoints = %v", capability.Endpoints)
	}
	if capability.Endpoints["appSnapshots"] == "" || capability.Endpoints["appMigrations"] == "" || capability.Endpoints["appRollbacks"] == "" {
		t.Fatalf("durable app recovery endpoints = %v", capability.Endpoints)
	}
	found := false
	productionFound := false
	auditFound := false
	drillsFound := false
	appRecoveryFound := false
	runnerFound := false
	deploymentsFound := false
	placementFound := false
	for _, feature := range capability.Features {
		if feature == "host-metrics" {
			found = true
		}
		if feature == "production-readiness" {
			productionFound = true
		}
		if feature == "durable-mutation-audit" {
			auditFound = true
		}
		if feature == "recovery-drill-receipts" {
			drillsFound = true
		}
		if feature == "durable-app-recovery-v1" {
			appRecoveryFound = true
		}
		if feature == "fleet-runner-attempts-v1" {
			runnerFound = true
		}
		if feature == "versioned-deployment-history-v1" {
			deploymentsFound = true
		}
		if feature == "service-instance-placement-v2" {
			placementFound = true
		}
	}
	if !found {
		t.Fatalf("host-metrics feature missing: %v", capability.Features)
	}
	if !productionFound {
		t.Fatalf("production-readiness feature missing: %v", capability.Features)
	}
	if !auditFound || !drillsFound {
		t.Fatalf("production evidence features missing: %v", capability.Features)
	}
	if !appRecoveryFound {
		t.Fatalf("durable app recovery feature missing: %v", capability.Features)
	}
	if !runnerFound || !deploymentsFound || !placementFound {
		t.Fatalf("durable fleet/deployment/placement features missing: %v", capability.Features)
	}
}

func TestReleaseCapabilitiesRequireConfiguredReleasePolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	withoutPolicy := httptest.NewRecorder()
	writeControlCapabilitiesForConfig(&config.Config{Environment: "production"}, withoutPolicy, request)
	var unavailable struct {
		Features  []string          `json:"features"`
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(withoutPolicy.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if containsCapability(unavailable.Features, "release-promotions-v1") || unavailable.Endpoints["releasePromotions"] != "" {
		t.Fatalf("unconfigured release policy was advertised: %#v", unavailable)
	}

	withPolicy := httptest.NewRecorder()
	writeControlCapabilitiesForConfig(&config.Config{
		Environment: "production", ReleaseAdmissionMode: "keyless", QualificationSigningKey: "not-used-in-production", TrustedQualificationSigningKeys: []string{"trusted"},
		GitHubActionsOIDCAudience: "norn-production", GitHubActionsReleaseBindings: []string{"demo=acme/demo@101@202"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/demo/.github/workflows/release.yml@sha"}, GitHubActionsAllowedRefs: []string{"refs/tags/v*"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedEnvironments: []string{"production"}, GitHubActionsDefaultBranch: "main",
	}, withPolicy, request)
	var available struct {
		Features  []string          `json:"features"`
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(withPolicy.Body.Bytes(), &available); err != nil {
		t.Fatal(err)
	}
	if !containsCapability(available.Features, "release-promotions-v1") || available.Endpoints["releasePromotions"] != "/api/v1/apps/{id}/promotions" {
		t.Fatalf("configured release policy was not advertised: %#v", available)
	}
}

func containsCapability(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestOnlyEnrollmentStartAndExchangeArePublic(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/v1/enrollments", true},
		{http.MethodGet, "/api/v1/enrollments", false},
		{http.MethodPost, "/api/v1/enrollments/approve", false},
		{http.MethodPost, "/api/v1/enrollments/2c91f/exchange", true},
		{http.MethodGet, "/api/v1/enrollments/2c91f/exchange", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		if got := publicEnrollmentRequest(req); got != tt.want {
			t.Errorf("%s %s public = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestGitHubActionsExchangePublicExceptionIsExact(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/v1/auth/github-actions/exchange", true},
		{http.MethodGet, "/api/v1/auth/github-actions/exchange", false},
		{http.MethodPost, "/api/v1/auth/github-actions/exchange/other", false},
	} {
		if got := publicGitHubActionsExchangeRequest(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestReleaseMutationsDeferScopeToBoundHandler(t *testing.T) {
	release := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/releases/deployments", nil)
	if got := controlScopeForRequest(release); got != "" {
		t.Fatalf("release route global scope = %q, want handler-owned scope", got)
	}
	generic := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/scale", nil)
	if got := controlScopeForRequest(generic); got != handler.ScopeAPIWrite {
		t.Fatalf("generic mutation scope = %q, want api:write", got)
	}
}

func TestExactOperationReadDefersScopeToBoundHandler(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/api/v1/operations/operation-id", nil)
		if got := controlScopeForRequest(request); got != "" {
			t.Fatalf("%s exact operation global scope=%q, want handler-owned scope", method, got)
		}
	}
	for _, path := range []string{"/api/v1/operations", "/api/v1/operations/operation-id/extra", "/api/v1/operations/operation-id/cancel"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if got := controlScopeForRequest(request); got != handler.ScopeAPIRead {
			t.Fatalf("non-exact operation path %q scope=%q, want api:read", path, got)
		}
	}
}

func TestBearerAuthEnforcesIssuedTokenScopes(t *testing.T) {
	cfg := &config.Config{APIToken: "control-plane-token", LegacyTokenSigningUntil: time.Now().Add(time.Hour)}
	h := handler.New(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	token := legacyScopedToken(t, cfg.APIToken, []string{handler.ScopeAPIRead, handler.ScopeEventsRead})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	authenticated := bearerAuth(cfg.APIToken, h, false)(next)

	for _, tt := range []struct {
		path string
		want int
	}{
		{path: "/api/apps", want: http.StatusNoContent},
		{path: "/ws", want: http.StatusNoContent},
		{path: "/api/apps/demo/exec", want: http.StatusForbidden},
		{path: "/api/v1/platform/upgrades", want: http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		req.RemoteAddr = "100.64.0.2:1234"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		authenticated.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s status = %d, want %d; body=%s", tt.path, rec.Code, tt.want, rec.Body.String())
		}
	}
}

func legacyScopedToken(t *testing.T, secret string, scopes []string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]interface{}{
		"sub": "read client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"jti": "legacy-scoped-test", "scp": scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
