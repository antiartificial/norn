// Package fleettest holds the Fleet controller's shared behavioral suites:
// one exported RunFleet*Conformance(t, Harness) function per aggregate
// (target, lifecycle, fence, resource, reconciler), mirroring the existing
// convention in store/storetest. Each backend package then wires its own
// Harness and a top-level TestFleet*Conformance{Postgres,Etcd} test that
// calls the shared function, so the same assertions run once per backend
// instead of being duplicated.
//
// This file is a placeholder: WP1 only reserves the package and the
// convention. The harness types and suite functions land with the WP that
// owns each aggregate (see docs/v3/fleet-controller/plan.md, §3).
package fleettest
