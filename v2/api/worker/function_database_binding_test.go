package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func functionDatabaseBindingSpec() *model.InfraSpec {
	return &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "widgets", Processes: map[string]model.Process{
		"resize": {Command: "./resize", Function: &model.FunctionSpec{}},
	}, Databases: []model.DatabaseRequirement{
		{Name: "analytics-db", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{Env: "ANALYTICS_DATABASE_URL"}},
		{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}},
	}}
}

func functionDatabaseBindingDelivery(t *testing.T) nomad.DatabaseRevision {
	t.Helper()
	encode := func(target database.TargetIdentity) string {
		bytes, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		return string(bytes)
	}
	return nomad.DatabaseRevision{Revision: 14, Promoted: 14,
		URLs: map[string]string{"analytics_db": "private", "primary": "private"},
		Targets: map[string]string{
			"analytics_db": encode(database.TargetIdentity{ServiceID: "pg-analytics", ServiceGeneration: 3, BindingID: "widgets-analytics", BindingGeneration: 2, Engine: database.EnginePostgreSQL, Database: "analytics", Role: "widgets_app"}),
			"primary":      encode(database.TargetIdentity{ServiceID: "pg-primary", ServiceGeneration: 7, BindingID: "widgets-primary", BindingGeneration: 4, Engine: database.EnginePostgreSQL, Database: "widgets", Role: "widgets_app"}),
		}}
}

func functionDatabaseBindingInput(t *testing.T, spec *model.InfraSpec, delivery nomad.DatabaseRevision) FunctionInvocationEffectInput {
	t.Helper()
	binding, err := NewFunctionInvocationDatabaseBinding(spec, delivery)
	if err != nil {
		t.Fatal(err)
	}
	target, revision, err := binding.Public(delivery)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return FunctionInvocationEffectInput{App: spec.App, Process: "resize", SpecDigest: digest, DatabaseTarget: target, DatabaseRevision: revision}
}

func TestFunctionInvocationDatabaseBindingIsCanonicalAndPublic(t *testing.T) {
	spec, delivery := functionDatabaseBindingSpec(), functionDatabaseBindingDelivery(t)
	binding, err := NewFunctionInvocationDatabaseBinding(spec, delivery)
	if err != nil {
		t.Fatal(err)
	}
	target, revision, err := binding.Public(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if revision != "14" || !strings.Contains(target, `"name":"analytics-db"`) || strings.Index(target, `"name":"analytics-db"`) > strings.Index(target, `"name":"primary"`) {
		t.Fatalf("public database binding = %q, %q", target, revision)
	}
	for _, private := range []string{"postgresql://", "credentialRef", "password", "norn_db_url"} {
		if strings.Contains(target, private) {
			t.Fatalf("public binding leaked private database material %q: %s", private, target)
		}
	}
	if err := RecheckFunctionInvocationDatabaseBinding(functionDatabaseBindingInput(t, spec, delivery), spec, delivery); err != nil {
		t.Fatal(err)
	}
}

func TestFunctionInvocationDatabaseBindingRejectsPinnedSpecAndDeliveryChanges(t *testing.T) {
	for name, change := range map[string]func(FunctionInvocationEffectInput, *model.InfraSpec, nomad.DatabaseRevision) (FunctionInvocationEffectInput, *model.InfraSpec, nomad.DatabaseRevision){
		"changed spec": func(in FunctionInvocationEffectInput, pinned *model.InfraSpec, running nomad.DatabaseRevision) (FunctionInvocationEffectInput, *model.InfraSpec, nomad.DatabaseRevision) {
			pinned.Processes["resize"] = model.Process{Command: "./other", Function: &model.FunctionSpec{}}
			return in, pinned, running
		},
		"changed revision": func(in FunctionInvocationEffectInput, pinned *model.InfraSpec, running nomad.DatabaseRevision) (FunctionInvocationEffectInput, *model.InfraSpec, nomad.DatabaseRevision) {
			running.Revision, running.Promoted = 15, 15
			return in, pinned, running
		},
		"changed target": func(in FunctionInvocationEffectInput, pinned *model.InfraSpec, running nomad.DatabaseRevision) (FunctionInvocationEffectInput, *model.InfraSpec, nomad.DatabaseRevision) {
			var target database.TargetIdentity
			if err := json.Unmarshal([]byte(running.Targets["primary"]), &target); err != nil {
				t.Fatal(err)
			}
			target.BindingGeneration++
			bytes, _ := json.Marshal(target)
			running.Targets["primary"] = string(bytes)
			return in, pinned, running
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec, delivery := functionDatabaseBindingSpec(), functionDatabaseBindingDelivery(t)
			input := functionDatabaseBindingInput(t, spec, delivery)
			in, pinned, running := change(input, spec, delivery)
			if err := RecheckFunctionInvocationDatabaseBinding(in, pinned, running); err == nil {
				t.Fatal("changed public binding was accepted")
			}
		})
	}
}

