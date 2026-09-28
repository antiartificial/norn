package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestBuildEtcdFleetReleaseAcceptanceUsesServerPlacementAndVerifier(t *testing.T) {
	sha := strings.Repeat("a", 40)
	artifact := "ghcr.io/acme/demo@sha256:" + strings.Repeat("b", 64)
	input := fleetReleaseAdmissionInputs{
		Authority: "control", Actor: store.OperationActor{Issuer: "github-actions", Subject: "run-1"}, IdempotencyKey: "release-1",
		Audit: store.AcceptanceAuditContext{Source: "test"},
		Spec: &model.InfraSpec{App: "demo", Deploy: true, Repo: &model.RepoSpec{URL: "https://github.com/acme/demo"},
			Processes: map[string]model.Process{"web": {Command: "sleep 1", Port: 8080}},
			Endpoints: []model.Endpoint{{URL: "https://demo.example.test", Region: "local", Process: "web",
				TrafficProbe: &model.TrafficProbeSpec{Path: "/ready", BodySHA256: strings.Repeat("d", 64)}}}},
		Target: etcdstore.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "demo", ControlEnvironment: "staging",
			Cluster: "staging-cluster", FleetEnvironment: "staging/local", Region: "local", NomadRegion: "global",
			Datacenters: []string{"dc1"}, Generation: 1},
		RegistryURL: "ghcr.io/acme", SourceSHA: sha, Artifact: artifact,
		Candidate: model.ReleaseCandidate{Provider: "github-actions", Repository: "acme/demo"},
	}
	verified := 0
	verify := func(_ context.Context, spec *model.InfraSpec, gotSHA, gotArtifact string, candidate model.ReleaseCandidate) error {
		verified++
		if spec != input.Spec || gotSHA != sha || gotArtifact != artifact || candidate.Repository != "acme/demo" {
			t.Fatal("artifact verifier received changed source")
		}
		return nil
	}
	acceptance, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify)
	if err != nil {
		t.Fatal(err)
	}
	if verified != 1 || acceptance.Deployment == nil || acceptance.Deployment.SpecDigest == "" ||
		acceptance.Operation.Payload["specDigest"] != acceptance.Deployment.SpecDigest ||
		acceptance.Operation.Payload["databaseTargets"] != nil ||
		!reflect.DeepEqual(acceptance.Semantics["fleetAppTarget"], input.Target) ||
		len(acceptance.Regions) != 1 || acceptance.Regions[0].Name != "local" || acceptance.Regions[0].TrafficWeight != 100 ||
		!acceptance.Admission.OneActiveMutablePerApp || acceptance.Fingerprint.Digest == "" {
		t.Fatalf("incomplete server-derived acceptance: %+v", acceptance)
	}
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, nil); err == nil {
		t.Fatal("missing release verifier admitted deployment")
	}
	input.Spec.Deploy = false
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify); err == nil || verified != 1 {
		t.Fatal("disabled app reached artifact verifier or admission")
	}
	input.Spec.Deploy = true
	input.Spec.Endpoints = nil
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify); err == nil || verified != 1 {
		t.Fatal("missing first-route endpoint reached artifact verifier or admission")
	}
	input.Spec.Endpoints = []model.Endpoint{{URL: "https://demo.example.test", Region: "local", Process: "web"}}
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify); err == nil || verified != 1 {
		t.Fatal("missing signed traffic probe reached artifact verifier or admission")
	}
	input.Spec.Endpoints[0].TrafficProbe = &model.TrafficProbeSpec{Path: "/ready", BodySHA256: strings.Repeat("d", 64)}
	input.Target.Region = "other"
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify); err == nil || verified != 1 {
		t.Fatal("mismatched placement reached artifact verifier or admission")
	}
	input.Target.Region = "local"
	input.Spec.Repo.URL = "https://github.com/another/demo"
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, verify); err == nil || verified != 1 {
		t.Fatal("mismatched source reached artifact verifier or admission")
	}
	input.Spec.Repo.URL = "https://github.com/acme/demo"
	if _, err := buildEtcdFleetReleaseAcceptance(context.Background(), input, func(context.Context, *model.InfraSpec, string, string, model.ReleaseCandidate) error {
		return errors.New("scan denied")
	}); err == nil {
		t.Fatal("failed artifact verification admitted deployment")
	}
}
