// Package functionruntime resolves the public and private state needed by a
// v3 function invocation. Its sources are deliberately narrow so admission
// and the claimed worker use the same policy without either depending on
// handler startup state.
package functionruntime

import (
	"context"
	"errors"
	"sort"
	"strings"

	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/worker"
)

// ErrUnavailable is returned for unavailable, incomplete, or stale runtime
// state. It intentionally carries no source error because sources may handle
// private environment and database material.
var ErrUnavailable = errors.New("function runtime is unavailable")

// SpecSource returns the currently deployed application specification.
type SpecSource interface {
	FunctionSpec(context.Context, string) (*model.InfraSpec, error)
}

// SpecSourceFunc adapts a confined production lookup without broadening this
// package's dependencies to startup configuration or filesystem layout.
type SpecSourceFunc func(context.Context, string) (*model.InfraSpec, error)

func (f SpecSourceFunc) FunctionSpec(ctx context.Context, app string) (*model.InfraSpec, error) {
	return f(ctx, app)
}

// DeploymentSource returns the image and spec digest recorded together when
// the latest deployment completed.
type DeploymentSource interface {
	FunctionDeployment(context.Context, string) (image, specDigest string, err error)
}

// DeploymentSourceFunc adapts a deployment-store lookup.
type DeploymentSourceFunc func(context.Context, string) (image, specDigest string, err error)

func (f DeploymentSourceFunc) FunctionDeployment(ctx context.Context, app string) (string, string, error) {
	return f(ctx, app)
}

// DeliverySource returns the exact promoted database revision used for
// functions. It must revalidate the delivery against the running deployment.
type DeliverySource interface {
	FunctionDatabaseDelivery(context.Context, *model.InfraSpec) (nomad.DatabaseRevision, error)
}

// DeliverySourceFunc adapts the pipeline's running-delivery recheck.
type DeliverySourceFunc func(context.Context, *model.InfraSpec) (nomad.DatabaseRevision, error)

func (f DeliverySourceFunc) FunctionDatabaseDelivery(ctx context.Context, spec *model.InfraSpec) (nomad.DatabaseRevision, error) {
	return f(ctx, spec)
}

// EnvironmentSource returns private secret environment. App environment is
// taken from the exact spec whose digest was checked, avoiding a second mutable
// filesystem read after the public binding check.
type EnvironmentSource interface {
	FunctionEnvironment(context.Context, string) (map[string]string, error)
}

// EnvironmentSourceFunc adapts one explicitly selected private environment
// layer, such as app configuration or the secrets manager.
type EnvironmentSourceFunc func(context.Context, string) (map[string]string, error)

func (f EnvironmentSourceFunc) FunctionEnvironment(ctx context.Context, app string) (map[string]string, error) {
	return f(ctx, app)
}

// Resolver shares production runtime selection between HTTP admission and the
// claimed worker. It is safe to construct before route or worker wiring.
type Resolver struct {
	Specs      SpecSource
	Deployment DeploymentSource
	Delivery   DeliverySource
	SecretEnv  EnvironmentSource
}

var _ handler.FunctionInvocationResolver = (*Resolver)(nil)
var _ worker.ClaimedFunctionInvocationRuntimeResolver = (*Resolver)(nil)

// ResolveFunctionInvocation selects a function process and seals the exact
// public spec, image, and database-delivery binding for durable acceptance.
func (r *Resolver) ResolveFunctionInvocation(ctx context.Context, app, requestedProcess string) (handler.FunctionInvocationResolution, error) {
	spec, process, image, delivery, err := r.resolvePublic(ctx, app, requestedProcess)
	if err != nil {
		return handler.FunctionInvocationResolution{}, err
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		return handler.FunctionInvocationResolution{}, ErrUnavailable
	}
	binding, err := worker.NewFunctionInvocationDatabaseBinding(spec, delivery)
	if err != nil {
		return handler.FunctionInvocationResolution{}, ErrUnavailable
	}
	target, revision, err := binding.Public(delivery)
	if err != nil {
		return handler.FunctionInvocationResolution{}, ErrUnavailable
	}
	return handler.FunctionInvocationResolution{Process: process, SpecDigest: digest, ImageReference: image, DatabaseTarget: target, DatabaseRevision: revision}, nil
}

