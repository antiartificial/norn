package worker

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

var (
	ErrFunctionRuntimeEnvironment = errors.New("function runtime environment is invalid")
	functionRuntimeEnvName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// EncodeFunctionInvocationRuntimeMaterial builds the transient private
// variable payload after a worker decrypts the accepted request and resolves
// the pinned app's current secrets. The precedence matches the legacy batch
// translator: app env, then secrets, then process env. Request fields are
// reserved and rendered separately by the closed Nomad template.
func EncodeFunctionInvocationRuntimeMaterial(request store.PrivateInvocationInput, appEnv, secretEnv, processEnv map[string]string) ([]byte, error) {
	encoded, _, err := EncodeFunctionInvocationRuntimeMaterialWithDatabase(request, appEnv, secretEnv, processEnv, nil, nomad.DatabaseRevision{})
	return encoded, err
}

// EncodeFunctionInvocationRuntimeMaterialWithDatabase adds one exact,
// revalidated database delivery to the same opaque invocation payload as the
// request and application environment. The returned file layout is public and
// safe to bind into the closed Nomad job digest; all file bytes stay private.
func EncodeFunctionInvocationRuntimeMaterialWithDatabase(request store.PrivateInvocationInput, appEnv, secretEnv, processEnv map[string]string, spec *model.InfraSpec, delivery nomad.DatabaseRevision) ([]byte, []nomad.FunctionInvocationFileLayout, error) {
	env := make(map[string]string, len(appEnv)+len(secretEnv)+len(processEnv))
	for _, source := range []map[string]string{appEnv, secretEnv, processEnv} {
		for name, value := range source {
			if !functionRuntimeEnvName.MatchString(name) || strings.HasPrefix(name, "NORN_REQUEST_") {
				return nil, nil, ErrFunctionRuntimeEnvironment
			}
			env[name] = value
		}
	}
	files := map[string]string{}
	layout := []nomad.FunctionInvocationFileLayout{}
	if spec == nil && (delivery.Revision != 0 || delivery.Promoted != 0 || len(delivery.URLs)+len(delivery.Components)+len(delivery.TLS)+len(delivery.Targets) != 0) {
		return nil, nil, ErrFunctionRuntimeEnvironment
	}
	if spec != nil {
		if len(spec.DatabaseEnvConflicts(appEnv, secretEnv, processEnv)) != 0 {
			return nil, nil, ErrFunctionRuntimeEnvironment
		}
		for _, requirement := range spec.Databases {
			runtime := requirement.Runtime
			if runtime == nil {
				continue
			}
			name := strings.ReplaceAll(requirement.Name, "-", "_")
			url := delivery.URLs[name]
			if (runtime.Env != "" || runtime.FileEnv != "") && url == "" {
				return nil, nil, ErrFunctionRuntimeEnvironment
			}
			if runtime.Env != "" {
				env[runtime.Env] = url
			}
			if runtime.FileEnv != "" {
				addFunctionRuntimeFile(files, &layout, "db_url_"+name, runtime.FileEnv, url)
			}
			if runtime.Components != nil {
				for _, component := range []struct{ key, env string }{{"host", runtime.Components.Host}, {"user", runtime.Components.User}, {"password", runtime.Components.Password}, {"name", runtime.Components.Name}} {
					value := delivery.Components[nomad.DatabaseComponentItemKey(requirement.Name, component.key)]
					if component.env == "" || value == "" {
						return nil, nil, ErrFunctionRuntimeEnvironment
					}
					env[component.env] = value
				}
			}
			if runtime.TLS != nil {
				for _, material := range []struct{ key, env string }{{"ca", runtime.TLS.CAFileEnv}, {"client_cert", runtime.TLS.ClientCertFileEnv}, {"client_key", runtime.TLS.ClientKeyFileEnv}} {
					if material.env != "" {
						value := delivery.TLS[nomad.DatabaseTLSItemKey(requirement.Name, material.key)]
						if value == "" {
							return nil, nil, ErrFunctionRuntimeEnvironment
						}
						addFunctionRuntimeFile(files, &layout, "db_tls_"+material.key+"_"+name, material.env, value)
					}
				}
			}
		}
	}
	for name := range env {
		if !functionRuntimeEnvName.MatchString(name) || strings.HasPrefix(name, "NORN_REQUEST_") {
			return nil, nil, ErrFunctionRuntimeEnvironment
		}
	}
	sort.Slice(layout, func(i, j int) bool { return layout[i].Key < layout[j].Key })
	encoded, err := json.Marshal(struct {
		Body   string            `json:"body"`
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Env    map[string]string `json:"env"`
		Files  map[string]string `json:"files,omitempty"`
	}{Body: request.Body, Method: request.Method, Path: request.Path, Env: env, Files: files})
	if err != nil {
		return nil, nil, ErrFunctionRuntimeEnvironment
	}
	return encoded, layout, nil
}

func addFunctionRuntimeFile(files map[string]string, layout *[]nomad.FunctionInvocationFileLayout, key, env, value string) {
	files[key] = value
	*layout = append(*layout, nomad.FunctionInvocationFileLayout{Key: key, Env: env})
}
