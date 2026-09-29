package ingress

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewNodeReadbackHandler exposes only the local route revision and Traefik's
// loopback raw configuration to the one pinned control-plane client identity.
// The caller must also require and verify client certificates in the TLS
// listener. This read-only surface cannot authorize route publication.
func NewNodeReadbackHandler(routeDirectory, traefikOrigin, clientURI string) (http.Handler, error) {
	if !strings.HasPrefix(routeDirectory, "/") || clientURI == "" {
		return nil, fmt.Errorf("node readback configuration is incomplete")
	}
	identity, err := url.Parse(clientURI)
	if err != nil || identity.Scheme == "" || identity.Host == "" || identity.User != nil || identity.RawQuery != "" || identity.Fragment != "" {
		return nil, fmt.Errorf("node readback client identity is invalid")
	}
	origin, err := url.Parse(traefikOrigin)
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || origin.Hostname() == "" {
		return nil, fmt.Errorf("Traefik readback origin is invalid")
	}
	ip := net.ParseIP(origin.Hostname())
	if ip == nil || !ip.IsLoopback() || origin.Port() == "" {
		return nil, fmt.Errorf("Traefik readback must use a loopback socket")
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || !hasVerifiedClientURI(r.TLS.VerifiedChains, clientURI) {
			http.Error(w, "client identity required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.Fragment != "" {
			http.Error(w, "readback request is invalid", http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/api/rawdata":
			request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, traefikOrigin+"/api/rawdata", nil)
			if err != nil {
				http.Error(w, "readback unavailable", http.StatusBadGateway)
				return
			}
			response, err := client.Do(request)
			if err != nil {
				http.Error(w, "readback unavailable", http.StatusBadGateway)
				return
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) > 1<<20 || !json.Valid(body) {
				http.Error(w, "readback unavailable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case strings.HasPrefix(r.URL.Path, "/v1/routes/") && strings.HasSuffix(r.URL.Path, "/revision"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/routes/"), "/revision")
			if !publishedRouteName.MatchString(name) {
				http.Error(w, "route identity is invalid", http.StatusBadRequest)
				return
			}
			revision, err := ReadPublishedRouteRevision(routeDirectory, name)
			if err != nil {
				http.Error(w, "route revision unavailable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Generation  uint64 `json:"generation"`
				RouteSHA256 string `json:"routeSHA256"`
				Present     bool   `json:"present"`
			}{revision.Generation, revision.RouteSHA256, revision.Present})
		default:
			http.NotFound(w, r)
		}
	}), nil
}

func hasVerifiedClientURI(chains [][]*x509.Certificate, expected string) bool {
	for _, chain := range chains {
		if len(chain) == 0 {
			continue
		}
		for _, identity := range chain[0].URIs {
			if identity.String() == expected {
				return true
			}
		}
	}
	return false
}