// ResolveClaimedFunctionInvocationRuntime re-reads every current runtime
// source and accepts it only when it exactly matches the signed public input.
// Environment sources are read only after the public state has been checked.
func (r *Resolver) ResolveClaimedFunctionInvocationRuntime(ctx context.Context, input worker.FunctionInvocationEffectInput) (worker.ClaimedFunctionInvocationRuntime, error) {
	spec, process, image, delivery, err := r.resolvePublic(ctx, input.App, input.Process)
	if err != nil || process != input.Process || image != input.ImageReference {
		return worker.ClaimedFunctionInvocationRuntime{}, ErrUnavailable
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != input.SpecDigest || worker.RecheckFunctionInvocationDatabaseBinding(input, spec, delivery) != nil {
		return worker.ClaimedFunctionInvocationRuntime{}, ErrUnavailable
	}
	appEnv := make(map[string]string, len(spec.Env))
	for key, value := range spec.Env {
		appEnv[key] = value
	}
	secretEnv, err := r.environment(ctx, r.SecretEnv, input.App)
	if err != nil || len(spec.DatabaseEnvConflicts(appEnv, secretEnv, spec.Processes[process].Env)) != 0 {
		return worker.ClaimedFunctionInvocationRuntime{}, ErrUnavailable
	}
	return worker.ClaimedFunctionInvocationRuntime{Spec: spec, ImageReference: image, Database: delivery, AppEnv: appEnv, SecretEnv: secretEnv}, nil
}

func (r *Resolver) resolvePublic(ctx context.Context, app, requestedProcess string) (*model.InfraSpec, string, string, nomad.DatabaseRevision, error) {
	if r == nil || r.Specs == nil || r.Deployment == nil || strings.TrimSpace(app) == "" {
		return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
	}
	spec, err := r.Specs.FunctionSpec(ctx, app)
	if err != nil || spec == nil || spec.App != app || !spec.Deploy {
		return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
	}
	process, err := functionProcess(spec, requestedProcess)
	if err != nil {
		return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
	}
	image, deployedDigest, err := r.Deployment.FunctionDeployment(ctx, app)
	specDigest, digestErr := model.InfraSpecDigest(spec)
	if err != nil || digestErr != nil || deployedDigest != specDigest || !validImageReference(image) {
		return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
	}
	delivery := nomad.DatabaseRevision{}
	if nomad.HasRuntimeDatabases(spec) {
		if r.Delivery == nil {
			return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
		}
		delivery, err = r.Delivery.FunctionDatabaseDelivery(ctx, spec)
		if err != nil {
			return nil, "", "", nomad.DatabaseRevision{}, ErrUnavailable
		}
	}
	return spec, process, image, delivery, nil
}

func validImageReference(image string) bool {
	const marker = "@sha256:"
	if image != strings.TrimSpace(image) || strings.ContainsAny(image, " \t\r\n") || !model.IsContentAddressedImage(image) {
		return false
	}
	digest := image[strings.LastIndex(image, marker)+len(marker):]
	return strings.Trim(digest, "0123456789abcdef") == ""
}

func (r *Resolver) environment(ctx context.Context, source EnvironmentSource, app string) (map[string]string, error) {
	if source == nil {
		return map[string]string{}, nil
	}
	values, err := source.FunctionEnvironment(ctx, app)
	if err != nil {
		return nil, err
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy, nil
}

func functionProcess(spec *model.InfraSpec, requested string) (string, error) {
	if spec == nil {
		return "", ErrUnavailable
	}
	requested = strings.TrimSpace(requested)
	if requested != "" {
		process, ok := spec.Processes[requested]
		if !ok || process.Function == nil {
			return "", ErrUnavailable
		}
		return requested, nil
	}
	functions := make([]string, 0, len(spec.Processes))
	for name, process := range spec.Processes {
		if process.Function != nil {
			functions = append(functions, name)
		}
	}
	if len(functions) == 0 {
		return "", ErrUnavailable
	}
	sort.Strings(functions)
	return functions[0], nil
}
