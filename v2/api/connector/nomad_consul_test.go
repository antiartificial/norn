package connector

import (
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func TestHTTPOriginFormatsIPv6(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{address: "127.0.0.1", want: "http://127.0.0.1:3000"},
		{address: "::1", want: "http://[::1]:3000"},
		{address: "2001:db8::1", want: "http://[2001:db8::1]:3000"},
	}
	for _, test := range tests {
		if got := httpOrigin(test.address, 3000); got != test.want {
			t.Fatalf("httpOrigin(%q, 3000) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestNomadConnectorValidateRejectsMutatedVariableFileTransport(t *testing.T) {
	connector := &NomadConsulConnector{Nomad: &nomad.Client{}}
	err := connector.Validate(&model.InfraSpec{
		App: "orders",
		Processes: map[string]model.Process{
			"web": {NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "DATABASE_URL", Destination: "../escape"}}}},
		},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "invalid Nomad variable-file transport") {
		t.Fatalf("err=%v, want variable transport rejection", err)
	}
}
