package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

const fleetAppTargetSchema = store.FleetAppTargetSchema

// FleetAppTarget is control-owned configuration. A deployment request cannot
// create or select one; a future authenticated operator path must own writes.
type FleetAppTarget = store.FleetAppTarget

func (s *V3OperationStore) fleetAppTargetKey(app, environment string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + environment))
	return s.prefix + "/v3/fleet-app-targets/" + hex.EncodeToString(sum[:])
}

func validateFleetAppTarget(target FleetAppTarget) error {
	if target.SchemaVersion != fleetAppTargetSchema || target.Generation == 0 || len(target.Datacenters) == 0 || !slices.IsSorted(target.Datacenters) {
		return fmt.Errorf("Fleet app target schema, generation or datacenters are invalid")
	}
	for _, value := range []string{target.App, target.ControlEnvironment, target.Cluster, target.FleetEnvironment, target.Region, target.NomadRegion} {
		if value == "" || len(value) > 256 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t") {
			return fmt.Errorf("Fleet app target identity is invalid")
		}
	}
	for i, dc := range target.Datacenters {
		if dc == "" || len(dc) > 256 || dc != strings.TrimSpace(dc) || strings.ContainsAny(dc, "\x00\r\n\t") || (i > 0 && dc == target.Datacenters[i-1]) {
			return fmt.Errorf("Fleet app target datacenter is invalid")
		}
	}
	return nil
}

func sameFleetAppTarget(a, b FleetAppTarget) bool {
	return a.SchemaVersion == b.SchemaVersion && a.App == b.App && a.ControlEnvironment == b.ControlEnvironment &&
		a.Cluster == b.Cluster && a.FleetEnvironment == b.FleetEnvironment && a.Region == b.Region &&
		a.NomadRegion == b.NomadRegion && a.Generation == b.Generation && slices.Equal(a.Datacenters, b.Datacenters)
}

func (s *V3OperationStore) loadFleetAppTarget(ctx context.Context, app, environment string) (FleetAppTarget, int64, error) {
	key := s.fleetAppTargetKey(app, environment)
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return FleetAppTarget{}, 0, err
	}
	if len(response.Kvs) != 1 {
		return FleetAppTarget{}, 0, fmt.Errorf("Fleet app target is unavailable")
	}
	var target FleetAppTarget
	if err := decodeV3Record(response.Kvs[0].Value, &target); err != nil || validateFleetAppTarget(target) != nil || target.App != app || target.ControlEnvironment != environment {
		return FleetAppTarget{}, 0, fmt.Errorf("Fleet app target record is invalid")
	}
	return target, response.Kvs[0].ModRevision, nil
}

// CurrentFleetAppTarget returns validated, control-owned placement and its
// etcd revision for a server-side deployment admission builder. The later
// admission transaction still compares this target revision atomically.
func (s *V3OperationStore) CurrentFleetAppTarget(ctx context.Context, app, environment string) (FleetAppTarget, int64, error) {
	if s == nil || s.kv == nil || app == "" || environment == "" {
		return FleetAppTarget{}, 0, fmt.Errorf("Fleet app target reader is unavailable")
	}
	return s.loadFleetAppTarget(ctx, app, environment)
}

// putFleetAppTarget is private until an authenticated operator configuration
// path can supply its expected revision. Generation and etcd CAS both advance
// on replacement, so an old acceptance cannot be silently retargeted.
func (s *V3OperationStore) putFleetAppTarget(ctx context.Context, target FleetAppTarget, expectedRevision int64) (int64, error) {
	if err := validateFleetAppTarget(target); err != nil {
		return 0, err
	}
	key := s.fleetAppTargetKey(target.App, target.ControlEnvironment)
	if expectedRevision < 0 {
		return 0, fmt.Errorf("Fleet app target expected revision is invalid")
	}
	if expectedRevision > 0 {
		prior, revision, err := s.loadFleetAppTarget(ctx, target.App, target.ControlEnvironment)
		if err != nil || revision != expectedRevision || target.Generation != prior.Generation+1 {
			return 0, fmt.Errorf("Fleet app target replacement is stale")
		}
	} else if target.Generation != 1 {
		return 0, fmt.Errorf("Fleet app target initial generation must be one")
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return 0, err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", expectedRevision)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return 0, err
	}
	if !txn.Succeeded {
		return 0, fmt.Errorf("Fleet app target changed during replacement")
	}
	return txn.Header.Revision, nil
}

func fleetAppTargetFromSignedRequest(raw []byte) (FleetAppTarget, error) {
	var request struct {
		Semantics struct {
			FleetAppTarget json.RawMessage `json:"fleetAppTarget"`
		} `json:"semantics"`
	}
	if err := json.Unmarshal(raw, &request); err != nil || len(request.Semantics.FleetAppTarget) == 0 {
		return FleetAppTarget{}, fmt.Errorf("signed request lacks Fleet app target")
	}
	var target FleetAppTarget
	decoder := json.NewDecoder(bytes.NewReader(request.Semantics.FleetAppTarget))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil || validateFleetAppTarget(target) != nil {
		return FleetAppTarget{}, fmt.Errorf("signed Fleet app target is invalid")
	}
	return target, nil
}

func (s *V3OperationStore) prepareFleetAppTargetAdmission(ctx context.Context, acceptance store.OperationAcceptance) (FleetAppTarget, clientv3.Cmp, error) {
	if acceptance.Deployment == nil || acceptance.Deployment.Environment == "" || len(acceptance.Regions) != 1 {
		return FleetAppTarget{}, clientv3.Cmp{}, fmt.Errorf("Fleet deployment requires one explicit environment and region")
	}
	canonical, err := store.CanonicalOperationRequest(acceptance)
	if err != nil {
		return FleetAppTarget{}, clientv3.Cmp{}, err
	}
	requested, err := fleetAppTargetFromSignedRequest(canonical)
	if err != nil {
		return FleetAppTarget{}, clientv3.Cmp{}, err
	}
	current, revision, err := s.loadFleetAppTarget(ctx, acceptance.Deployment.App, acceptance.Deployment.Environment)
	if err != nil {
		return FleetAppTarget{}, clientv3.Cmp{}, err
	}
	region := acceptance.Regions[0]
	if !sameFleetAppTarget(requested, current) || region.Name != current.Region || region.NomadRegion != current.NomadRegion || !slices.Equal(region.Datacenters, current.Datacenters) {
		return FleetAppTarget{}, clientv3.Cmp{}, fmt.Errorf("signed Fleet app target differs from control-owned target or placement")
	}
	return current, clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(current.App, current.ControlEnvironment)), "=", revision), nil
}
