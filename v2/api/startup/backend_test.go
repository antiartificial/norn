package startup

import (
	"bytes"
	"strings"
	"testing"
)

func TestEtcdBackendSelectsNormalRuntime(t *testing.T) {
	env := map[string]string{ControlBackendEnv: BackendEtcd, EtcdEndpointsEnv: "https://127.0.0.1:2379"}
	cfg, err := ParseControlBackend(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireRuntimeCapabilities(cfg); err != nil {
		t.Fatalf("capability error=%v", err)
	}
	var out bytes.Buffer
	handled, err := WriteControlBackendProbe([]string{ControlBackendProbeArgument}, func(k string) string { return env[k] }, &out)
	if !handled || err != nil || !strings.Contains(out.String(), `"backend":"etcd"`) {
		t.Fatalf("handled=%v err=%v output=%q", handled, err, out.String())
	}
}
func TestPostgresBackendProbeDoesNotNeedEtcd(t *testing.T) {
	var out bytes.Buffer
	handled, err := WriteControlBackendProbe([]string{ControlBackendProbeArgument}, func(string) string { return "" }, &out)
	if !handled || err != nil || !strings.Contains(out.String(), `"backend":"postgres"`) {
		t.Fatalf("handled=%v err=%v output=%q", handled, err, out.String())
	}
}

func TestEtcdSourceValidationProbeReportsNarrowMode(t *testing.T) {
	env := map[string]string{
		ControlBackendEnv: BackendEtcd, EtcdEndpointsEnv: "http://127.0.0.1:2379",
		EtcdSourceValidationModeEnv: "true",
	}
	var out bytes.Buffer
	handled, err := WriteControlBackendProbe([]string{ControlBackendProbeArgument}, func(k string) string { return env[k] }, &out)
	if !handled || err != nil || !strings.Contains(out.String(), `"backend":"etcd"`) || !strings.Contains(out.String(), `"sourceValidation":true`) {
		t.Fatalf("handled=%v err=%v output=%q", handled, err, out.String())
	}
}

func TestEtcdSourceValidationProbeRejectsPostgresModes(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"passive": {StartupModeEnv: "passive", SchemaModeEnv: "check"},
		"schema":  {SchemaModeEnv: "check"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{ControlBackendEnv: BackendEtcd, EtcdEndpointsEnv: "http://127.0.0.1:2379", EtcdSourceValidationModeEnv: "true"}
			for key, value := range overrides {
				env[key] = value
			}
			var out bytes.Buffer
			handled, err := WriteControlBackendProbe([]string{ControlBackendProbeArgument}, func(k string) string { return env[k] }, &out)
			if !handled || err == nil || out.Len() != 0 {
				t.Fatalf("handled=%v err=%v output=%q", handled, err, out.String())
			}
		})
	}
}
