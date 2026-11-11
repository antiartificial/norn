package pipeline

import (
	"strings"
	"testing"

	"norn/v2/api/nomad"
)

func TestWorkloadReleasePlanBindsObservedNomadRevisionBeforeResolvingIntent(t *testing.T) {
	intent := workloadReleaseFixture()
	intent.TargetJobVersion = 0
	intent.TargetJobModifyIndex = 0
	plan := WorkloadReleasePlan{SchemaVersion: WorkloadReleasePlanSchema, Intent: intent}
	planDigest, err := plan.Digest()
	if err != nil || planDigest == "" {
		t.Fatalf("plan digest=%q err=%v", planDigest, err)
	}

	observation := nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 8, JobModifyIndex: 43}
	request := workloadReleaseTargetReadbackRequest(intent)
	binding, err := plan.BindObservedTarget("effect-stage-1", 4, request, observation)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.Resolve(binding, "effect-stage-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TargetJobVersion != observation.Version || resolved.TargetJobModifyIndex != observation.JobModifyIndex {
		t.Fatalf("resolved scheduler identity differs from observation: version=%d index=%d", resolved.TargetJobVersion, resolved.TargetJobModifyIndex)
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("resolved intent is not executable: %v", err)
	}
}

func TestWorkloadReleasePlanAcceptsNomadInitialJobVersionZero(t *testing.T) {
	intent := workloadReleaseFixture()
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	plan := WorkloadReleasePlan{SchemaVersion: WorkloadReleasePlanSchema, Intent: intent}
	request := workloadReleaseTargetReadbackRequest(intent)
	observation := nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 0, JobModifyIndex: 7}
	binding, err := plan.BindObservedTarget("effect-stage-1", 2, request, observation)
	if err != nil {
		t.Fatalf("initial Nomad revision binding: %v", err)
	}
	resolved, err := plan.Resolve(binding, "effect-stage-1", 2)
	if err != nil || resolved.TargetJobVersion != 0 || resolved.TargetJobModifyIndex != 7 {
		t.Fatalf("initial Nomad revision did not resolve from its nonzero modify index: intent=%+v err=%v", resolved, err)
	}
}

func TestWorkloadReleasePlanAndStageBindingRejectUntrustedCoordinates(t *testing.T) {
	intent := workloadReleaseFixture()
	intent.TargetJobVersion = 0
	intent.TargetJobModifyIndex = 0
	plan := WorkloadReleasePlan{SchemaVersion: WorkloadReleasePlanSchema, Intent: intent}
	good := nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 8, JobModifyIndex: 43}
	request := workloadReleaseTargetReadbackRequest(intent)
	binding, err := plan.BindObservedTarget("effect-stage-1", 4, request, good)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func() error{
		"caller selected target version in plan": func() error {
			changed := plan
			changed.Intent.TargetJobVersion = 8
			return changed.Validate()
		},
		"missing job is not a binding": func() error {
			_, err := plan.BindObservedTarget("effect-stage-1", 4, request, nomad.DeploymentJobObservation{State: nomad.DeploymentJobNotFound})
			return err
		},
		"missing durable effect identity": func() error {
			_, err := plan.BindObservedTarget("", 4, request, good)
			return err
		},
		"missing claim generation": func() error {
			_, err := plan.BindObservedTarget("effect-stage-1", 0, request, good)
			return err
		},
		"modify request": func() error {
			changed := request
			changed.ExpectedJobModifyIndex = 1
			_, err := plan.BindObservedTarget("effect-stage-1", 4, changed, good)
			return err
		},
		"wrong request target": func() error {
			changed := request
			changed.DeploymentID = "different-target"
			_, err := plan.BindObservedTarget("effect-stage-1", 4, changed, good)
			return err
		},
		"changed plan": func() error {
			changed := plan
			changed.Intent.TargetReplicas++
			_, err := changed.Resolve(binding, "effect-stage-1", 4)
			return err
		},
		"changed observed version": func() error {
			changed := binding
			changed.TargetJobVersion++
			_, err := plan.Resolve(changed, "effect-stage-1", 4)
			return err
		},
		"changed observed modify index": func() error {
			changed := binding
			changed.Observation.JobModifyIndex++
			_, err := plan.Resolve(changed, "effect-stage-1", 4)
			return err
		},
		"changed job digest": func() error {
			changed := binding
			changed.TargetJobDigest = "sha256:" + strings.Repeat("f", 64)
			_, err := plan.Resolve(changed, "effect-stage-1", 4)
			return err
		},
		"changed observation digest": func() error {
			changed := binding
			changed.ObservationDigest = "sha256:" + strings.Repeat("f", 64)
			_, err := plan.Resolve(changed, "effect-stage-1", 4)
			return err
		},
		"wrong effect reservation": func() error {
			_, err := plan.Resolve(binding, "other-effect", 4)
			return err
		},
		"binding from future claim": func() error {
			_, err := plan.Resolve(binding, "effect-stage-1", 3)
			return err
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := test(); err == nil {
				t.Fatal("unsafe staged target was accepted")
			}
		})
	}
}

func workloadReleaseTargetReadbackRequest(intent WorkloadReleaseIntent) nomad.CASDeploymentJobRequest {
	return nomad.CASDeploymentJobRequest{
		App: intent.TargetJobID, Region: intent.NomadRegion, Namespace: intent.Namespace,
		DeploymentID: intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest,
		OperationID: intent.TargetOperationID, ExecutionID: intent.TargetExecutionID,
		JobDigest: strings.TrimPrefix(intent.TargetJobDigest, "sha256:"), ImageTag: intent.TargetImage,
	}
}
