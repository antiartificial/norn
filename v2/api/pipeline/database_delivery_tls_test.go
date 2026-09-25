package pipeline

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type tlsDeliverySecrets map[string][]byte

func (s tlsDeliverySecrets) Resolve(_ context.Context, reference string) ([]byte, error) {
	value, ok := s[reference]
	if !ok {
		return nil, errors.New("missing test material")
	}
	return append([]byte(nil), value...), nil
}

func TestMySQLVerifiedTLSStagesSessionMaterialForNomad(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	resolved := database.ResolvedBinding{
		Target: database.TargetIdentity{ServiceID: "mysql-test", ServiceGeneration: 1, BindingID: "wordpress", BindingGeneration: 1,
			Engine: database.EngineMySQL, Database: "wordpress", Role: "wp"},
		Purpose: database.PurposeApplication, CredentialRef: "secret:mysql/password",
		Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 3306},
		TLS:      database.DatabaseTLS{Mode: database.TLSVerifyFull, ServerName: "127.0.0.1", CARef: "secret:mysql/ca"},
	}
	session, err := database.OpenSession(context.Background(), resolved, tlsDeliverySecrets{
		"secret:mysql/password": []byte(`{"password":"synthetic-password"}`), "secret:mysql/ca": ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	bound := &boundDatabase{session: session, resolved: resolved}
	requirement := model.DatabaseRequirement{Name: "primary", Runtime: &model.DatabaseRuntime{
		Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
		TLS:        &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA"},
	}}
	items, err := databaseConnectionItems(requirement, bound)
	if err != nil {
		t.Fatal(err)
	}
	if items[nomad.DatabaseComponentItemKey("primary", "host")] != "127.0.0.1:3306" ||
		items[nomad.DatabaseComponentItemKey("primary", "user")] != "wp" ||
		items[nomad.DatabaseComponentItemKey("primary", "password")] != "synthetic-password" ||
		items[nomad.DatabaseComponentItemKey("primary", "name")] != "wordpress" ||
		items[nomad.DatabaseTLSItemKey("primary", "ca")] != string(ca) || len(items) != 5 {
		t.Fatal("pipeline did not stage exactly the resolved MySQL connection and CA material")
	}
	missingCA := requirement
	missingCA.Runtime = &model.DatabaseRuntime{Components: requirement.Runtime.Components}
	if _, err := databaseConnectionItems(missingCA, bound); err == nil {
		t.Fatal("verified MySQL target staged without a runtime CA declaration")
	}
}

func TestRunningMySQLTLSRevisionRequiresCAAndRejectsClientMaterial(t *testing.T) {
	target := database.TargetIdentity{ServiceID: "mysql-test", ServiceGeneration: 1, BindingID: "wordpress", BindingGeneration: 1,
		Engine: database.EngineMySQL, Database: "wordpress", Role: "wp"}
	identity, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "wordpress", Databases: []model.DatabaseRequirement{{
		Name: "primary", Runtime: &model.DatabaseRuntime{
			Components: &model.DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME"},
			TLS:        &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA"},
		},
	}}}
	revision := nomad.DatabaseRevision{Revision: 7, Components: map[string]string{}, TLS: map[string]string{
		nomad.DatabaseTLSItemKey("primary", "ca"): "private-ca",
	}, Targets: map[string]string{"primary": string(identity)}}
	for _, field := range []string{"host", "user", "password", "name"} {
		revision.Components[nomad.DatabaseComponentItemKey("primary", field)] = field
	}
	expected := map[string]database.TargetIdentity{"primary": target}
	if err := validateRunningDatabaseRevision(spec, "wordpress", revision, expected); err != nil {
		t.Fatalf("complete TLS revision refused: %v", err)
	}
	delete(revision.TLS, nomad.DatabaseTLSItemKey("primary", "ca"))
	if err := validateRunningDatabaseRevision(spec, "wordpress", revision, expected); err == nil {
		t.Fatal("running revision without CA material was accepted")
	}
	revision.TLS[nomad.DatabaseTLSItemKey("primary", "ca")] = "private-ca"
	revision.TLS[nomad.DatabaseTLSItemKey("primary", "client_key")] = "unqualified-key"
	if err := validateRunningDatabaseRevision(spec, "wordpress", revision, expected); err == nil {
		t.Fatal("running revision with unqualified client key was accepted")
	}
}
