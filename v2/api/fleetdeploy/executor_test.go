package fleetdeploy

import (
	"testing"

	"norn/v2/api/ingress"
)

func TestClaimedFleetRouteRequiresExactActiveNodeIdentities(t *testing.T) {
	nodes := []ingress.IngressNode{{ID: "ingress-01", APIURL: "https://10.43.0.21:18082"},
		{ID: "ingress-02", APIURL: "https://10.43.0.22:18082"}}
	configured := map[string]string{"spiffe://norn.test/ingress/one": "ingress-01",
		"spiffe://norn.test/ingress/two": "ingress-02"}
	if err := requireClaimedFleetNodeIdentities(nodes, configured); err != nil {
		t.Fatal(err)
	}
	for name, identities := range map[string]map[string]string{
		"missing node":  {"spiffe://norn.test/ingress/one": "ingress-01"},
		"extra node":    {"spiffe://norn.test/ingress/one": "ingress-01", "spiffe://norn.test/ingress/two": "ingress-02", "spiffe://norn.test/ingress/old": "ingress-old"},
		"repeated node": {"spiffe://norn.test/ingress/one": "ingress-01", "spiffe://norn.test/ingress/two": "ingress-01"},
		"wrong node":    {"spiffe://norn.test/ingress/one": "ingress-01", "spiffe://norn.test/ingress/two": "ingress-other"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := requireClaimedFleetNodeIdentities(nodes, identities); err == nil {
				t.Fatal("stale or incomplete route identities passed preflight")
			}
		})
	}
}
