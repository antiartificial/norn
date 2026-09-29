package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AuthorizedRoutePublication is supplied by a trusted control authority,
// never by the HTTP caller. Resolve must revalidate the live operation claim,
// app lock, signed acceptance, intent, and current Fleet inventory for nodeID
// immediately before returning it. A cached decision is not sufficient.
type AuthorizedRoutePublication struct {
	IntentID   string
	NodeID     string
	Route      RenderedRoute
	Expected   PublishedRouteRevision
	Generation uint64
}

type RoutePublicationAuthority func(context.Context, string, string) (*AuthorizedRoutePublication, error)

// NewNodePublisherHandler accepts only an opaque intent ID from the pinned
// mTLS control identity. It cannot be made operational without an authority
// resolver backed by live durable control state.
func NewNodePublisherHandler(routeDirectory, clientURI, nodeID string, resolve RoutePublicationAuthority) (http.Handler, error) {
	identity, err := url.Parse(clientURI)
	if !strings.HasPrefix(routeDirectory, "/") || err != nil || identity.Scheme == "" || identity.Host == "" || identity.User != nil || identity.RawQuery != "" || identity.Fragment != "" || nodeID == "" || resolve == nil {
		return nil, fmt.Errorf("node publisher configuration is incomplete")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || !hasVerifiedClientURI(r.TLS.VerifiedChains, clientURI) {
			http.Error(w, "client identity required", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" && r.URL.RawQuery == "" && r.URL.Fragment == "" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				NodeID string `json:"nodeId"`
			}{nodeID})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/routes/publish" || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "publication request is invalid", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 513))
		if err != nil || len(body) > 512 {
			http.Error(w, "publication request is too large", http.StatusBadRequest)
			return
		}
		var request struct {
			IntentID string `json:"intentId"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.IntentID == "" || len(request.IntentID) > 128 || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "publication intent is invalid", http.StatusBadRequest)
			return
		}
		decision, err := resolve(r.Context(), request.IntentID, nodeID)
		if err != nil || decision == nil || decision.IntentID != request.IntentID || decision.NodeID != nodeID || decision.Generation == 0 {
			http.Error(w, "publication authority unavailable", http.StatusConflict)
			return
		}
		if err := PublishRenderedRoute(routeDirectory, decision.Route, decision.Expected, decision.Generation); err != nil {
			http.Error(w, "publication conflict", http.StatusConflict)
			return
		}
		revision, err := ReadPublishedRouteRevision(routeDirectory, decision.Route.RouterName)
		if err != nil || !revision.Present || revision.Generation != decision.Generation || revision.RouteSHA256 != decision.Route.SHA256 {
			http.Error(w, "publication readback failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Generation  uint64 `json:"generation"`
			RouteSHA256 string `json:"routeSHA256"`
		}{revision.Generation, revision.RouteSHA256})
	}), nil
}
