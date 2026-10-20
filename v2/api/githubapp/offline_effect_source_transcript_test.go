package githubapp

import (
	"context"
	"net/http"
	"testing"
)

func TestOfflineEffectSourceTranscriptRequiresPinnedClient(t *testing.T) {
	f := goodFake()
	f.runRepo = "antiartificial/norn-fleet"
	c := testClient(t, f.handler(t))
	c.cfg.Repository = "antiartificial/norn-fleet"
	c.cfg.DefaultBranch = "main"
	if _, err := c.ReadOfflineEffectSourceTranscript(context.Background(), tipSHA); err == nil {
		t.Fatal("generic injected client could issue source proof")
	}
	// Test-only marker permits the fake transport to exercise the same
	// authenticated verifier without contacting GitHub or starting Actions.
	c.offlineEffectSourceSafe = true
	raw, err := c.ReadOfflineEffectSourceTranscript(context.Background(), tipSHA)
	if err != nil || len(raw) == 0 {
		t.Fatalf("verified transcript refused: %v", err)
	}
	f.runs = nil
	if _, err := c.ReadOfflineEffectSourceTranscript(context.Background(), tipSHA); err == nil {
		t.Fatal("missing existing validate run accepted")
	}
	prod, err := NewOfflineEffectSourceClient(Config{AppID: "1", InstallationID: 1, PrivateKeyFile: "/tmp/not-read", Repository: "antiartificial/norn-fleet", Environment: "staging", DefaultBranch: "main", ConfigPath: "environments/staging/nyc3/cluster.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !prod.offlineEffectSourceSafe || prod.cfg.APIBaseURL != "https://api.github.com" || prod.httpClient.CheckRedirect == nil {
		t.Fatal("production source client is not pinned")
	}
	transport, ok := prod.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.ServerName != "api.github.com" || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("production source transport is not pinned")
	}
	if _, err := NewOfflineEffectSourceClient(Config{APIBaseURL: "https://proxy.example.test"}); err == nil {
		t.Fatal("alternate GitHub origin accepted")
	}
}
