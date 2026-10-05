package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
)

const repoTokenSecretValue = "ghs_REPO_READ_TOKEN_must_never_be_stored"

// fakeGitHubInstallation is a fake GitHub API that only mints installation
// tokens, and records the body of every mint request.
type fakeGitHubInstallation struct {
	mu      sync.Mutex
	bodies  []map[string]interface{}
	paths   []string
	revoked []string
}

func (f *fakeGitHubInstallation) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
		f.revoked = append(f.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f.paths = append(f.paths, r.URL.Path)
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, repoTokenSecretValue, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
}

func (f *fakeGitHubInstallation) mints() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func newRepoReadGitHub(t *testing.T) (*githubapp.Client, *fakeGitHubInstallation) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHubInstallation{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := githubapp.New(githubapp.Config{
		AppID: "1234", InstallationID: 5678, PrivateKeyFile: keyPath, Repository: "acme/norn-fleet",
		Environment: "staging", ConfigPath: "environments/staging/nyc3/cluster.yaml", APIBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, fake
}

func (f *opsFixture) opsDump() string {
	f.backend.ops.mu.Lock()
	defer f.backend.ops.mu.Unlock()
	encoded, _ := json.Marshal(f.backend.ops.accepted)
	return string(encoded)
}

func TestFleetExecutorRepoReadTokenScopeAndSecrecy(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	client, github := newRepoReadGitHub(t)
	f.routes.WithRepoTokenIssuer(client)
	principal := opsExecutorPrincipal()

	rec := f.grant(principal, "rr1", f.grantBody(executor.GrantPhasePlan, executor.CredDORead, executor.CredRepoRead))
	if rec.Code != http.StatusCreated {
		t.Fatalf("plan grant with repo-read: %d %s", rec.Code, rec.Body.String())
	}
	var raw map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if raw["repoToken"] != repoTokenSecretValue {
		t.Fatalf("response: %s", rec.Body.String())
	}
	if _, present := raw["attemptToken"]; present {
		t.Fatal("no norn-attempt was requested")
	}

	// Scope: one repository, contents:read and nothing else.
	if github.mints() != 1 {
		t.Fatalf("mints = %d", github.mints())
	}
	body := github.bodies[0]
	repos, _ := body["repositories"].([]interface{})
	perms, _ := body["permissions"].(map[string]interface{})
	if len(body) != 2 || len(repos) != 1 || repos[0] != "norn-fleet" || len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("GitHub token request = %v", body)
	}
	if github.paths[0] != "/app/installations/5678/access_tokens" {
		t.Fatalf("path = %s", github.paths[0])
	}

	// The ledger records that it was issued and when it expires, never the token.
	if strings.Contains(f.opsDump(), repoTokenSecretValue) || strings.Contains(logs.String(), repoTokenSecretValue) {
		t.Fatal("the repo token reached the ledger or the logs")
	}
	op := f.backend.ops.last(executor.KindCredentialGrant)
	expires, err := time.Parse(time.RFC3339, stringFromAny(op.Operation.Metadata[grantMetadataRepoExpiresAt]))
	if err != nil || stringFromAny(op.Operation.Metadata[grantMetadataRepoIssuedAt]) == "" {
		t.Fatalf("metadata = %v", op.Operation.Metadata)
	}
	if d := time.Until(expires); d < 50*time.Minute || d > time.Hour+time.Minute {
		t.Fatalf("recorded expiry %v", expires)
	}
	var payload executor.CredentialGrantPayload
	decodeLedgerValueMap(op.Operation.Payload, &payload)
	if strings.Join(payload.Types, ",") != "do-read,repo-read" {
		t.Fatalf("types = %v", payload.Types)
	}

	// recover may also carry it.
	f.backend.attempts[0].RetryOf = uuid.NewString()
	rec = f.grant(principal, "rr2", f.grantBody(executor.GrantPhaseRecover, executor.CredRepoRead, executor.CredNornAttempt))
	if got := decodeRepoToken(t, rec); rec.Code != http.StatusCreated || got != repoTokenSecretValue || github.mints() != 2 {
		t.Fatalf("recover grant: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(f.opsDump(), repoTokenSecretValue) || strings.Contains(logs.String(), repoTokenSecretValue) {
		t.Fatal("the repo token reached the ledger or the logs")
	}
}

func decodeRepoToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		RepoToken string `json:"repoToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.RepoToken
}

func TestFleetExecutorRepoReadReplay(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	client, github := newRepoReadGitHub(t)
	f.routes.WithRepoTokenIssuer(client)
	principal := opsExecutorPrincipal()
	body := f.grantBody(executor.GrantPhasePlan, executor.CredRepoRead)

	first := f.grant(principal, "rp1", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("%d %s", first.Code, first.Body.String())
	}
	// A lost response: the same key returns the same token, mints nothing.
	replay := f.grant(principal, "rp1", body)
	if replay.Code != http.StatusOK || decodeRepoToken(t, replay) != repoTokenSecretValue || github.mints() != 1 {
		t.Fatalf("replay: %d %s mints=%d", replay.Code, replay.Body.String(), github.mints())
	}
	if f.backend.ops.count(executor.KindCredentialGrant) != 1 {
		t.Fatal("a replay records nothing new")
	}

	// After the token expires a replay is refused, never re-minted.
	later := time.Now().Add(61 * time.Minute)
	f.routes.now = func() time.Time { return later }
	rec := f.grant(principal, "rp1", body)
	requireProblem(t, rec, http.StatusConflict, executor.CodeFleetRepoTokenReplayExpired)
	if strings.Contains(rec.Body.String(), repoTokenSecretValue) || github.mints() != 1 {
		t.Fatalf("expired replay: %s mints=%d", rec.Body.String(), github.mints())
	}

	// The memory does not survive a restart: same refusal.
	f.routes.now = func() time.Time { return time.Now().UTC() }
	f.routes.repoMu.Lock()
	f.routes.repoCache = nil
	f.routes.repoMu.Unlock()
	requireProblem(t, f.grant(principal, "rp1", body), http.StatusConflict, executor.CodeFleetRepoTokenReplayExpired)

	// A new key gets a fresh grant and a fresh token.
	if fresh := f.grant(principal, "rp2", body); fresh.Code != http.StatusCreated || github.mints() != 2 {
		t.Fatalf("fresh grant: %d mints=%d", fresh.Code, github.mints())
	}
}

type fixedExpiryIssuer struct{ expires time.Time }

func (i fixedExpiryIssuer) IssueRepoReadToken(context.Context) (string, time.Time, error) {
	return "stub-token", i.expires, nil
}

func (fixedExpiryIssuer) RevokeRepoReadToken(context.Context, string) error { return nil }

// A mint whose ledger write then fails is revoked at GitHub, never cached.
func TestFleetExecutorRepoReadRevokedWhenLedgerWriteFails(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	client, github := newRepoReadGitHub(t)
	f.routes.WithRepoTokenIssuer(client)
	f.backend.ops.failNext = errors.New("ledger down")
	rec := f.grant(opsExecutorPrincipal(), "lf1", f.grantBody(executor.GrantPhasePlan, executor.CredRepoRead))
	if rec.Code < 500 || strings.Contains(rec.Body.String(), repoTokenSecretValue) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	github.mu.Lock()
	revoked := append([]string(nil), github.revoked...)
	github.mu.Unlock()
	if github.mints() != 1 || len(revoked) != 1 || revoked[0] != repoTokenSecretValue {
		t.Fatalf("mints=%d revoked=%v", github.mints(), revoked)
	}
	f.routes.repoMu.Lock()
	cached := len(f.routes.repoCache)
	f.routes.repoMu.Unlock()
	if cached != 0 {
		t.Fatal("an unrecorded grant's token was cached")
	}
}

// The attempt-bound credential (fleet:attempt) can never request a grant.
func TestFleetExecutorRepoReadRefusesAttemptCredential(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	client, github := newRepoReadGitHub(t)
	f.routes.WithRepoTokenIssuer(client)
	attempt := &AccessPrincipal{Subject: fleetAttemptSubjectPrefix + f.attempt.ID, TokenID: "norn_x", DeviceID: opsDevice, Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	rec := f.grant(attempt, "ac1", f.grantBody(executor.GrantPhasePlan, executor.CredRepoRead))
	if rec.Code != http.StatusForbidden || github.mints() != 0 {
		t.Fatalf("%d %s mints=%d", rec.Code, rec.Body.String(), github.mints())
	}
}

func TestFleetExecutorRepoReadExpiryNeverExceedsGrant(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	f.routes.WithRepoTokenIssuer(fixedExpiryIssuer{expires: time.Now().Add(5 * time.Hour)})
	rec := f.grant(opsExecutorPrincipal(), "rx1", f.grantBody(executor.GrantPhasePlan, executor.CredRepoRead))
	grant := decodeGrant(t, rec)
	op := f.backend.ops.last(executor.KindCredentialGrant)
	if rec.Code != http.StatusCreated || stringFromAny(op.Operation.Metadata[grantMetadataRepoExpiresAt]) != grant.ExpiresAt.UTC().Format(time.RFC3339) {
		t.Fatalf("recorded expiry %v, grant expiry %v", op.Operation.Metadata, grant.ExpiresAt)
	}
}

func TestFleetExecutorRepoReadWithoutGitHubApp(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	requireProblem(t, f.grant(principal, "n1", f.grantBody(executor.GrantPhasePlan, executor.CredDORead, executor.CredRepoRead)), http.StatusServiceUnavailable, executor.CodeFleetRepoTokenUnavailable)
	// A typed-nil client is also "not configured".
	f.routes.WithRepoTokenIssuer((*githubapp.Client)(nil))
	requireProblem(t, f.grant(principal, "n2", f.grantBody(executor.GrantPhasePlan, executor.CredRepoRead)), http.StatusServiceUnavailable, executor.CodeFleetRepoTokenUnavailable)
	if f.backend.ops.count(executor.KindCredentialGrant) != 0 {
		t.Fatal("a refused grant records nothing")
	}
	// Grants without repo-read are unaffected.
	if rec := f.grant(principal, "n3", f.grantBody(executor.GrantPhasePlan, executor.CredDORead)); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetExecutorRepoReadOnlyForPlanAndRecover(t *testing.T) {
	for _, profile := range []executor.GrantProfile{executor.GrantProfileV3, executor.GrantProfilePostgresLegacyNoDrain} {
		phases := map[executor.GrantProfile][]string{
			executor.GrantProfileV3:                    {"prechange_verified", "provider_applying", "infrastructure_applied", "nodes_configured", "readiness_verified"},
			executor.GrantProfilePostgresLegacyNoDrain: {"infrastructure_applied", "nodes_configured", "readiness_verified"},
		}[profile]
		for _, phase := range phases {
			f := newOpsFixture(t, profile, phase)
			client, github := newRepoReadGitHub(t)
			f.routes.WithRepoTokenIssuer(client)
			requireProblem(t, f.grant(opsExecutorPrincipal(), "o1", f.grantBody(phase, executor.CredRepoRead)), http.StatusForbidden, executor.CodeFleetCredentialTypeNotAllowed)
			if github.mints() != 0 || f.backend.ops.count(executor.KindCredentialGrant) != 0 {
				t.Fatalf("%s/%s: a refused repo-read minted or recorded", profile, phase)
			}
		}
	}
}
