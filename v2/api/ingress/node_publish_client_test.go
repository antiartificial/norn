package ingress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublishRouteIntentToNodesReportsPartialPublication(t *testing.T) {
	const intentID = "intent-1"
	digest := strings.Repeat("a", 64)
	var firstCalls atomic.Int32
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/routes/publish" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected publication request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["intentId"] != intentID {
			t.Errorf("caller sent an unauthorized route payload: %v, %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generation":1,"routeSHA256":"` + digest + `"}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		http.Error(w, "authority lost", http.StatusConflict)
	}))
	defer second.Close()
	client := first.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Test transport only; production wrapper pins the CA.
	nodes := []IngressNode{{ID: "ingress-01", APIURL: first.URL}, {ID: "ingress-02", APIURL: second.URL}}
	receipts, err := publishRouteIntentToNodes(context.Background(), client, nodes, intentID, 1, digest)
	if err == nil || len(receipts) != 1 || receipts[0].NodeID != "ingress-01" || receipts[0].RouteSHA256 != digest || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("partial publication = %+v, %v; calls = %d/%d", receipts, err, firstCalls.Load(), secondCalls.Load())
	}
	nodes[1].APIURL = "https://203.0.113.10:18083"
	if receipts, err := publishRouteIntentToNodes(context.Background(), client, nodes, intentID, 1, digest); err == nil || len(receipts) != 0 || firstCalls.Load() != 1 {
		t.Fatalf("invalid inventory caused publication: %+v, %v", receipts, err)
	}
	nodes[1].APIURL = first.URL
	if _, err := publishRouteIntentToNodes(context.Background(), client, nodes, intentID, 1, digest); err == nil || firstCalls.Load() != 1 {
		t.Fatal("duplicate publisher origin caused publication")
	}
}

func TestIngressPublisherNodesUseSeparatePortAndExactInventoryIPs(t *testing.T) {
	observers := []IngressNode{{ID: "one", APIURL: "https://10.43.0.21:18082"}, {ID: "two", APIURL: "https://10.43.0.22:18082"}}
	publishers, err := ingressPublisherNodes(observers, 18083)
	if err != nil || len(publishers) != 2 || publishers[0].APIURL != "https://10.43.0.21:18083" || publishers[1].APIURL != "https://10.43.0.22:18083" || observers[0].APIURL != "https://10.43.0.21:18082" {
		t.Fatalf("publisher targets=%+v observers=%+v err=%v", publishers, observers, err)
	}
	for _, invalid := range []IngressNode{{ID: "public", APIURL: "https://203.0.113.10:18082"}, {ID: "name", APIURL: "https://ingress.example.test:18082"}, {ID: "path", APIURL: "https://10.43.0.21:18082/other"}} {
		if _, err := ingressPublisherNodes([]IngressNode{observers[0], invalid}, 18083); err == nil {
			t.Fatalf("invalid publisher inventory accepted: %+v", invalid)
		}
	}
	if _, err := ingressPublisherNodes(observers, 0); err == nil {
		t.Fatal("missing publisher port accepted")
	}
}
