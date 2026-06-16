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

func TestValidateSpecStrictSecretsTurnsPlainEnvWarningsIntoErrors(t *testing.T) {
	spec := &InfraSpec{
		App:       "secret-app",
		Processes: map[string]Process{"web": {}},
		Env: map[string]string{
			"DATABASE_URL": "postgres://user:pass@localhost:5432/app",
		},
	}

	result := ValidateSpecWithOptions(spec, ValidationOptions{NetworkMode: "local", StrictSecrets: true})
	if result.Valid {
		t.Fatalf("expected strict secret validation to fail")
	}
	assertErrorFinding(t, result, "env.DATABASE_URL")
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

func TestValidateSpecRejectsInvalidScheduleTimezone(t *testing.T) {
	spec := &InfraSpec{
		App: "bad-cron-timezone",
		Processes: map[string]Process{
			"job": {
				Schedule: "10 8 * * *",
				Timezone: "Mars/Olympus",
			},
		},
	}

	result := ValidateSpec(spec)
	if result.Valid {
		t.Fatal("expected invalid timezone to fail validation")
	}
	assertErrorFinding(t, result, "processes.job.timezone")
}

func TestValidateSpecAcceptsProcessMetrics(t *testing.T) {
	spec := &InfraSpec{
		App: "metrics-app",
		Processes: map[string]Process{
			"web": {
				Port: 8080,
				Metrics: &MetricsSpec{
					Enabled: true,
					Path:    "/metrics",
				},
			},
			"worker": {
				Metrics: &MetricsSpec{
					Enabled: true,
					Path:    "/internal/metrics",
					Port:    9090,
				},
			},
		},
	}

	result := ValidateSpec(spec)
	if !result.Valid {
		t.Fatalf("expected valid metrics spec, got %+v", result.Findings)
	}
}

func TestValidateSpecRejectsInvalidProcessMetrics(t *testing.T) {
	spec := &InfraSpec{
		App: "metrics-app",
		Processes: map[string]Process{
			"worker": {
				Metrics: &MetricsSpec{
					Enabled: true,
					Path:    "metrics",
				},
			},
		},
	}

	result := ValidateSpec(spec)
	assertErrorFinding(t, result, "processes.worker.metrics.path")
	assertErrorFinding(t, result, "processes.worker.metrics.port")
}

func TestValidateSpecAcceptsTuningPolicy(t *testing.T) {
	spec := &InfraSpec{
		App: "tuned-app",
		Processes: map[string]Process{
			"web": {
				Tuning: &TuningPolicy{
					Mode:     "advisory",
					Cooldown: "6h",
					Profiles: map[string]TuningProfile{
						"quiet":  {CPU: 25, Memory: 256, Scale: 1},
						"normal": {CPU: 50, Memory: 512, Scale: 1},
					},
					Limits: &TuningLimits{
						Min: TuningProfile{CPU: 25, Memory: 128, Scale: 1},
						Max: TuningProfile{CPU: 500, Memory: 2048, Scale: 3},
					},
					Signals: []TuningSignal{
						{Source: "nomad", Metric: "memory_rss", Aggregate: "current"},
						{Source: "prometheus", Metric: "container_memory_working_set_bytes", Window: "24h", Aggregate: "p95"},
					},
				},
			},
		},
	}

	result := ValidateSpec(spec)
	if !result.Valid {
		t.Fatalf("expected valid tuning policy, got %+v", result.Findings)
	}
}

func TestValidateSpecRejectsInvalidTuningPolicy(t *testing.T) {
	spec := &InfraSpec{
		App: "bad-tuning-app",
		Processes: map[string]Process{
			"web": {
				Tuning: &TuningPolicy{
					Mode:     "aggressive",
					Cooldown: "soon",
					Limits: &TuningLimits{
						Min: TuningProfile{CPU: 200, Memory: 1024, Scale: 2},
						Max: TuningProfile{CPU: 100, Memory: 512, Scale: 1},
					},
					Signals: []TuningSignal{
						{Source: "mystery"},
					},
				},
			},
		},
	}

	result := ValidateSpec(spec)
	if result.Valid {
		t.Fatal("expected invalid tuning policy to fail validation")
	}
	assertErrorFinding(t, result, "processes.web.tuning.mode")
	assertErrorFinding(t, result, "processes.web.tuning.cooldown")
	assertErrorFinding(t, result, "processes.web.tuning.limits.cpu")
	assertErrorFinding(t, result, "processes.web.tuning.limits.memory")
	assertErrorFinding(t, result, "processes.web.tuning.limits.scale")
	assertErrorFinding(t, result, "processes.web.tuning.signals[0].source")
	assertErrorFinding(t, result, "processes.web.tuning.signals[0].metric")
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

func TestValidateSpecAcceptsKafkaTopics(t *testing.T) {
	spec := &InfraSpec{
		App:       "kafka-app",
		Processes: map[string]Process{"worker": {}},
		Infrastructure: &Infrastructure{
			Kafka: &KafkaInfra{
				Topics: []string{"mail.events", "archive-events", "archive_events"},
			},
		},
	}

	result := ValidateSpec(spec)
	if !result.Valid {
		t.Fatalf("expected valid kafka spec, got %+v", result.Findings)
	}
}

func TestValidateSpecRejectsInvalidKafkaTopics(t *testing.T) {
	spec := &InfraSpec{
		App:       "kafka-app",
		Processes: map[string]Process{"worker": {}},
		Infrastructure: &Infrastructure{
			Kafka: &KafkaInfra{
				Topics: []string{"bad topic", ".", "events", "events"},
			},
		},
	}

	result := ValidateSpec(spec)
	assertErrorFinding(t, result, "infrastructure.kafka.topics[0]")
	assertErrorFinding(t, result, "infrastructure.kafka.topics[1]")
	assertErrorFinding(t, result, "infrastructure.kafka.topics[3]")
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
