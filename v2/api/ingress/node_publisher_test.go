package ingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNodePublisherUsesLiveAuthorityAndRejectsCallerRoute(t *testing.T) {
	const identity = "spiffe://norn.test/control/ingress-publisher"
	const nodeID = "ingress-1"
	routes := t.TempDir()
	route := fixtureRoute(t, "publisher")
	allowed := true
	calls := 0
	handler, err := NewNodePublisherHandler(routes, identity, nodeID, func(_ context.Context, intentID, node string) (*AuthorizedRoutePublication, error) {
		calls++
		if !allowed || intentID != "reserved-intent" || node != nodeID {
			return nil, nil
		}
		return &AuthorizedRoutePublication{IntentID: intentID, NodeID: node, Route: route, Generation: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string) *http.Request {
		r := verifiedReadbackRequest(t, "/v1/routes/publish", identity)
		r.Method = http.MethodPost
		r.Body = http.NoBody
		if body != "" {
			r.Body = io.NopCloser(strings.NewReader(body))
		}
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	unverified := request(`{"intentId":"reserved-intent"}`)
	unverified.TLS = nil
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unverified)
	if response.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("unverified publish status=%d authority calls=%d", response.Code, calls)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(`{"intentId":"reserved-intent","route":"forged"}`))
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("caller route status=%d authority calls=%d", response.Code, calls)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(`{"intentId":"reserved-intent"}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), route.SHA256) || calls != 1 {
		t.Fatalf("authorized publish status=%d body=%q calls=%d", response.Code, response.Body.String(), calls)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(`{"intentId":"reserved-intent"}`))
	if response.Code != http.StatusOK || calls != 2 {
		t.Fatalf("idempotent retry status=%d calls=%d", response.Code, calls)
	}
	allowed = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request(`{"intentId":"reserved-intent"}`))
	if response.Code != http.StatusConflict || calls != 3 {
		t.Fatalf("revoked authority status=%d calls=%d", response.Code, calls)
	}
}

func TestNodePublisherRejectsAuthorityForAnotherNode(t *testing.T) {
	const identity = "spiffe://norn.test/control/ingress-publisher"
	routes := t.TempDir()
	route := fixtureRoute(t, "publisher-wrong-node")
	handler, err := NewNodePublisherHandler(routes, identity, "ingress-1", func(_ context.Context, intentID, _ string) (*AuthorizedRoutePublication, error) {
		return &AuthorizedRoutePublication{IntentID: intentID, NodeID: "ingress-2", Route: route, Generation: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := verifiedReadbackRequest(t, "/v1/routes/publish", identity)
	request.Method = http.MethodPost
	request.Header.Set("Content-Type", "application/json")
	request.Body = io.NopCloser(strings.NewReader(`{"intentId":"reserved-intent"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("wrong-node authority status=%d", response.Code)
	}
	if revision, err := ReadPublishedRouteRevision(routes, route.RouterName); err != nil || revision.Present {
		t.Fatalf("wrong-node authority published route=%+v err=%v", revision, err)
	}
}
