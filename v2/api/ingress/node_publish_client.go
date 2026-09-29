package ingress

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// NodePublicationReceipt records a host's immediate file readback. It is not
// Traefik, endpoint, public-path, or terminal deployment proof.
type NodePublicationReceipt struct {
	NodeID      string
	Generation  uint64
	RouteSHA256 string
}

// ProbePublisherNodesWithTLS checks every active inventory member before a
// deployment job is submitted. This proves reachability and the pinned mTLS
// node identity, not authority for any route or effective traffic.
func ProbePublisherNodesWithTLS(ctx context.Context, caPEM, certPEM, keyPEM []byte, nodes []IngressNode, publisherPort int) error {
	publishers, err := ingressPublisherNodes(nodes, publisherPort)
	if err != nil {
		return err
	}
	client, err := NewMutualTLSNodeClient(caPEM, certPEM, keyPEM)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return probePublisherNodes(ctx, client, publishers)
}

func probePublisherNodes(ctx context.Context, client *http.Client, nodes []IngressNode) error {
	if ctx == nil || client == nil || len(nodes) < 2 {
		return fmt.Errorf("ingress publisher preflight is incomplete")
	}
	origins := make([]string, len(nodes))
	seenID, seenOrigin := make(map[string]bool, len(nodes)), make(map[string]bool, len(nodes))
	for i, node := range nodes {
		origin, err := validIngressNodeOrigin(node)
		if err != nil {
			return err
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || seenID[node.ID] || seenOrigin[origin] {
			return fmt.Errorf("ingress publisher preflight node is invalid or repeated")
		}
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			return fmt.Errorf("ingress publisher preflight requires private IPs")
		}
		seenID[node.ID], seenOrigin[origin] = true, true
		origins[i] = origin
	}
	for i, node := range nodes {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, origins[i]+"/v1/health", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("ingress publisher %s is unavailable: %w", node.ID, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 257))
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || readErr != nil || closeErr != nil || len(body) > 256 || response.Request == nil || response.Request.URL.Scheme+"://"+response.Request.URL.Host != origins[i] {
			return fmt.Errorf("ingress publisher %s failed private preflight", node.ID)
		}
		var health struct {
			NodeID string `json:"nodeId"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&health) != nil || decoder.Decode(new(any)) != io.EOF || health.NodeID != node.ID {
			return fmt.Errorf("ingress publisher %s returned another node identity", node.ID)
		}
	}
	return nil
}

// PublishRouteIntentToNodesWithTLS sends only the reserved intent ID to each
// private ingress publisher. Partial receipts are returned on failure so the
// caller can reconcile hosts that may already be serving the route.
func PublishRouteIntentToNodesWithTLS(ctx context.Context, caPEM, certPEM, keyPEM []byte, nodes []IngressNode, publisherPort int, intentID string, generation uint64, routeSHA256 string) ([]NodePublicationReceipt, error) {
	publishers, err := ingressPublisherNodes(nodes, publisherPort)
	if err != nil {
		return nil, err
	}
	client, err := NewMutualTLSNodeClient(caPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return publishRouteIntentToNodes(ctx, client, publishers, intentID, generation, routeSHA256)
}

// Inventory URLs point at the read-only observer. Publication uses the same
// exact private node identities on a separate, explicitly configured port.
func ingressPublisherNodes(nodes []IngressNode, publisherPort int) ([]IngressNode, error) {
	if publisherPort < 1024 || publisherPort > 65535 {
		return nil, fmt.Errorf("ingress publisher port is invalid")
	}
	publishers := make([]IngressNode, 0, len(nodes))
	for _, node := range nodes {
		origin, err := validIngressNodeOrigin(node)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Port() == "" {
			return nil, fmt.Errorf("ingress publisher requires a private HTTPS inventory origin")
		}
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			return nil, fmt.Errorf("ingress publisher requires a private inventory IP")
		}
		publishers = append(publishers, IngressNode{ID: node.ID, APIURL: "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(publisherPort))})
	}
	return publishers, nil
}

func publishRouteIntentToNodes(ctx context.Context, client *http.Client, nodes []IngressNode, intentID string, generation uint64, routeSHA256 string) ([]NodePublicationReceipt, error) {
	_, digestErr := hex.DecodeString(routeSHA256)
	if client == nil || ctx == nil || len(nodes) < 2 || intentID == "" || len(intentID) > 128 || generation == 0 || len(routeSHA256) != 64 || digestErr != nil {
		return nil, fmt.Errorf("route publication request is incomplete")
	}
	origins := make([]string, len(nodes))
	seen := make(map[string]bool, len(nodes))
	seenOrigin := make(map[string]bool, len(nodes))
	for i, node := range nodes {
		origin, err := validIngressNodeOrigin(node)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			return nil, fmt.Errorf("ingress publisher node is invalid")
		}
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Scheme != "https" || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) || seen[node.ID] || seenOrigin[origin] {
			return nil, fmt.Errorf("ingress publisher node is invalid or repeated")
		}
		seen[node.ID] = true
		seenOrigin[origin] = true
		origins[i] = origin
	}
	body, err := json.Marshal(routeAuthorityRequest{IntentID: intentID})
	if err != nil {
		return nil, err
	}
	receipts := make([]NodePublicationReceipt, 0, len(nodes))
	for i, node := range nodes {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, origins[i]+"/v1/routes/publish", bytes.NewReader(body))
		if err != nil {
			return receipts, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return receipts, fmt.Errorf("ingress publisher %s: %w", node.ID, err)
		}
		limited, readErr := io.ReadAll(io.LimitReader(response.Body, 513))
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || readErr != nil || closeErr != nil || len(limited) > 512 {
			return receipts, fmt.Errorf("ingress publisher %s did not confirm publication: HTTP %d", node.ID, response.StatusCode)
		}
		var receipt struct {
			Generation  uint64 `json:"generation"`
			RouteSHA256 string `json:"routeSHA256"`
		}
		decoder := json.NewDecoder(bytes.NewReader(limited))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || receipt.Generation != generation || receipt.RouteSHA256 != routeSHA256 {
			return receipts, fmt.Errorf("ingress publisher %s returned a mismatched revision", node.ID)
		}
		receipts = append(receipts, NodePublicationReceipt{NodeID: node.ID, Generation: receipt.Generation, RouteSHA256: receipt.RouteSHA256})
	}
	return receipts, nil
}
