package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleetdeploy"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

var fleetReleaseSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// fleetReleaseAdmissionInputs are server-owned or verified values. No field in
// this struct is decoded directly from a deployment request body.
type fleetReleaseAdmissionInputs struct {
	Authority       string
	Actor           store.OperationActor
	IdempotencyKey  string
	Audit           store.AcceptanceAuditContext
	Spec            *model.InfraSpec
	Target          etcdstore.FleetAppTarget
	DatabaseProfile string
	DatabaseCatalog store.DatabaseCatalogRevision
	RegistryURL     string
	SourceSHA       string
	Artifact        string
	Candidate       model.ReleaseCandidate
}

// buildEtcdFleetReleaseAcceptance prepares only the first staging Fleet
// deployment. verify must be the configured release artifact verifier, not an
// HTTP request assertion. The accepting transaction independently compares
// the current Fleet target and active database catalog revisions.
func buildEtcdFleetReleaseAcceptance(ctx context.Context, input fleetReleaseAdmissionInputs,
	verify func(context.Context, *model.InfraSpec, string, string, model.ReleaseCandidate) error,
) (store.OperationAcceptance, error) {
	if verify == nil || input.Spec == nil || !input.Spec.Deploy || input.Spec.App == "" || input.Authority == "" || input.Actor.Issuer == "" || input.Actor.Subject == "" || input.IdempotencyKey == "" {
		return store.OperationAcceptance{}, fmt.Errorf("Fleet release admission inputs or verifier are unavailable")
	}
	if input.Target.App != input.Spec.App || input.Target.ControlEnvironment != "staging" || input.Target.SchemaVersion != store.FleetAppTargetSchema ||
		input.Target.Generation == 0 || input.Target.Cluster == "" || input.Target.FleetEnvironment == "" {
		return store.OperationAcceptance{}, fmt.Errorf("first Fleet release requires a configured staging app target")
	}
	regions := input.Spec.ResolvedRegions()
	if len(regions) != 1 || regions[0].TrafficWeight != 100 || regions[0].Name != input.Target.Region ||
		regions[0].NomadRegion != input.Target.NomadRegion || !slices.Equal(regions[0].Datacenters, input.Target.Datacenters) {
		return store.OperationAcceptance{}, fmt.Errorf("Fleet release placement differs from the configured target")
	}
	if !fleetReleaseSHA.MatchString(input.SourceSHA) || !model.IsContentAddressedImage(input.Artifact) || input.Candidate.Provider == "" {
		return store.OperationAcceptance{}, fmt.Errorf("Fleet release source and candidate must be immutable and verified")
	}
	if err := model.ValidateReleaseSpecBinding(input.Spec, input.Candidate, input.Artifact, input.RegistryURL); err != nil {
		return store.OperationAcceptance{}, err
	}
	if err := verify(ctx, input.Spec, input.SourceSHA, input.Artifact, input.Candidate); err != nil {
		return store.OperationAcceptance{}, fmt.Errorf("verify Fleet release artifact: %w", err)
	}
	databaseTargets, err := fleetdeploy.BindFirstFleetDeploymentDatabases(input.Spec, input.DatabaseProfile, input.DatabaseCatalog)
	if err != nil {
		return store.OperationAcceptance{}, fmt.Errorf("bind first Fleet deployment databases: %w", err)
	}
	specDigest, err := model.InfraSpecDigest(input.Spec)
	if err != nil {
		return store.OperationAcceptance{}, err
	}
	now := time.Now().UTC()
	deploymentID, sagaID := uuid.NewString(), uuid.NewString()
	deployment := &model.Deployment{ID: deploymentID, App: input.Spec.App, CommitSHA: input.SourceSHA, ImageTag: input.Artifact,
		SpecDigest: specDigest, Environment: "staging", SagaID: sagaID, Status: model.StatusQueued,
		SourceKind: "release", SourceRef: input.SourceSHA, StartedAt: now}
	payload := map[string]interface{}{"deploymentId": deploymentID, "app": input.Spec.App, "sourceSha": input.SourceSHA,
		"artifact": input.Artifact, "specDigest": specDigest, "candidate": input.Candidate}
	if databaseTargets != "" {
		payload["databaseTargets"] = databaseTargets
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: input.Spec.App, SagaID: sagaID, Ref: input.SourceSHA,
		Status: model.OperationQueued, Risk: "app rolling update", Source: "release-control-api",
		Message: fmt.Sprintf("queued release deploy for %s", input.Spec.App), StartedAt: now, MaxAttempts: 2,
		Payload: payload, Metadata: map[string]interface{}{"candidate": input.Candidate, "deploymentId": deploymentID,
			"sourceSha": input.SourceSHA, "artifact": input.Artifact, "environment": "staging"}}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: input.Authority, Actor: input.Actor,
		Kind: "app.deploy", Resource: "app/" + input.Spec.App, Key: input.IdempotencyKey}, Operation: operation,
		Deployment: deployment, Regions: regions, Audit: input.Audit,
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Semantics: map[string]interface{}{"fleetAppTarget": input.Target}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return store.OperationAcceptance{}, err
	}
	return acceptance, nil
}
