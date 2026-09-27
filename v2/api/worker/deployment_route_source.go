package worker

import (
	"context"
	"fmt"
	"net/url"

	"norn/v2/api/model"
)

// VerifiedFleetRouteSource comes from the exact InfraSpec pinned by a signed,
// claimed deployment. It is only a source identity; route intent and traffic
// proof still need durable publication and readback.
type VerifiedFleetRouteSource struct {
	App              string
	OperationID      string
	DeploymentID     string
	AcceptanceID     string
	AcceptanceDigest string
	Region           string
	Endpoint         string
	Process          string
	Port             int
	SpecDigest       string
}

// VerifyClaimedFleetRouteSource never accepts a caller-selected endpoint or
// process. Fleet route intent must call this after obtaining the claimed source.
func VerifyClaimedFleetRouteSource(ctx context.Context, verifier ClaimedDeploymentVerifier, claimed model.Operation, spec *model.InfraSpec, region string) (VerifiedFleetRouteSource, error) {
	verified, err := VerifyClaimedManagedDeployment(ctx, verifier, claimed, spec)
	if err != nil {
		return VerifiedFleetRouteSource{}, err
	}
	accepted := verified.Accepted
	if accepted.Deployment.ID == "" || accepted.Intent.ID == "" || accepted.Intent.CanonicalDigest == "" ||
		accepted.Intent.OperationID != claimed.ID || accepted.Intent.DeploymentID != accepted.Deployment.ID ||
		accepted.Operation.Payload["deploymentId"] != accepted.Deployment.ID {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route source lacks a matching signed deployment intent")
	}
	if region == "" {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region is missing")
	}
	regionAccepted := false
	for _, acceptedRegion := range verified.Accepted.Regions {
		if acceptedRegion.Name == region {
			regionAccepted = true
			break
		}
	}
	if !regionAccepted {
		return VerifiedFleetRouteSource{}, fmt.Errorf("fleet route region is not in signed placement")
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
	return VerifiedFleetRouteSource{App: spec.App, OperationID: claimed.ID, DeploymentID: accepted.Deployment.ID,
		AcceptanceID: accepted.Intent.ID, AcceptanceDigest: accepted.Intent.CanonicalDigest,
		Region: region, Endpoint: selected.URL, Process: selected.Process, Port: process.Port, SpecDigest: accepted.Deployment.SpecDigest}, nil
}
