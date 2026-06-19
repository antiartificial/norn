package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/model"
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

	for _, host := range []string{"100.88.12.4", "100.88.12.4:7070", "harbor.tail113139.ts.net:8443"} {
		target, ok := wakeGatewayTargetForHost(services, host)
		if !ok {
			t.Fatalf("expected target for %s", host)
		}
		if target.App != "harbor" || target.Process != "web" {
			t.Fatalf("target = %+v, want harbor/web", target)
		}
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

func TestWakeGatewayHostMiddlewareSkipsControlPlaneAPIPaths(t *testing.T) {
	h := &Handler{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "trove.example.com"
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

func TestRequestHostnameNormalizesHostHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api", nil)
	req.Host = "Trove.Example.Com:8443"
	if got := requestHostname(req); got != "trove.example.com" {
		t.Fatalf("hostname = %q, want trove.example.com", got)
	}
}