func TestFunctionInvocationDatabaseBindingRequiresExactCanonicalPayload(t *testing.T) {
	spec, delivery := functionDatabaseBindingSpec(), functionDatabaseBindingDelivery(t)
	input := functionDatabaseBindingInput(t, spec, delivery)
	input.DatabaseTarget = strings.Replace(input.DatabaseTarget, `"schema":`, `"extra":"private","schema":`, 1)
	if err := RecheckFunctionInvocationDatabaseBinding(input, spec, delivery); err == nil {
		t.Fatal("noncanonical target payload was accepted")
	}
}

func TestFunctionInvocationDatabaseBindingUsesNoDatabaseSentinel(t *testing.T) {
	spec := &model.InfraSpec{App: "widgets", Processes: map[string]model.Process{"resize": {Function: &model.FunctionSpec{}}}}
	binding, err := NewFunctionInvocationDatabaseBinding(spec, nomad.DatabaseRevision{})
	if err != nil {
		t.Fatal(err)
	}
	target, revision, err := binding.Public(nomad.DatabaseRevision{})
	if err != nil || target != FunctionInvocationNoDatabase || revision != FunctionInvocationNoDatabase {
		t.Fatalf("database-free public binding = %q, %q, %v", target, revision, err)
	}
	digest, _ := model.InfraSpecDigest(spec)
	input := FunctionInvocationEffectInput{App: "widgets", Process: "resize", SpecDigest: digest, DatabaseTarget: target, DatabaseRevision: revision}
	if err := RecheckFunctionInvocationDatabaseBinding(input, spec, nomad.DatabaseRevision{}); err != nil {
		t.Fatal(err)
	}
	if err := RecheckFunctionInvocationDatabaseBinding(input, spec, nomad.DatabaseRevision{Revision: 1}); err == nil {
		t.Fatal("database-free function accepted delivery material")
	}
}

func TestFunctionInvocationDatabaseBindingRequiresDeclaredTLSFiles(t *testing.T) {
	spec, delivery := functionDatabaseBindingSpec(), functionDatabaseBindingDelivery(t)
	spec.Databases[0].Runtime.TLS = &model.DatabaseRuntimeTLS{CAFileEnv: "DB_CA", ClientCertFileEnv: "DB_CERT", ClientKeyFileEnv: "DB_KEY"}
	delivery.TLS = map[string]string{
		nomad.DatabaseTLSItemKey("analytics-db", "ca"):          "ca-pem",
		nomad.DatabaseTLSItemKey("analytics-db", "client_cert"): "cert-pem",
	}
	if _, err := NewFunctionInvocationDatabaseBinding(spec, delivery); err == nil {
		t.Fatal("missing client key was accepted")
	}
	delivery.TLS[nomad.DatabaseTLSItemKey("analytics-db", "client_key")] = "key-pem"
	if _, err := NewFunctionInvocationDatabaseBinding(spec, delivery); err != nil {
		t.Fatal(err)
	}
}
