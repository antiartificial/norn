package functionruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/worker"
)

type testSpecs struct {
	spec *model.InfraSpec
	err  error
}

func (s testSpecs) FunctionSpec(context.Context, string) (*model.InfraSpec, error) {
	return s.spec, s.err
}

type testDeployment struct {
	image  string
	digest string
	err    error
}

func (s testDeployment) FunctionDeployment(context.Context, string) (string, string, error) {
	return s.image, s.digest, s.err
}

func deploymentFor(spec *model.InfraSpec) testDeployment {
	digest, _ := model.InfraSpecDigest(spec)
	return testDeployment{image: resolverImage(), digest: digest}
}

type testRunningImage struct{ err error }

func (s testRunningImage) VerifyRunningAppImage(context.Context, *model.InfraSpec, string) error {
	return s.err
}

type testDelivery struct {
	delivery nomad.DatabaseRevision
	err      error
	calls    int
}

func (s *testDelivery) FunctionDatabaseDelivery(context.Context, *model.InfraSpec) (nomad.DatabaseRevision, error) {
	s.calls++
	return s.delivery, s.err
}

type testEnvironment struct {
	values map[string]string
	err    error
}

func (s testEnvironment) FunctionEnvironment(context.Context, string) (map[string]string, error) {
	return s.values, s.err
}

func resolverSpec() *model.InfraSpec {
	return &model.InfraSpec{App: "widgets", Deploy: true, Processes: map[string]model.Process{
		"alpha":  {Command: "alpha", Function: &model.FunctionSpec{}},
		"resize": {Command: "resize", Function: &model.FunctionSpec{}},
	}}
}

func resolverImage() string { return "registry.example/widgets@sha256:" + strings.Repeat("a", 64) }

func TestResolverAdmissionAndClaimedRuntimeShareExactBinding(t *testing.T) {
	spec := resolverSpec()
	appValues := map[string]string{"APP": "one"}
	spec.Env = appValues
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{}, SecretEnv: testEnvironment{values: map[string]string{"SECRET": "two"}}}
	binding, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize")
	if err != nil {
		t.Fatal(err)
	}
	input := worker.FunctionInvocationEffectInput{App: "widgets", Process: binding.Process, SpecDigest: binding.SpecDigest, ImageReference: binding.ImageReference, DatabaseTarget: binding.DatabaseTarget, DatabaseRevision: binding.DatabaseRevision}
	runtime, err := r.ResolveClaimedFunctionInvocationRuntime(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Spec != spec || runtime.ImageReference != resolverImage() || runtime.AppEnv["APP"] != "one" || runtime.SecretEnv["SECRET"] != "two" {
		t.Fatalf("runtime = %+v", runtime)
	}
	appValues["APP"] = "changed"
	if runtime.AppEnv["APP"] != "one" {
		t.Fatal("app environment was not copied")
	}
}

func TestResolverUsesStableFirstFunctionForOmittedProcess(t *testing.T) {
	spec := resolverSpec()
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{}}
	binding, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "")
	if err != nil || binding.Process != "alpha" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

func TestResolverRejectsImageOutsideAcceptedFunctionReferenceGrammar(t *testing.T) {
	spec := resolverSpec()
	deployment := deploymentFor(spec)
	deployment.image = "registry.example/widgets@sha256:" + strings.Repeat("A", 64)
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deployment, Running: testRunningImage{}}
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uppercase digest err=%v", err)
	}
}

func TestResolverRejectsUndeployedSpec(t *testing.T) {
	spec := resolverSpec()
	spec.Deploy = false
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{}}
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("undeployed spec err=%v", err)
	}
}

func TestResolverRejectsSpecWithoutMatchingDeploymentProvenance(t *testing.T) {
	spec := resolverSpec()
	deployment := deploymentFor(spec)
	deployment.digest = ""
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deployment, Running: testRunningImage{}}
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing deployment digest err=%v", err)
	}
	deployment.digest = "sha256:" + strings.Repeat("b", 64)
	r.Deployment = deployment
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("mismatched deployment digest err=%v", err)
	}
}

func TestResolverDefersUnprovenRunningImage(t *testing.T) {
	spec := resolverSpec()
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{err: errors.New("nomad unavailable")}}
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, worker.ErrFunctionRunningImageUnproven) {
		t.Fatalf("unproven running image err=%v", err)
	}
}

func TestResolverRejectsChangedClaimedSpecImageAndDatabaseBinding(t *testing.T) {
	spec := resolverSpec()
	delivery := &testDelivery{}
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{}, Delivery: delivery}
	binding, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize")
	if err != nil {
		t.Fatal(err)
	}
	input := worker.FunctionInvocationEffectInput{App: "widgets", Process: binding.Process, SpecDigest: binding.SpecDigest, ImageReference: binding.ImageReference, DatabaseTarget: binding.DatabaseTarget, DatabaseRevision: binding.DatabaseRevision}
	changed := deploymentFor(spec)
	changed.image = "registry.example/widgets@sha256:" + strings.Repeat("b", 64)
	r.Deployment = changed
	if _, err := r.ResolveClaimedFunctionInvocationRuntime(context.Background(), input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed image err=%v", err)
	}
	r.Deployment = deploymentFor(spec)
	spec.Processes["resize"] = model.Process{Command: "changed", Function: &model.FunctionSpec{}}
	if _, err := r.ResolveClaimedFunctionInvocationRuntime(context.Background(), input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed spec err=%v", err)
	}
}

func TestResolverFailsClosedForMissingDeliveryAndDatabaseEnvironmentConflict(t *testing.T) {
	spec := resolverSpec()
	spec.Databases = []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}
	r := &Resolver{Specs: testSpecs{spec: spec}, Deployment: deploymentFor(spec), Running: testRunningImage{}}
	if _, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing delivery err=%v", err)
	}

	delivery := &testDelivery{delivery: nomad.DatabaseRevision{Revision: 3, Promoted: 3, URLs: map[string]string{"primary": "private"}, Targets: map[string]string{"primary": `{"serviceId":"svc","serviceGeneration":1,"bindingId":"bind","bindingGeneration":1,"engine":"mysql","database":"db","role":"app"}`}}}
	r.Delivery = delivery
	binding, err := r.ResolveFunctionInvocation(context.Background(), "widgets", "resize")
	if err != nil {
		t.Fatal(err)
	}
	input := worker.FunctionInvocationEffectInput{App: "widgets", Process: binding.Process, SpecDigest: binding.SpecDigest, ImageReference: binding.ImageReference, DatabaseTarget: binding.DatabaseTarget, DatabaseRevision: binding.DatabaseRevision}
	spec.Env = map[string]string{"DATABASE_URL": "shadow"}
	input.SpecDigest, _ = model.InfraSpecDigest(spec)
	r.Deployment = deploymentFor(spec)
	if _, err := r.ResolveClaimedFunctionInvocationRuntime(context.Background(), input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("environment conflict err=%v", err)
	}
	if delivery.calls < 2 {
		t.Fatalf("delivery did not revalidate for worker: %d", delivery.calls)
	}
}
