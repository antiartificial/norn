package redpanda

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestProvisionAppKafkaCreatesTopicsAndEnv(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rpk.log")
	rpkPath := filepath.Join(dir, "rpk")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + shellQuote(logPath) + "\n"
	if err := os.WriteFile(rpkPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rpk: %v", err)
	}

	client, err := NewClient(Config{
		Brokers: []string{"127.0.0.1:9092", "127.0.0.1:9092, redpanda.service:9092"},
		RPKPath: rpkPath,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	result, err := client.ProvisionAppKafka(context.Background(), "archive", &model.KafkaInfra{
		Topics: []string{"archive-events", "mail.events", "archive-events"},
	})
	if err != nil {
		t.Fatalf("provision kafka: %v", err)
	}

	assertEnv(t, result.Env, "KAFKA_BOOTSTRAP_SERVERS", "127.0.0.1:9092,redpanda.service:9092")
	assertEnv(t, result.Env, "REDPANDA_BROKERS", "127.0.0.1:9092,redpanda.service:9092")
	assertEnv(t, result.Env, "RPK_BROKERS", "127.0.0.1:9092,redpanda.service:9092")
	assertEnv(t, result.Env, "KAFKA_TOPICS", "archive-events,mail.events")
	assertEnv(t, result.Env, "KAFKA_TOPIC_ARCHIVE_EVENTS", "archive-events")
	assertEnv(t, result.Env, "KAFKA_TOPIC_MAIL_EVENTS", "mail.events")

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake rpk log: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	want := "topic\ncreate\n--if-not-exists\narchive-events\nmail.events\n-X\nbrokers=127.0.0.1:9092,redpanda.service:9092"
	if got != want {
		t.Fatalf("rpk args:\n%s\nwant:\n%s", got, want)
	}
}

func TestValidateTopicName(t *testing.T) {
	for _, topic := range []string{"events", "mail.events", "archive-events", "archive_events"} {
		if err := ValidateTopicName(topic); err != nil {
			t.Fatalf("ValidateTopicName(%q): %v", topic, err)
		}
	}
	for _, topic := range []string{"", ".", "..", "bad topic"} {
		if err := ValidateTopicName(topic); err == nil {
			t.Fatalf("ValidateTopicName(%q) unexpectedly succeeded", topic)
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func assertEnv(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	if got := env[key]; got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
