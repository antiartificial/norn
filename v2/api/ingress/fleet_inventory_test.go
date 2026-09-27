package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func fleetInventoryDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestParseFleetIngressInventoryBindsExactPrivateHostSet(t *testing.T) {
	body := []byte(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"10.43.0.21"},{"name":"ingress-02","privateIP":"10.43.0.22"}],"nodesFileSHA256":"` + strings.Repeat("a", 64) + `","schemaVersion":"norn.fleet-ingress-inventory/v1"}` + "\n")
	nodes, err := ParseFleetIngressInventory(body, fleetInventoryDigest(body), "norn-staging", "staging/nyc3", 18082)
	if err != nil || len(nodes) != 2 || nodes[0] != (IngressNode{ID: "ingress-01", APIURL: "https://10.43.0.21:18082"}) || nodes[1].ID != "ingress-02" {
		t.Fatalf("parsed ingress inventory = %+v, %v", nodes, err)
	}
	if _, err := ParseFleetIngressInventory(body, "sha256:"+strings.Repeat("0", 64), "norn-staging", "staging/nyc3", 18082); err == nil {
		t.Fatal("inventory with another evidence digest accepted")
	}
	if _, err := ParseFleetIngressInventory(body, fleetInventoryDigest(body), "another-cluster", "staging/nyc3", 18082); err == nil {
		t.Fatal("inventory from another cluster accepted")
	}
}

func TestParseFleetIngressInventoryRejectsAmbiguousOrNonPrivateMembership(t *testing.T) {
	base := `{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"10.43.0.21"},{"name":"ingress-02","privateIP":"10.43.0.22"}],"nodesFileSHA256":"` + strings.Repeat("a", 64) + `","schemaVersion":"norn.fleet-ingress-inventory/v1"}` + "\n"
	for name, body := range map[string]string{
		"duplicate field":   strings.Replace(base, `"cluster":"norn-staging",`, `"cluster":"other","cluster":"norn-staging",`, 1),
		"duplicate address": strings.Replace(base, "10.43.0.22", "10.43.0.21", 1),
		"public address":    strings.Replace(base, "10.43.0.22", "203.0.113.10", 1),
		"wrong order":       strings.Replace(base, "ingress-02", "ingress-00", 1),
		"unknown field":     strings.Replace(base, `"schemaVersion":`, `"extra":true,"schemaVersion":`, 1),
		"noncanonical":      strings.Replace(base, `,"environment":`, `, "environment":`, 1),
	} {
		if _, err := ParseFleetIngressInventory([]byte(body), fleetInventoryDigest([]byte(body)), "norn-staging", "staging/nyc3", 18082); err == nil {
			t.Errorf("%s inventory accepted", name)
		}
	}
}
