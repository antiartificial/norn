package main

import "testing"

func TestLoadConfigRequiresAllDurableDependencies(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("VALKEY_ADDR", "")
	t.Setenv("KAFKA_BROKERS", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected missing dependency configuration to be rejected")
	}
}

func TestLoadConfigUsesStableDefaultTopic(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("VALKEY_ADDR", "valkey.service.consul:16379")
	t.Setenv("KAFKA_BROKERS", "redpanda.service.consul:19092")
	t.Setenv("PILOT_TOPIC", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.topic != "norn-durability-work" {
		t.Fatalf("topic = %q", cfg.topic)
	}
	if got := cacheKey("job-1"); got != "norn:durability:job:job-1" {
		t.Fatalf("cache key = %q", got)
	}
}
