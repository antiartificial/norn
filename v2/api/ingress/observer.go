package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

type IngressNode struct {
	ID     string
	APIURL string
}

type NodeObservation struct {
	NodeID                    string
	MatchedDesiredRouteSHA256 string
}

type rawRoute struct {
	Rule        string          `json:"rule"`
	EntryPoints []string        `json:"entryPoints"`
	Service     string          `json:"service"`
	Status      string          `json:"status"`
	TLS         json.RawMessage `json:"tls"`
}

type rawService struct {
	Status   string `json:"status"`
	Weighted *struct {
		Services []routeBackend `json:"services"`
	} `json:"weighted"`
}

type rawData struct {
	Routers  map[string]rawRoute   `json:"routers"`
	Services map[string]rawService `json:"services"`
	Errors   json.RawMessage       `json:"errors"`
}

// ObserveRenderedRoute checks Traefik's effective configuration on every
// named ingress node. It proves config propagation only; endpoint probes and
// public-path behavior remain separate evidence.
func ObserveRenderedRoute(ctx context.Context, client *http.Client, nodes []IngressNode, desired RenderedRoute) ([]NodeObservation, error) {
	if client == nil || len(nodes) == 0 || desired.SHA256 == "" || desired.RouterName == "" || desired.ServiceName == "" || desired.EndpointHost == "" {
		return nil, fmt.Errorf("ingress observation is incomplete")
	}
	var document routeDocument
	digest := sha256.Sum256(desired.YAML)
	if hex.EncodeToString(digest[:]) != desired.SHA256 {
		return nil, fmt.Errorf("desired ingress route digest differs from content")
	}
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		return nil, fmt.Errorf("decode desired ingress route: %w", err)
	}
	seen := map[string]bool{}
	observations := make([]NodeObservation, 0, len(nodes))
	boundedClient := *client
	boundedClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	for _, node := range nodes {
		if node.ID == "" || seen[node.ID] {
			return nil, fmt.Errorf("ingress node identity is missing or repeated")
		}
		seen[node.ID] = true
		endpoint, err := url.Parse(node.APIURL)
		if err != nil || endpoint == nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || endpoint.RawPath != "" || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && net.ParseIP(endpoint.Hostname()) != nil && net.ParseIP(endpoint.Hostname()).IsLoopback())) {
			return nil, fmt.Errorf("ingress node %s API origin is invalid", node.ID)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(node.APIURL, "/")+"/api/rawdata", nil)
		if err != nil {
			return nil, err
		}
		response, err := boundedClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("read ingress node %s: %w", node.ID, err)
		}
		if response.Request.URL.Scheme != endpoint.Scheme || response.Request.URL.Host != endpoint.Host {
			response.Body.Close()
			return nil, fmt.Errorf("ingress node %s redirected outside its API origin", node.ID)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) > 1<<20 {
			return nil, fmt.Errorf("ingress node %s did not return bounded raw configuration", node.ID)
		}
		if err := verifyRawData(body, desired, document); err != nil {
			return nil, fmt.Errorf("ingress node %s: %w", node.ID, err)
		}
		observations = append(observations, NodeObservation{NodeID: node.ID, MatchedDesiredRouteSHA256: desired.SHA256})
	}
	return observations, nil
}

func verifyRawData(body []byte, desired RenderedRoute, document routeDocument) error {
	var observed rawData
	if err := json.Unmarshal(body, &observed); err != nil {
		return fmt.Errorf("decode Traefik raw configuration: %w", err)
	}
	if len(observed.Errors) != 0 && string(observed.Errors) != "null" && string(observed.Errors) != "{}" && string(observed.Errors) != "[]" {
		return fmt.Errorf("Traefik reports configuration errors")
	}
	wantRouter := document.HTTP.Routers[desired.RouterName]
	wantService := document.HTTP.Services[desired.ServiceName]
	routerKey := desired.RouterName + "@file"
	serviceKey := desired.ServiceName + "@file"
	router, ok := observed.Routers[routerKey]
	if !ok || router.Status != "enabled" || router.Rule != wantRouter.Rule || (router.Service != desired.ServiceName && router.Service != serviceKey) || len(router.EntryPoints) != len(wantRouter.EntryPoints) || len(router.EntryPoints) != 1 || router.EntryPoints[0] != wantRouter.EntryPoints[0] {
		return fmt.Errorf("effective public router differs from desired route")
	}
	if (wantRouter.TLS != nil) != (len(router.TLS) != 0 && string(router.TLS) != "null") {
		return fmt.Errorf("effective router TLS mode differs from desired route")
	}
	for name, other := range observed.Routers {
		if name != routerKey && strings.Contains(other.Rule, "Host(`"+desired.EndpointHost+"`)") && other.Status == "enabled" {
			return fmt.Errorf("competing enabled public-host router %s", name)
		}
	}
	service, ok := observed.Services[serviceKey]
	if !ok || service.Status != "enabled" || service.Weighted == nil || len(service.Weighted.Services) != len(wantService.Weighted.Services) {
		return fmt.Errorf("effective weighted service differs from desired route")
	}
	wanted := map[string]int{}
	for _, backend := range wantService.Weighted.Services {
		wanted[backend.Name] = backend.Weight
	}
	for _, backend := range service.Weighted.Services {
		weight, found := wanted[backend.Name]
		if !found || backend.Weight != weight {
			return fmt.Errorf("effective backend weight differs from desired route")
		}
		delete(wanted, backend.Name)
		if upstream, ok := observed.Services[backend.Name]; !ok || upstream.Status != "enabled" {
			return fmt.Errorf("effective backend service is unavailable")
		}
	}
	if len(wanted) != 0 {
		return fmt.Errorf("effective backend set is incomplete")
	}
	return nil
}
