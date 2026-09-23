package startup

import (
	"bytes"
	"strings"
	"testing"
)

func TestEtcdBackendFailsBeforeRuntimeFallback(t *testing.T) {
	env := map[string]string{ControlBackendEnv: BackendEtcd, EtcdEndpointsEnv: "https://127.0.0.1:2379"}
	cfg, err := ParseControlBackend(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireRuntimeCapabilities(cfg); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("capability error=%v", err)
	}
	var out bytes.Buffer
	handled, err := WriteControlBackendProbe([]string{ControlBackendProbeArgument}, func(k string) string { return env[k] }, &out)
	if !handled || err == nil || out.Len() != 0 {
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
