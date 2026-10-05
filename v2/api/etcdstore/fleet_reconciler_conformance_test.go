package etcdstore

import (
	"testing"

	"norn/v2/api/fleet/fleettest"
)

func TestFleetReconcilerConformanceEtcd(t *testing.T) {
	s := fleetTargetConformanceStore(t)
	fleettest.RunFleetReconcilerConformance(t, &fleetResourceTestHarness{store: s})
}
