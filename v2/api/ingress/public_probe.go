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
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProbeRenderedRoutePublic resolves the managed public hostname normally and
// requires its bounded response to match the expected endpoint identity.
// One response does not prove load-balancer distribution or traffic weights.
func ProbeRenderedRoutePublic(ctx context.Context, desired RenderedRoute, path, expectedBodySHA256 string, roots *x509.CertPool) error {
	return probeRenderedRoutePublic(ctx, desired, path, expectedBodySHA256, roots, "")
}

// dialAddress is used only by local fixtures; production always uses DNS.
func probeRenderedRoutePublic(ctx context.Context, desired RenderedRoute, path, expectedBodySHA256 string, roots *x509.CertPool, dialAddress string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#") || len(expectedBodySHA256) != 64 {
		return fmt.Errorf("public ingress endpoint probe is incomplete")
	}
	if _, err := hex.DecodeString(expectedBodySHA256); err != nil {
		return fmt.Errorf("expected endpoint digest is invalid")
	}
	if err := validateRenderedRoute(desired); err != nil {
		return fmt.Errorf("invalid desired ingress route: %w", err)
	}
	var document routeDocument
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		return err
	}
	scheme := "http"
	if document.HTTP.Routers[desired.RouterName].TLS != nil {
		scheme = "https"
	}
	requestURL := scheme + "://" + desired.EndpointHost + path
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed.Path != path || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return fmt.Errorf("public ingress endpoint probe path is invalid")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	if dialAddress != "" {
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, dialAddress)
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe public ingress endpoint: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) > 64<<10 {
		return fmt.Errorf("public ingress endpoint did not return a bounded OK response")
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expectedBodySHA256 {
		return fmt.Errorf("public ingress endpoint returned the wrong identity")
	}
	return nil
}
