package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func TestWakeGatewayTargetForHostMapsPublicServiceEndpoint(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "trove",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "https://trove.example.com"},
			},
			Instances: []model.ServiceInstance{
				{Address: "127.0.0.1", Port: 9090, Status: "passing"},
			},
		},
		{
			App:     "worker-only",
			Process: "worker",
			Type:    "worker",
			Endpoints: []model.Endpoint{
				{URL: "https://worker.example.com"},
			},
		},
	}

	target, ok := wakeGatewayTargetForHost(services, "trove.example.com")
	if !ok {
		t.Fatalf("expected target")
	}
	if target.App != "trove" || target.Process != "web" || target.Endpoint != "https://trove.example.com" {
		t.Fatalf("target = %+v", target)
	}
	if _, ok := wakeGatewayTargetForHost(services, "worker.example.com"); ok {
		t.Fatalf("worker endpoint should not be wake-routable")
	}
}

func TestWakeGatewayTargetForHostMapsTailnetEndpointWithPort(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "harbor",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "http://100.88.12.4:7070"},
				{URL: "https://harbor.tail113139.ts.net:8443"},
			},
			Instances: []model.ServiceInstance{
				{Address: "100.88.12.4", Port: 7070, Status: "passing"},
			},
		},
	}

	for _, host := range []string{"100.88.12.4:7070", "harbor.tail113139.ts.net:8443"} {
		target, ok := wakeGatewayTargetForHost(services, host)
		if !ok {
			t.Fatalf("expected target for %s", host)
		}
		if target.App != "harbor" || target.Process != "web" {
			t.Fatalf("target = %+v, want harbor/web", target)
		}
	}

	target, ok := wakeGatewayTargetForHost(services, "100.88.12.4%3A7070")
	if !ok {
		t.Fatalf("expected target for escaped host:port")
	}
	if target.App != "harbor" || target.Process != "web" {
		t.Fatalf("escaped target = %+v, want harbor/web", target)
	}
	if _, ok := wakeGatewayTargetForHost(services, "100.88.12.4"); ok {
		t.Fatalf("host without declared endpoint port should not match")
	}
}

func TestWakeGatewayTargetForHostMapsBarePrivateEndpoint(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "ft-trove",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "ft-trove.norn"},
			},
			Instances: []model.ServiceInstance{
				{Address: "127.0.0.1", Port: 21485, Status: "passing"},
			},
		},
	}

	target, ok := wakeGatewayTargetForHost(services, "ft-trove.norn")
	if !ok {
		t.Fatalf("expected bare private endpoint target")
	}
	if target.App != "ft-trove" || target.Process != "web" || target.Endpoint != "ft-trove.norn" {
		t.Fatalf("target = %+v", target)
	}
}

func TestWakeGatewayTargetForAppPrefersWebService(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "ft-trove",
			Process: "worker",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "ft-trove-worker.norn"},
			},
		},
		{
			App:     "ft-trove",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "ft-trove.norn"},
			},
		},
	}

	target, ok := wakeGatewayTargetForApp(services, "ft-trove")
	if !ok {
		t.Fatalf("expected app target")
	}
	if target.Process != "web" || target.Key != "ft-trove.norn" {
		t.Fatalf("target = %+v, want web ft-trove.norn", target)
	}
}

func TestWakeGatewayTargetForHostDoesNotUseCloudflarePublicFilter(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "mini",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "https://aarons-mac-mini.tail113139.ts.net"},
			},
		},
	}
	if _, ok := wakeGatewayTargetForHost(services, "aarons-mac-mini.tail113139.ts.net"); !ok {
		t.Fatalf("expected tailnet MagicDNS endpoint to be wake-routable")
	}
}

func TestWakeGatewayHostMiddlewareRoutesMappedAPIPaths(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "trove")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("mkdir app dir: %v", err)
	}
	spec := `name: trove
deploy: true
processes:
  web:
    port: 4173
endpoints:
  - url: https://trove.example.com:4173
`
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(spec), 0644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Host = "trove.example.com:4173"
	rec := httptest.NewRecorder()

	h.WakeGatewayHostMiddleware(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 from routed app host", rec.Code)
	}
}

func TestWakeGatewayHostMiddlewarePassesUnmappedAPIPaths(t *testing.T) {
	h := &Handler{cfg: &config.Config{AppsDir: t.TempDir()}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "norn.example.com"
	rec := httptest.NewRecorder()

	h.WakeGatewayHostMiddleware(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestFirstReadyInstanceRequiresRoutablePassingInstance(t *testing.T) {
	instance, ok := firstReadyInstance(model.ServiceManifestEntry{
		Instances: []model.ServiceInstance{
			{Address: "127.0.0.1", Port: 9000, Status: "critical"},
			{Address: "127.0.0.1", Port: 9001, Status: "passing"},
		},
	})
	if !ok {
		t.Fatalf("expected ready instance")
	}
	if instance.Port != 9001 {
		t.Fatalf("instance = %+v, want port 9001", instance)
	}
}

func TestWakeGatewayUpstreamPathStripsGatewayPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/wake-gateway/trove.example.com/assets/app.js?x=1", nil)
	if got := wakeGatewayUpstreamPath(req, "trove.example.com"); got != "/assets/app.js" {
		t.Fatalf("path = %q, want /assets/app.js", got)
	}
	rootReq := httptest.NewRequest(http.MethodGet, "/api/wake-gateway/trove.example.com", nil)
	if got := wakeGatewayUpstreamPath(rootReq, "trove.example.com"); got != "/" {
		t.Fatalf("root path = %q, want /", got)
	}
}

func TestWakeGatewayUpstreamPathStripsPortTargetPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/wake-gateway/100.88.12.4:7070/assets/app.js?x=1", nil)
	if got := wakeGatewayUpstreamPath(req, "100.88.12.4:7070"); got != "/assets/app.js" {
		t.Fatalf("path = %q, want /assets/app.js", got)
	}

	encodedReq := httptest.NewRequest(http.MethodGet, "/api/wake-gateway/100.88.12.4%3A7070/assets/app.js?x=1", nil)
	if got := wakeGatewayUpstreamPath(encodedReq, "100.88.12.4:7070"); got != "/assets/app.js" {
		t.Fatalf("encoded path = %q, want /assets/app.js", got)
	}
}

func TestWakeGatewayAliasUpstreamPathStripsAppPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/a/ft-trove/assets/app.js?x=1", nil)
	if got := wakeGatewayAliasUpstreamPath(req, "ft-trove"); got != "/assets/app.js" {
		t.Fatalf("path = %q, want /assets/app.js", got)
	}

	rootReq := httptest.NewRequest(http.MethodGet, "/api/a/ft-trove", nil)
	if got := wakeGatewayAliasUpstreamPath(rootReq, "ft-trove"); got != "/" {
		t.Fatalf("root path = %q, want /", got)
	}
}

