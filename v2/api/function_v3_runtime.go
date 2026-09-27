package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/functionruntime"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/secrets"
	"norn/v2/api/startup"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// configureFunctionV3 connects the same runtime resolver to admission and
// execution. The compatibility-named feature flag keeps activation explicit;
// once enabled, all required runtime capabilities must be present before this
// process serves traffic.
func configureFunctionV3(cfg *config.Config, db *store.DB, pipe *pipeline.Pipeline, remote *nomad.Client, sec *secrets.Manager, operations store.OperationStore) (http.HandlerFunc, *worker.ClaimedFunctionInvocationWorker, error) {
	if cfg == nil || !cfg.FunctionV3PreviewEnabled {
		return nil, nil, nil
	}
	if !cfg.PrivateInvocationEnabled || db == nil || db.Pool == nil || pipe == nil || remote == nil || operations == nil {
		return nil, nil, fmt.Errorf("function v3 requires private acceptance, PostgreSQL, pipeline, and Nomad")
	}
	identity, okIdentity := operations.(store.OperationIdentityResolver)
	private, okPrivate := operations.(store.PrivateInvocationStore)
	verifier, okVerifier := operations.(worker.ClaimedFunctionInvocationVerifier)
	if !okIdentity || !okPrivate || !okVerifier {
		return nil, nil, fmt.Errorf("function v3 acceptance capabilities are unavailable")
	}
	keys, err := startup.PrivateInvocationKeyRingFromRuntimeConfig(true, cfg.PrivateInvocationCurrentKeyID, cfg.PrivateInvocationKeys)
	if err != nil {
		return nil, nil, err
	}
	resolver := &functionruntime.Resolver{
		Specs: functionruntime.SpecSourceFunc(func(_ context.Context, app string) (*model.InfraSpec, error) {
			return deployedFunctionSpec(cfg.AppsDir, app)
		}),
		Deployment: functionruntime.DeploymentSourceFunc(func(ctx context.Context, app string) (string, string, error) {
			image, digest, err := db.FunctionDeploymentBinding(ctx, app, cfg.EnvironmentID())
			if err != nil {
				return "", "", functionruntime.ErrUnavailable
			}
			return image, digest, nil
		}),
		Running: remote,
		Delivery: functionruntime.DeliverySourceFunc(func(ctx context.Context, spec *model.InfraSpec) (nomad.DatabaseRevision, error) {
			return pipe.RunningDeliveryRevision(ctx, spec, "global", spec.App)
		}),
		SecretEnv: functionruntime.EnvironmentSourceFunc(func(_ context.Context, app string) (map[string]string, error) {
			if sec == nil {
				return map[string]string{}, nil
			}
			values, err := sec.EnvMap(app)
			if os.IsNotExist(err) {
				return map[string]string{}, nil
			}
			return values, err
		}),
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	claimed := &worker.ClaimedFunctionInvocationWorker{
		Store: db, Attempts: db, Receipts: db, Private: private, Keys: keys,
		Verifier: verifier, Runtime: resolver, Remote: remote,
		ID: fmt.Sprintf("%s:%d:function", host, os.Getpid()), Lease: 90 * time.Second,
	}
	admission := handler.FunctionV3AdmissionHandler(handler.FunctionV3AdmissionConfig{
		OperationStore: operations, IdentityResolver: identity, PrivateStore: private,
		PrivateKeys: keys, InvocationResolver: resolver,
	})
	return admission, claimed, nil
}

func deployedFunctionSpec(appsDir, app string) (*model.InfraSpec, error) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return nil, functionruntime.ErrUnavailable
	}
	for _, spec := range specs {
		if spec.App == app {
			return spec, nil
		}
	}
	return nil, functionruntime.ErrUnavailable
}
