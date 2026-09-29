package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// NewMutualTLSNodeClient authenticates both ends of private ingress readback.
// The node URL's host must match the server certificate's DNS or IP SAN.
func NewMutualTLSNodeClient(caPEM, certPEM, keyPEM []byte) (*http.Client, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("ingress node server CA is invalid")
	}
	identity, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("ingress node client identity is invalid: %w", err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:       &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// ObservePublishedRenderedRouteWithTLS requires HTTPS on every supplied node
// before making a request. Inventory completeness remains the caller's duty.
func ObservePublishedRenderedRouteWithTLS(ctx context.Context, caPEM, certPEM, keyPEM []byte, nodes []IngressNode, desired RenderedRoute, generation uint64) ([]NodeObservation, error) {
	for _, node := range nodes {
		origin, err := validIngressNodeOrigin(node)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" {
			return nil, fmt.Errorf("ingress node %s requires HTTPS readback", node.ID)
		}
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			return nil, fmt.Errorf("ingress node %s requires a private IP readback endpoint", node.ID)
		}
	}
	client, err := NewMutualTLSNodeClient(caPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return ObservePublishedRenderedRoute(ctx, client, nodes, desired, generation)
}
