package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSpecWarnsForPlainSecretLikeEnv(t *testing.T) {
	spec := &InfraSpec{
		App: "secret-app",
		Processes: map[string]Process{
			"web": {
				Env: map[string]string{
					"OIDC_CLIENT_SECRET": "plain-secret",
				},
			},
		},
		Env: map[string]string{
			"DATABASE_URL": "postgres://user:pass@localhost:5432/app",
		},
	}

	result := ValidateSpec(spec)
	assertFinding(t, result, "env.DATABASE_URL")
	assertFinding(t, result, "processes.web.env.OIDC_CLIENT_SECRET")
}

func TestValidateSpecWarnsWhenDeclaredSecretAlsoAppearsInEnv(t *testing.T) {
	spec := &InfraSpec{
		App:       "secret-app",
		Secrets:   []string{"DATABASE_URL"},
		Processes: map[string]Process{"web": {}},
		Env: map[string]string{
			"DATABASE_URL": "postgres://user:pass@localhost:5432/app",
		},
	}

	result := ValidateSpec(spec)
	assertFinding(t, result, "env.DATABASE_URL")
}

func TestValidateSpecIgnoresDeclaredSecretsWithoutPlainEnv(t *testing.T) {
	spec := &InfraSpec{
		App:       "secret-app",
		Secrets:   []string{"DATABASE_URL"},
		Processes: map[string]Process{"web": {}},
	}

	result := ValidateSpec(spec)
	for _, finding := range result.Findings {
		if finding.Field == "env.DATABASE_URL" {
			t.Fatalf("unexpected finding: %+v", finding)
		}
	}
}

func TestInfraSpecEnvValidatesButDoesNotMarshalToJSON(t *testing.T) {
	spec := &InfraSpec{
		App: "secret-app",
		Processes: map[string]Process{
			"web": {
				Env: map[string]string{
					"OIDC_CLIENT_SECRET": "plain-secret",
				},
			},
		},
		Env: map[string]string{
			"DATABASE_URL": "postgres://user:pass@localhost:5432/app",
		},
	}

	result := ValidateSpec(spec)
	assertFinding(t, result, "env.DATABASE_URL")
	assertFinding(t, result, "processes.web.env.OIDC_CLIENT_SECRET")

	payload, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if strings.Contains(string(payload), "DATABASE_URL") || strings.Contains(string(payload), "OIDC_CLIENT_SECRET") {
		t.Fatalf("env leaked into JSON: %s", payload)
	}
}

func assertFinding(t *testing.T, result *ValidationResult, field string) {
	t.Helper()
	for _, finding := range result.Findings {
		if finding.Field == field && finding.Severity == "warning" {
			return
		}
	}
	t.Fatalf("missing warning for %s in %+v", field, result.Findings)
}
