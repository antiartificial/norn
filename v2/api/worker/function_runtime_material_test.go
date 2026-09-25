package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func TestEncodeFunctionInvocationRuntimeMaterialMergesPrivateEnv(t *testing.T) {
	request := store.PrivateInvocationInput{Body: "line one\nline two", Method: "", Path: "/x?q=é"}
	encoded, err := EncodeFunctionInvocationRuntimeMaterial(request,
		map[string]string{"APP": "app", "SHARED": "app"},
		map[string]string{"SECRET": "private\nquoted \"value\"", "SHARED": "secret"},
		map[string]string{"SHARED": "process", "EMPTY": ""})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Body, Method, Path string
		Env                map[string]string
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	empty, present := got.Env["EMPTY"]
	if got.Body != request.Body || got.Method != request.Method || got.Path != request.Path || got.Env["APP"] != "app" || got.Env["SHARED"] != "process" || got.Env["SECRET"] != "private\nquoted \"value\"" || !present || empty != "" {
		t.Fatalf("runtime material did not preserve precedence and bytes: %+v", got)
	}
}

func TestEncodeFunctionInvocationRuntimeMaterialIncludesDatabaseEnvAndFiles(t *testing.T) {
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "shop", Databases: []model.DatabaseRequirement{{
		Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL", FileEnv: "DATABASE_URL_FILE", Components: &model.DatabaseRuntimeComponents{Host: "DB_HOST", User: "DB_USER", Password: "DB_PASSWORD", Name: "DB_NAME"}, TLS: &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA", ClientCertFileEnv: "MYSQL_SSL_CERT", ClientKeyFileEnv: "MYSQL_SSL_KEY"}},
	}}}
	delivery := nomad.DatabaseRevision{Revision: 7, Promoted: 7,
		URLs: map[string]string{"primary": "mysql://private-url"},
		Components: map[string]string{
			nomad.DatabaseComponentItemKey("primary", "host"): "db.private", nomad.DatabaseComponentItemKey("primary", "user"): "app", nomad.DatabaseComponentItemKey("primary", "password"): "private-password", nomad.DatabaseComponentItemKey("primary", "name"): "shop",
		},
		TLS: map[string]string{
			nomad.DatabaseTLSItemKey("primary", "ca"): "private-ca", nomad.DatabaseTLSItemKey("primary", "client_cert"): "private-cert", nomad.DatabaseTLSItemKey("primary", "client_key"): "private-key",
		},
	}
	encoded, layout, err := EncodeFunctionInvocationRuntimeMaterialWithDatabase(store.PrivateInvocationInput{Body: "private-body"}, map[string]string{"APP": "yes"}, nil, nil, spec, delivery)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Env, Files map[string]string }
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Env["DATABASE_URL"] != delivery.URLs["primary"] || got.Env["DB_PASSWORD"] != "private-password" || got.Files["db_url_primary"] != delivery.URLs["primary"] || got.Files["db_tls_ca_primary"] != "private-ca" || got.Files["db_tls_client_cert_primary"] != "private-cert" || got.Files["db_tls_client_key_primary"] != "private-key" {
		t.Fatalf("database runtime material incomplete: env=%v files=%v", got.Env, got.Files)
	}
	want := []nomad.FunctionInvocationFileLayout{{Key: "db_tls_ca_primary", Env: "MYSQL_SSL_CA"}, {Key: "db_tls_client_cert_primary", Env: "MYSQL_SSL_CERT"}, {Key: "db_tls_client_key_primary", Env: "MYSQL_SSL_KEY"}, {Key: "db_url_primary", Env: "DATABASE_URL_FILE"}}
	if len(layout) != len(want) {
		t.Fatalf("layout = %+v", layout)
	}
	for index := range want {
		if layout[index] != want[index] {
			t.Fatalf("layout = %+v, want %+v", layout, want)
		}
	}
}

func TestEncodeFunctionInvocationRuntimeMaterialRejectsDatabaseEnvConflict(t *testing.T) {
	spec := &model.InfraSpec{Databases: []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}}
	_, _, err := EncodeFunctionInvocationRuntimeMaterialWithDatabase(store.PrivateInvocationInput{}, map[string]string{"DATABASE_URL": "shadow-private"}, nil, nil, spec, nomad.DatabaseRevision{URLs: map[string]string{"primary": "database-private"}})
	if !errors.Is(err, ErrFunctionRuntimeEnvironment) || strings.Contains(err.Error(), "shadow-private") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestEncodeFunctionInvocationRuntimeMaterialRejectsInvalidKeysWithoutValueLeak(t *testing.T) {
	const canary = "private-secret-canary"
	for _, name := range []string{"BAD\nKEY", "NORN_REQUEST_BODY", "9BAD"} {
		_, err := EncodeFunctionInvocationRuntimeMaterial(store.PrivateInvocationInput{}, nil, map[string]string{name: canary}, nil)
		if !errors.Is(err, ErrFunctionRuntimeEnvironment) || strings.Contains(err.Error(), canary) {
			t.Fatalf("invalid environment name %q: %v", name, err)
		}
	}
}
