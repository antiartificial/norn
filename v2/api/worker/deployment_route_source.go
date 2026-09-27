package worker

import (
	"context"
	"fmt"
	"net/url"
	"slices"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// VerifiedFleetRouteSource comes from the exact InfraSpec pinned by a signed,
// claimed deployment. It is only a source identity; route intent and traffic
// proof still need durable publication and readback.
type VerifiedFleetRouteSource struct {
	App                string
	ControlEnvironment string
	FleetCluster       string
	FleetEnvironment   string
	TargetGeneration   uint64
	OperationID        string
	DeploymentID       string
	AcceptanceID       string
	AcceptanceDigest   string
	Region             string
	NomadRegion        string
	DesiredWeight      int
	Endpoint           string
	Process            string
	Port               int
	SpecDigest         string
}

// VerifyClaimedFleetRouteSource never accepts a caller-selected endpoint or
// process. Fleet route intent must call this after obtaining the claimed source.
func VerifyClaimedFleetRouteSource(ctx context.Context, verifier ClaimedDeploymentVerifier, claimed model.Operation, spec *model.InfraSpec, region string) (VerifiedFleetRouteSource, error) {
	verified, err := VerifyClaimedManagedDeployment(ctx, verifier, claimed, spec)
	if err != nil {
		return VerifiedFleetRouteSource{}, err
	}
	accepted := verified.Accepted
	if accepted.Deployment.ID == "" || accepted.Deployment.Environment == "" || accepted.Intent.ID == "" || accepted.Intent.CanonicalDigest == "" ||
		accepted.Intent.OperationID != claimed.ID || accepted.Intent.DeploymentID != accepted.Deployment.ID ||
		accepted.Operation.Payload["deploymentId"] != accepted.Deployment.ID {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route source lacks a matching signed deployment intent")
	}
	if region == "" {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region is missing")
	}
	var sourceRegion *model.ResolvedRegion
	for _, candidate := range spec.ResolvedRegions() {
		if candidate.Name == region {
			copy := candidate
			sourceRegion = &copy
			break
		}
	}
	if sourceRegion == nil {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region is absent from pinned InfraSpec")
	}
	var acceptedRegion *model.ResolvedRegion
	for _, candidate := range verified.Accepted.Regions {
		if candidate.Name == region {
			if acceptedRegion != nil {
				return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region is repeated in signed placement")
			}
			copy := candidate
			acceptedRegion = &copy
		}
	}
	if acceptedRegion == nil || acceptedRegion.NomadRegion != sourceRegion.NomadRegion || acceptedRegion.TrafficWeight != sourceRegion.TrafficWeight || !slices.Equal(acceptedRegion.Datacenters, sourceRegion.Datacenters) {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region differs from signed placement")
	}
	target := accepted.FleetAppTarget
	if target == nil || target.SchemaVersion != store.FleetAppTargetSchema || target.App != spec.App || target.ControlEnvironment != accepted.Deployment.Environment || target.Region != region ||
		target.Cluster == "" || target.FleetEnvironment == "" || target.Generation == 0 || target.NomadRegion != acceptedRegion.NomadRegion || !slices.Equal(target.Datacenters, acceptedRegion.Datacenters) {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route source lacks a matching signed Fleet target")
	}
	var selected *model.Endpoint
	for i := range spec.Endpoints {
		ep := &spec.Endpoints[i]
		if ep.Region != region {
			continue
		}
		if selected != nil {
			return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route endpoint is ambiguous")
		}
		selected = ep
	}
	if selected == nil || selected.Process == "" {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route endpoint or process binding is missing")
	}
	process, ok := spec.Processes[selected.Process]
	if !ok || process.Port <= 0 || process.Schedule != "" || process.Function != nil {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route process is not a service with a port")
	}
	u, err := url.Parse(selected.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route endpoint must be an HTTPS origin")
	}
	return VerifiedFleetRouteSource{App: spec.App, ControlEnvironment: accepted.Deployment.Environment, FleetCluster: target.Cluster, FleetEnvironment: target.FleetEnvironment, TargetGeneration: target.Generation, OperationID: claimed.ID, DeploymentID: accepted.Deployment.ID,
		AcceptanceID: accepted.Intent.ID, AcceptanceDigest: accepted.Intent.CanonicalDigest,
		Region: region, NomadRegion: acceptedRegion.NomadRegion, DesiredWeight: acceptedRegion.TrafficWeight,
		Endpoint: selected.URL, Process: selected.Process, Port: process.Port, SpecDigest: accepted.Deployment.SpecDigest}, nil
}
