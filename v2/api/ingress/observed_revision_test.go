package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func revisionReadbackServer(t *testing.T, desired RenderedRoute, generation *atomic.Uint64, rawRead func()) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/routes/" + desired.RouterName + "/revision":
			_, _ = fmt.Fprintf(w, `{"generation":%d,"routeSHA256":%q,"present":true}`, generation.Load(), desired.SHA256)
		case "/api/rawdata":
			if rawRead != nil {
				rawRead()
			}
			if err := json.NewEncoder(w).Encode(observedRawData(t, desired)); err != nil {
				t.Errorf("encode rawdata: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestObservePublishedRenderedRouteJoinsEveryNodeFileAndTraefik(t *testing.T) {
	desired := fixtureRoute(t, "release-a")
	var firstGeneration, secondGeneration atomic.Uint64
	firstGeneration.Store(7)
	secondGeneration.Store(7)
	first := revisionReadbackServer(t, desired, &firstGeneration, nil)
	defer first.Close()
	second := revisionReadbackServer(t, desired, &secondGeneration, nil)
	defer second.Close()
	nodes := []IngressNode{{ID: "ingress-a", APIURL: first.URL}, {ID: "ingress-b", APIURL: second.URL}}
	observed, err := ObservePublishedRenderedRoute(context.Background(), first.Client(), nodes, desired, 7)
	if err != nil || len(observed) != 2 || observed[0].PublishedGeneration != 7 || observed[1].MatchedDesiredRouteSHA256 != desired.SHA256 {
		t.Fatalf("joined two-node observation = %+v, %v", observed, err)
	}
	secondGeneration.Store(8)
	if _, err := ObservePublishedRenderedRoute(context.Background(), first.Client(), nodes, desired, 7); err == nil {
		t.Fatal("node with another published generation was accepted")
	}
	secondGeneration.Store(7)
	nodes[1].APIURL = first.URL
	if _, err := ObservePublishedRenderedRoute(context.Background(), first.Client(), nodes, desired, 7); err == nil {
		t.Fatal("one endpoint presented as two nodes was accepted")
	}
}

func TestObservePublishedRenderedRouteRejectsChangeDuringTraefikReadback(t *testing.T) {
	desired := fixtureRoute(t, "release-a")
	var generation atomic.Uint64
	generation.Store(7)
	server := revisionReadbackServer(t, desired, &generation, func() { generation.Store(8) })
	defer server.Close()
	if _, err := ObservePublishedRenderedRoute(context.Background(), server.Client(), []IngressNode{{ID: "ingress-a", APIURL: server.URL}}, desired, 7); err == nil {
		t.Fatal("route generation changed during effective readback")
	}
}

func TestDecodePublishedRevisionRejectsAmbiguousRecords(t *testing.T) {
	for _, body := range []string{
		`{"generation":7,"generation":8,"routeSHA256":"abc","present":true}`,
		`{"generation":7,"routeSHA256":"abc","present":true,"extra":1}`,
		`{"generation":7,"routeSHA256":"abc"}`,
		`{"generation":7,"routeSHA256":"abc","present":true} {}`,
	} {
		if _, err := decodePublishedRevision([]byte(body)); err == nil {
			t.Fatalf("ambiguous revision was accepted: %s", body)
		}
	}
}
