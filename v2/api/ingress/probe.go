package ingress

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RouteProbeNode identifies the exact ingress socket to probe while the
// request retains the managed public Host header and TLS server name.
type RouteProbeNode struct {
	ID          string
	DialAddress string
}

type NodeEndpointProbe struct {
	NodeID     string
	BodySHA256 string
}

// ProbeRenderedRouteNodes verifies an expected bounded response through every
// named ingress socket. It does not check the public load balancer, route
// revision, backend percentage, or whether all Fleet nodes were enumerated.
func ProbeRenderedRouteNodes(ctx context.Context, nodes []RouteProbeNode, desired RenderedRoute, path, expectedBodySHA256 string, roots *x509.CertPool) ([]NodeEndpointProbe, error) {
	if len(nodes) == 0 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#") || len(expectedBodySHA256) != 64 {
		return nil, fmt.Errorf("ingress endpoint probe is incomplete")
	}
	if _, err := hex.DecodeString(expectedBodySHA256); err != nil {
		return nil, fmt.Errorf("expected endpoint digest is invalid")
	}
	if err := validateRenderedRoute(desired); err != nil {
		return nil, fmt.Errorf("invalid desired ingress route: %w", err)
	}
	var document routeDocument
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		return nil, err
	}
	scheme := "http"
	if document.HTTP.Routers[desired.RouterName].TLS != nil {
		scheme = "https"
	}
	requestURL := scheme + "://" + desired.EndpointHost + path
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed.Path != path || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, fmt.Errorf("ingress endpoint probe path is invalid")
	}
	seenIDs := map[string]bool{}
	seenAddresses := map[string]bool{}
	results := make([]NodeEndpointProbe, 0, len(nodes))
	for _, node := range nodes {
		addressHost, rawPort, err := net.SplitHostPort(node.DialAddress)
		port, portErr := strconv.Atoi(rawPort)
		if err != nil || portErr != nil || port < 1 || port > 65535 || net.ParseIP(addressHost) == nil || node.ID == "" || seenIDs[node.ID] || seenAddresses[node.DialAddress] {
			return nil, fmt.Errorf("ingress probe node identity or address is invalid")
		}
		seenIDs[node.ID] = true
		seenAddresses[node.DialAddress] = true
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		transport := &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, node.DialAddress)
			},
			TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		}
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			transport.CloseIdleConnections()
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			transport.CloseIdleConnections()
			return nil, fmt.Errorf("probe ingress node %s: %w", node.ID, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		closeErr := response.Body.Close()
		transport.CloseIdleConnections()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) > 64<<10 {
			return nil, fmt.Errorf("ingress node %s did not return a bounded OK response", node.ID)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != expectedBodySHA256 {
			return nil, fmt.Errorf("ingress node %s returned the wrong endpoint identity", node.ID)
		}
		results = append(results, NodeEndpointProbe{NodeID: node.ID, BodySHA256: expectedBodySHA256})
	}
	return results, nil
}
