package worker

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

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
	env := make(map[string]string, len(appEnv)+len(secretEnv)+len(processEnv))
	for _, source := range []map[string]string{appEnv, secretEnv, processEnv} {
		for name, value := range source {
			if !functionRuntimeEnvName.MatchString(name) || strings.HasPrefix(name, "NORN_REQUEST_") {
				return nil, ErrFunctionRuntimeEnvironment
			}
			env[name] = value
		}
	}
	return json.Marshal(struct {
		Body   string            `json:"body"`
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Env    map[string]string `json:"env"`
	}{Body: request.Body, Method: request.Method, Path: request.Path, Env: env})
}
