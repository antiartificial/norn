package etcdstore

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/fleet/offline"
)

func terminalRuntimeConfigFixture(t *testing.T) (offlineTerminalRuntimeConfig, string) {
	t.Helper()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	ca := write("ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
	cfg := offlineTerminalRuntimeConfig{AccountUUID: offlineWorkAccountUUID, ProjectID: "work-project", VPCUUID: offline.WorkVPCUUID, RequiredTags: []string{"norn-work-staging"},
		DOTokenFile: write("do.token", "do-read-token"), S3Endpoint: "s3.example.test", S3Region: "us-east-1",
		BackendBucket: "norn-state", BackendKey: "work/staging.tfstate", S3AccessKeyFile: write("s3.access", "read-access"), S3SecretKeyFile: write("s3.secret", "read-secret"),
		NomadURL: "https://nomad.example.test", ConsulURL: "https://consul.example.test", NomadServerName: "nomad.example.test", ConsulServerName: "consul.example.test",
		NomadTokenFile: write("nomad.token", "nomad-read"), ConsulTokenFile: write("consul.token", "consul-read"), NomadCAFile: ca, ConsulCAFile: ca,
		NomadPolicyName: "norn-terminal-read", NomadPolicySHA256: strings.Repeat("a", 64), TerminalJournalDir: journal}
	manifest := filepath.Join(root, "manifest.json")
	return cfg, manifest
}

func TestOfflineTerminalObserverWiresOnlyPinnedOwnerConfig(t *testing.T) {
	cfg, manifest := terminalRuntimeConfigFixture(t)
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	authority, err := newOfflineTerminalObserverFromFile(manifest)
	if err != nil || authority == nil {
		t.Fatalf("constructor: %+v %v", authority, err)
	}
	composer := authority.(offlineTerminalObservationComposer)
	backend := composer.backend.(offlineBoundTerminalBackend)
	if _, err := backend.ObserveExactTerminalBackend(context.Background(), offline.Binding{Backend: offline.Backend{Bucket: "other", Key: cfg.BackendKey}}); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("substituted backend identity reached reader: %v", err)
	}
}

func TestOfflineTerminalRuntimeConfigFailsBeforeCredentialReads(t *testing.T) {
	cfg, manifest := terminalRuntimeConfigFixture(t)
	cfg.DOTokenFile = filepath.Join(t.TempDir(), "must-not-be-read")
	cfg.TerminalJournalDir = filepath.Join(t.TempDir(), "missing")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newOfflineTerminalObserverFromFile(manifest); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("invalid journal reached credential reads: %v", err)
	}
}

func TestOfflineTerminalRuntimeConfigRejectsIdentitySubstitution(t *testing.T) {
	base, _ := terminalRuntimeConfigFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*offlineTerminalRuntimeConfig)
	}{
		{"account", func(c *offlineTerminalRuntimeConfig) { c.AccountUUID = "foreign" }},
		{"tags", func(c *offlineTerminalRuntimeConfig) { c.RequiredTags = []string{"other"} }},
		{"vpc", func(c *offlineTerminalRuntimeConfig) { c.VPCUUID = "foreign-vpc" }},
		{"nomad path", func(c *offlineTerminalRuntimeConfig) { c.NomadURL += "/v1" }},
		{"consul userinfo", func(c *offlineTerminalRuntimeConfig) { c.ConsulURL = "https://user@consul.example.test" }},
		{"backend scheme", func(c *offlineTerminalRuntimeConfig) { c.S3Endpoint = "https://s3.example.test" }},
		{"backend bucket", func(c *offlineTerminalRuntimeConfig) { c.BackendBucket = "" }},
		{"backend key", func(c *offlineTerminalRuntimeConfig) { c.BackendKey = "" }},
		{"journal symlink", func(c *offlineTerminalRuntimeConfig) {
			link := filepath.Join(t.TempDir(), "journal")
			if err := os.Symlink(c.TerminalJournalDir, link); err != nil {
				t.Fatal(err)
			}
			c.TerminalJournalDir = link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.RequiredTags = append([]string(nil), base.RequiredTags...)
			tc.mutate(&cfg)
			if validOfflineTerminalRuntimeConfig(cfg) {
				t.Fatal("substituted runtime identity accepted")
			}
		})
	}
}

type terminalRoundTripper func(*http.Request) (*http.Response, error)

func (f terminalRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfflineTerminalBackendTransportRejectsRedirect(t *testing.T) {
	closed := false
	body := &terminalCloseRecorder{closed: &closed}
	transport := rejectRedirectTransport{next: terminalRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Body: body}, nil
	})}
	request := httptest.NewRequest(http.MethodHead, "https://s3.example.test/state", nil)
	if response, err := transport.RoundTrip(request); err == nil || response != nil || !closed {
		t.Fatalf("redirect accepted or body leaked: response=%v closed=%v err=%v", response, closed, err)
	}
}

type terminalCloseRecorder struct{ closed *bool }

func (r *terminalCloseRecorder) Read([]byte) (int, error) { return 0, errors.New("unused") }
func (r *terminalCloseRecorder) Close() error             { *r.closed = true; return nil }