func TestRewriteWakeGatewayAliasResponsePrefixesRootAssets(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(`<!doctype html>
<script type="module" src="/assets/index.js"></script>
<link rel="stylesheet" href="/assets/index.css">
<link rel="icon" href="/favicon.svg">`)),
	}

	if err := rewriteWakeGatewayAliasResponse(resp, "/api/a/ft-trove"); err != nil {
		t.Fatalf("rewrite html: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	html := string(body)
	for _, want := range []string{
		`src="/api/a/ft-trove/assets/index.js"`,
		`href="/api/a/ft-trove/assets/index.css"`,
		`href="/api/a/ft-trove/favicon.svg"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rewritten html missing %q in %s", want, html)
		}
	}
	if got := resp.Header.Get("Content-Length"); got == "" {
		t.Fatalf("expected content-length to be updated")
	}
}

func TestRewriteWakeGatewayAliasResponsePrefixesRootAPIStrings(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/javascript; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader("fetch('/api/session');fetch(\"/api/items?x=1\");const media=`/api/media/${key}`")),
	}

	if err := rewriteWakeGatewayAliasResponse(resp, "/api/a/ft-trove"); err != nil {
		t.Fatalf("rewrite js: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	js := string(body)
	for _, want := range []string{
		`'/api/a/ft-trove/api/session'`,
		`"/api/a/ft-trove/api/items?x=1"`,
		"`/api/a/ft-trove/api/media/${key}`",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("rewritten js missing %q in %s", want, js)
		}
	}
}

func TestRequestHostnameNormalizesHostHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api", nil)
	req.Host = "Trove.Example.Com:8443"
	if got := requestHostname(req); got != "trove.example.com" {
		t.Fatalf("hostname = %q, want trove.example.com", got)
	}
}

func TestWakeGatewayCapacityIntentIsSignedAndReplayedAcrossConcurrentRequests(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "sleeper")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`name: sleeper
deploy: true
processes:
  web:
    command: serve
regions:
  local:
    nomadRegion: global
`), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, &config.Config{AppsDir: appsDir, AuditSigningKey: strings.Repeat("w", 32)}, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	target := wakeGatewayTarget{App: "sleeper", Process: "web"}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.queueWakeGatewayCapacityIntent(context.Background(), target)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent wake capacity intent: %v", err)
		}
	}
	if err := h.queueWakeGatewayCapacityIntent(context.Background(), target); err != nil {
		t.Fatalf("replay wake capacity intent: %v", err)
	}

	ops, err := db.ListOperations(context.Background(), store.OperationFilter{App: "sleeper", Kind: "app.scale", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("scale operations = %d, want one durable replay target", len(ops))
	}
	if got := ops[0].Payload; got["group"] != "web" || got["region"] != "local" || got["nomadRegion"] != "global" || got["count"] != float64(1) {
		t.Fatalf("wake scale payload = %#v", got)
	}
	var signed int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1`, ops[0].ID).Scan(&signed); err != nil {
		t.Fatal(err)
	}
	if signed != 1 {
		t.Fatalf("signed acceptance intents = %d, want 1", signed)
	}
}

func TestWakeGatewayCapacityIntentRejectsAmbiguousOrInvalidWakeTarget(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "multi")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`name: multi
deploy: true
processes:
  web:
    command: serve
regions:
  iad: {}
  ord: {}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}, pipeline: &pipeline.Pipeline{}, operationStore: wakeTestOperationStore{}}
	if err := h.queueWakeGatewayCapacityIntent(context.Background(), wakeGatewayTarget{App: "multi", Process: "web"}); err == nil || !strings.Contains(err.Error(), "exactly one declared process region") {
		t.Fatalf("ambiguous wake error = %v", err)
	}
	if err := h.queueWakeGatewayCapacityIntent(context.Background(), wakeGatewayTarget{App: "multi", Process: "missing"}); err == nil || !strings.Contains(err.Error(), "not a scalable service process") {
		t.Fatalf("invalid wake error = %v", err)
	}
}

type wakeTestOperationStore struct{}

func (wakeTestOperationStore) Authority(context.Context) (string, error) {
	return "00000000-0000-0000-0000-000000000001", nil
}
func (wakeTestOperationStore) Accept(context.Context, store.OperationAcceptance) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, nil
}
func (wakeTestOperationStore) Resolve(context.Context, store.OperationRequestIdentity, store.RequestFingerprint) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, nil
}
