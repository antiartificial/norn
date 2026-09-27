package ingress

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type routeAuthorityRequest struct {
	IntentID string `json:"intentId"`
}

// NewControlRouteAuthorityHandler binds an ingress node to its verified mTLS
// URI SAN. The resolver must check live durable control state on every call.
// The listener must require and verify client certificates with a dedicated
// ingress-node CA; this handler does not configure TLS itself.
func NewControlRouteAuthorityHandler(nodeURIs map[string]string, resolve RoutePublicationAuthority) (http.Handler, error) {
	if len(nodeURIs) == 0 || resolve == nil {
		return nil, fmt.Errorf("route authority configuration is incomplete")
	}
	for identity, nodeID := range nodeURIs {
		uri, err := url.Parse(identity)
		if err != nil || uri.Scheme == "" || uri.Host == "" || uri.User != nil || uri.RawQuery != "" || uri.Fragment != "" || nodeID == "" {
			return nil, fmt.Errorf("route authority node identity is invalid")
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "node identity required", http.StatusUnauthorized)
			return
		}
		nodeID := ""
		for identity, candidate := range nodeURIs {
			if hasVerifiedClientURI(r.TLS.VerifiedChains, identity) {
				if nodeID != "" {
					http.Error(w, "ambiguous node identity", http.StatusUnauthorized)
					return
				}
				nodeID = candidate
			}
		}
		if nodeID == "" {
			http.Error(w, "node identity required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/route-authorizations" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "route authority request is invalid", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 513))
		if err != nil || len(body) > 512 {
			http.Error(w, "route authority request is too large", http.StatusBadRequest)
			return
		}
		var request routeAuthorityRequest
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.IntentID == "" || len(request.IntentID) > 128 || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "route authority intent is invalid", http.StatusBadRequest)
			return
		}
		decision, err := resolve(r.Context(), request.IntentID, nodeID)
		if err != nil || decision == nil || decision.IntentID != request.IntentID || decision.NodeID != nodeID || decision.Generation == 0 {
			http.Error(w, "route authority unavailable", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(decision)
	}), nil
}

// NewRemoteRoutePublicationAuthority creates the host-side live callback.
// The origin must be a private HTTPS IP whose certificate also has the pinned
// control URI SAN. The client certificate must identify this ingress node.
func NewRemoteRoutePublicationAuthority(origin, controlURI string, caPEM, certPEM, keyPEM []byte) (RoutePublicationAuthority, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Port() == "" {
		return nil, fmt.Errorf("route authority origin must be private HTTPS")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return nil, fmt.Errorf("route authority origin must use a private IP")
	}
	identity, err := url.Parse(controlURI)
	if err != nil || identity.Scheme == "" || identity.Host == "" || identity.User != nil || identity.RawQuery != "" || identity.Fragment != "" {
		return nil, fmt.Errorf("route authority server identity is invalid")
	}
	client, err := NewMutualTLSNodeClient(caPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || !hasVerifiedClientURI(state.VerifiedChains, controlURI) {
			return fmt.Errorf("route authority server URI is untrusted")
		}
		return nil
	}
	return func(ctx context.Context, intentID, nodeID string) (*AuthorizedRoutePublication, error) {
		if intentID == "" || len(intentID) > 128 || strings.TrimSpace(nodeID) == "" {
			return nil, fmt.Errorf("route publication identity is incomplete")
		}
		body, _ := json.Marshal(routeAuthorityRequest{IntentID: intentID})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/v1/route-authorizations", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("route authority refused publication: %d", response.StatusCode)
		}
		const maxAuthorityResponseBytes = 2*maxPublishedRouteBytes + 4096
		limited, err := io.ReadAll(io.LimitReader(response.Body, maxAuthorityResponseBytes+1))
		if err != nil || len(limited) > maxAuthorityResponseBytes {
			return nil, fmt.Errorf("route authority response is invalid")
		}
		var decision AuthorizedRoutePublication
		if err := json.Unmarshal(limited, &decision); err != nil || decision.IntentID != intentID || decision.NodeID != nodeID || decision.Generation == 0 {
			return nil, fmt.Errorf("route authority response differs from request")
		}
		return &decision, nil
	}, nil
}
