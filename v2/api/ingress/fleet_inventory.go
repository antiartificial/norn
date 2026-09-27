package ingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
)

var fleetIngressHostName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type fleetIngressSnapshot struct {
	SchemaVersion   string `json:"schemaVersion"`
	Cluster         string `json:"cluster"`
	Environment     string `json:"environment"`
	NodesFileSHA256 string `json:"nodesFileSHA256"`
	IngressNodes    []struct {
		Name      string `json:"name"`
		PrivateIP string `json:"privateIP"`
	} `json:"ingressNodes"`
}

// ParseFleetIngressInventory binds the exact Fleet hook snapshot bytes to a
// separately supplied digest and expected cluster. The caller must obtain that
// digest from durable, authorized Fleet evidence; a caller-supplied digest of
// its own snapshot is not inventory authority.
func ParseFleetIngressInventory(body []byte, expectedDigest, cluster, environment string, port int) ([]IngressNode, error) {
	if len(body) == 0 || len(body) > 64<<10 || len(expectedDigest) != len("sha256:")+64 || port < 1024 || port > 65535 || cluster == "" || environment == "" {
		return nil, fmt.Errorf("Fleet ingress inventory inputs are invalid")
	}
	digest := sha256.Sum256(body)
	if expectedDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return nil, fmt.Errorf("Fleet ingress inventory digest differs from evidence")
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, fmt.Errorf("Fleet ingress inventory is invalid JSON: %w", err)
	}
	canonical, err := json.Marshal(generic)
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return nil, fmt.Errorf("Fleet ingress inventory is not canonical")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var snapshot fleetIngressSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("Fleet ingress inventory schema is invalid: %w", err)
	}
	if snapshot.SchemaVersion != "norn.fleet-ingress-inventory/v1" || snapshot.Cluster != cluster || snapshot.Environment != environment || len(snapshot.NodesFileSHA256) != 64 || len(snapshot.IngressNodes) < 2 {
		return nil, fmt.Errorf("Fleet ingress inventory identity or membership is invalid")
	}
	sourceDigest, err := hex.DecodeString(snapshot.NodesFileSHA256)
	if err != nil || hex.EncodeToString(sourceDigest) != snapshot.NodesFileSHA256 {
		return nil, fmt.Errorf("Fleet ingress inventory source digest is invalid")
	}
	nodes := make([]IngressNode, 0, len(snapshot.IngressNodes))
	seenNames, seenIPs := map[string]bool{}, map[string]bool{}
	previous := ""
	for _, member := range snapshot.IngressNodes {
		ip := net.ParseIP(member.PrivateIP)
		if !fleetIngressHostName.MatchString(member.Name) || member.Name <= previous || seenNames[member.Name] || seenIPs[member.PrivateIP] || ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.String() != member.PrivateIP {
			return nil, fmt.Errorf("Fleet ingress inventory contains an invalid or repeated node")
		}
		previous = member.Name
		seenNames[member.Name], seenIPs[member.PrivateIP] = true, true
		nodes = append(nodes, IngressNode{ID: member.Name, APIURL: "https://" + net.JoinHostPort(member.PrivateIP, strconv.Itoa(port))})
	}
	return nodes, nil
}

// ObserveFleetIngressInventory verifies the complete supplied snapshot over
// private mutual TLS. Digest authority, endpoint and public probes, and
// durable proof persistence remain obligations of the calling control worker.
func ObserveFleetIngressInventory(ctx context.Context, body []byte, expectedDigest, cluster, environment string, port int, caPEM, certPEM, keyPEM []byte, desired RenderedRoute, generation uint64) ([]NodeObservation, error) {
	nodes, err := ParseFleetIngressInventory(body, expectedDigest, cluster, environment, port)
	if err != nil {
		return nil, err
	}
	return ObservePublishedRenderedRouteWithTLS(ctx, caPEM, certPEM, keyPEM, nodes, desired, generation)
}
