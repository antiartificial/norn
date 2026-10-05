package store

import (
	"testing"

	"norn/v2/api/fleet/fleettest"
)

func TestFleetReconcilerConformancePostgres(t *testing.T) {
	db := fleetTargetConformanceDB(t)
	fleettest.RunFleetReconcilerConformance(t, &fleetResourceTestHarness{db: db})
}
