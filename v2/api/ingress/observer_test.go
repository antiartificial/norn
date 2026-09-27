package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func observedRawData(t *testing.T, desired RenderedRoute) map[string]any {
	t.Helper()
	var document routeDocument
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		t.Fatal(err)
	}
	router := document.HTTP.Routers[desired.RouterName]
	service := document.HTTP.Services[desired.ServiceName]
	routerValue := map[string]any{"status": "enabled", "rule": router.Rule, "entryPoints": router.EntryPoints, "service": desired.ServiceName + "@file"}
	if router.TLS != nil {
		routerValue["tls"] = map[string]any{}
	}
	services := map[string]any{desired.ServiceName + "@file": map[string]any{"status": "enabled", "weighted": service.Weighted}}
	for _, backend := range service.Weighted.Services {
		services[backend.Name] = map[string]any{"status": "enabled"}
	}
	return map[string]any{"routers": map[string]any{desired.RouterName + "@file": routerValue}, "services": services}
}

func testRawDataServer(t *testing.T, data map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/rawdata" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			t.Errorf("encode raw configuration: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestObserveRenderedRouteRequiresEveryIngressNode(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 70}, {DeploymentID: "new", Weight: 30}}})
	if err != nil {
		t.Fatal(err)
	}
	first := testRawDataServer(t, observedRawData(t, desired))
	second := testRawDataServer(t, observedRawData(t, desired))
	nodes := []IngressNode{{ID: "ingress-a", APIURL: first.URL}, {ID: "ingress-b", APIURL: second.URL}}
	observed, err := ObserveRenderedRoute(context.Background(), first.Client(), nodes, desired)
	if err != nil || len(observed) != 2 || observed[0].RouteSHA256 != desired.SHA256 || observed[1].NodeID != "ingress-b" {
		t.Fatalf("two-node route observation=%+v err=%v", observed, err)
	}
	stale := observedRawData(t, desired)
	delete(stale["services"].(map[string]any), desired.ServiceName+"@file")
	staleNode := testRawDataServer(t, stale)
	nodes[1].APIURL = staleNode.URL
	if observed, err := ObserveRenderedRoute(context.Background(), first.Client(), nodes, desired); err == nil || len(observed) != 0 {
		t.Fatalf("partial propagation accepted: observations=%+v err=%v", observed, err)
	}
}

func TestObserveRenderedRouteRejectsWrongWeightAndCompetingRouter(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 70}, {DeploymentID: "new", Weight: 30}}})
	if err != nil {
		t.Fatal(err)
	}
	wrongWeight := observedRawData(t, desired)
	service := wrongWeight["services"].(map[string]any)[desired.ServiceName+"@file"].(map[string]any)
	weighted := service["weighted"].(struct {
		Services []routeBackend `yaml:"services"`
	})
	weighted.Services[0].Weight++
	service["weighted"] = weighted
	wrongWeightNode := testRawDataServer(t, wrongWeight)
	if _, err := ObserveRenderedRoute(context.Background(), wrongWeightNode.Client(), []IngressNode{{ID: "ingress-a", APIURL: wrongWeightNode.URL}}, desired); err == nil {
		t.Fatal("wrong effective weight accepted")
	}
	competing := observedRawData(t, desired)
	competing["routers"].(map[string]any)["legacy@consulcatalog"] = map[string]any{"status": "enabled", "rule": "Host(`orders.example.com`)", "service": "legacy@consulcatalog"}
	competingNode := testRawDataServer(t, competing)
	if _, err := ObserveRenderedRoute(context.Background(), competingNode.Client(), []IngressNode{{ID: "ingress-a", APIURL: competingNode.URL}}, desired); err == nil {
		t.Fatal("competing public router accepted")
	}
}

func TestObserveRenderedRouteDoesNotFollowAPIOriginRedirect(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	var offOriginRequests atomic.Int32
	offOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offOriginRequests.Add(1)
	}))
	defer offOrigin.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offOrigin.URL, http.StatusFound)
	}))
	defer redirecting.Close()
	if _, err := ObserveRenderedRoute(context.Background(), redirecting.Client(), []IngressNode{{ID: "ingress-a", APIURL: redirecting.URL}}, desired); err == nil || offOriginRequests.Load() != 0 {
		t.Fatalf("API redirect was followed or accepted: requests=%d err=%v", offOriginRequests.Load(), err)
	}
}
