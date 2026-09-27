package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ObservePublishedRenderedRoute requires each named node's local publication
// generation and Traefik's effective route to agree. Reading the file on
// both sides of Traefik observation rejects a concurrent local route change.
// The caller must still prove that nodes exactly match durable Fleet inventory,
// probe app endpoints and the public path, and persist the joined evidence.
func ObservePublishedRenderedRoute(ctx context.Context, client *http.Client, nodes []IngressNode, desired RenderedRoute, generation uint64) ([]NodeObservation, error) {
	if client == nil || len(nodes) == 0 || generation == 0 {
		return nil, fmt.Errorf("published ingress observation is incomplete")
	}
	if err := validateRenderedRoute(desired); err != nil {
		return nil, fmt.Errorf("invalid desired ingress route: %w", err)
	}
	seenIDs := map[string]bool{}
	seenOrigins := map[string]bool{}
	results := make([]NodeObservation, 0, len(nodes))
	for _, node := range nodes {
		origin, err := validIngressNodeOrigin(node)
		if err != nil || seenIDs[node.ID] || seenOrigins[origin] {
			return nil, fmt.Errorf("ingress node identity or API origin is invalid or repeated")
		}
		seenIDs[node.ID], seenOrigins[origin] = true, true
		before, err := readNodePublishedRevision(ctx, client, node, desired.RouterName)
		if err != nil {
			return nil, fmt.Errorf("ingress node %s file readback: %w", node.ID, err)
		}
		if before != (PublishedRouteRevision{Generation: generation, RouteSHA256: desired.SHA256, Present: true}) {
			return nil, fmt.Errorf("ingress node %s published route differs from desired generation", node.ID)
		}
		if _, err := ObserveRenderedRoute(ctx, client, []IngressNode{node}, desired); err != nil {
			return nil, err
		}
		after, err := readNodePublishedRevision(ctx, client, node, desired.RouterName)
		if err != nil || after != before {
			return nil, fmt.Errorf("ingress node %s changed during effective-route observation", node.ID)
		}
		results = append(results, NodeObservation{NodeID: node.ID, MatchedDesiredRouteSHA256: desired.SHA256, PublishedGeneration: generation})
	}
	return results, nil
}

func validIngressNodeOrigin(node IngressNode) (string, error) {
	if node.ID == "" {
		return "", fmt.Errorf("node ID is missing")
	}
	endpoint, err := url.Parse(node.APIURL)
	if err != nil || endpoint == nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || endpoint.RawPath != "" || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && net.ParseIP(endpoint.Hostname()) != nil && net.ParseIP(endpoint.Hostname()).IsLoopback())) {
		return "", fmt.Errorf("node API origin is invalid")
	}
	return endpoint.Scheme + "://" + endpoint.Host, nil
}

func readNodePublishedRevision(ctx context.Context, client *http.Client, node IngressNode, routerName string) (PublishedRouteRevision, error) {
	origin, err := validIngressNodeOrigin(node)
	if err != nil || !publishedRouteName.MatchString(routerName) {
		return PublishedRouteRevision{}, fmt.Errorf("route revision request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v1/routes/"+routerName+"/revision", nil)
	if err != nil {
		return PublishedRouteRevision{}, err
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := boundedClient.Do(request)
	if err != nil {
		return PublishedRouteRevision{}, err
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme+"://"+response.Request.URL.Host != origin || response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return PublishedRouteRevision{}, fmt.Errorf("route revision response is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<10)+1))
	if err != nil || len(body) > 4<<10 {
		return PublishedRouteRevision{}, fmt.Errorf("route revision response is not bounded")
	}
	return decodePublishedRevision(body)
}

func decodePublishedRevision(body []byte) (PublishedRouteRevision, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return PublishedRouteRevision{}, fmt.Errorf("route revision is not an object")
	}
	var revision PublishedRouteRevision
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return PublishedRouteRevision{}, fmt.Errorf("route revision has repeated or invalid fields")
		}
		seen[key] = true
		switch key {
		case "generation":
			err = decoder.Decode(&revision.Generation)
		case "routeSHA256":
			err = decoder.Decode(&revision.RouteSHA256)
		case "present":
			err = decoder.Decode(&revision.Present)
		default:
			return PublishedRouteRevision{}, fmt.Errorf("route revision has an unknown field")
		}
		if err != nil {
			return PublishedRouteRevision{}, fmt.Errorf("route revision field is invalid: %w", err)
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || len(seen) != 3 {
		return PublishedRouteRevision{}, fmt.Errorf("route revision fields are incomplete")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return PublishedRouteRevision{}, fmt.Errorf("route revision has trailing data")
	}
	return revision, nil
}
