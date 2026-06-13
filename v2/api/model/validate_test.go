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

func TestValidateSpecWarnsForLocalEndpointOutsideLocalMode(t *testing.T) {
	spec := &InfraSpec{
		App:       "network-app",
		Processes: map[string]Process{"web": {}},
		Endpoints: []Endpoint{{URL: "http://127.0.0.1:8080"}},
	}

	result := ValidateSpecWithOptions(spec, ValidationOptions{NetworkMode: "tailnet"})
	assertFinding(t, result, "endpoints[0].url")
}

func TestValidateSpecWarnsForPublicEndpointInLocalMode(t *testing.T) {
	spec := &InfraSpec{
		App:       "network-app",
		Processes: map[string]Process{"web": {}},
		Endpoints: []Endpoint{{URL: "https://app.example.test"}},
	}

	result := ValidateSpecWithOptions(spec, ValidationOptions{NetworkMode: "local"})
	assertFinding(t, result, "endpoints[0].url")
}

func TestValidateSpecAcceptsLocalEndpointInLocalMode(t *testing.T) {
	spec := &InfraSpec{
		App:       "network-app",
		Processes: map[string]Process{"web": {}},
		Endpoints: []Endpoint{{URL: "http://127.0.0.1:8080"}},
	}

	result := ValidateSpecWithOptions(spec, ValidationOptions{NetworkMode: "local"})
	for _, finding := range result.Findings {
		if finding.Field == "endpoints[0].url" {
			t.Fatalf("unexpected endpoint finding: %+v", finding)
		}
	}
}

func TestValidateSpecAcceptsObjectStorageBuckets(t *testing.T) {
	spec := &InfraSpec{
		App:       "storage-app",
		Processes: map[string]Process{"web": {}},
		Infrastructure: &Infrastructure{
			ObjectStorage: &ObjectStorageInfra{
				Provider: "garage",
				Buckets: []ObjectStorageBucket{
					{Name: "storage-app-media", Access: "readWrite", Env: "MEDIA"},
					{Name: "storage-app-snapshots", Access: "readOnly"},
				},
			},
		},
	}

	result := ValidateSpec(spec)
	if !result.Valid {
		t.Fatalf("expected valid object storage spec, got %+v", result.Findings)
	}
}

func TestValidateSpecRejectsInvalidObjectStorageBuckets(t *testing.T) {
	spec := &InfraSpec{
		App:       "storage-app",
		Processes: map[string]Process{"web": {}},
		Infrastructure: &Infrastructure{
			ObjectStorage: &ObjectStorageInfra{
				Buckets: []ObjectStorageBucket{
					{Name: "Bad_Bucket", Access: "admin"},
					{Name: "Bad_Bucket"},
				},
			},
		},
	}

	result := ValidateSpec(spec)
	assertErrorFinding(t, result, "infrastructure.objectStorage.buckets[0].name")
	assertErrorFinding(t, result, "infrastructure.objectStorage.buckets[0].access")
	assertErrorFinding(t, result, "infrastructure.objectStorage.buckets[1].name")
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

func assertErrorFinding(t *testing.T, result *ValidationResult, field string) {
	t.Helper()
	for _, finding := range result.Findings {
		if finding.Field == field && finding.Severity == "error" {
			return
		}
	}
	t.Fatalf("missing error for %s in %+v", field, result.Findings)
}
