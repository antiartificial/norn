package config

import (
	"strings"
	"testing"
)

func TestBeaconConfig(t *testing.T) {
	t.Setenv("NORN_BEACON_ENVIRONMENT", "mini")
	t.Setenv("NORN_BEACON_SINK_URL", "https://vigil.example.test/events")
	t.Setenv("NORN_BEACON_SINK_KEY_ID", "norn-mini")
	t.Setenv("NORN_BEACON_SINK_SECRET", "secret")

	cfg := Load()

	if cfg.BeaconEnvironment != "mini" {
		t.Fatalf("BeaconEnvironment = %q, want mini", cfg.BeaconEnvironment)
	}
	if cfg.BeaconSinkURL != "https://vigil.example.test/events" {
		t.Fatalf("BeaconSinkURL = %q", cfg.BeaconSinkURL)
	}
	if cfg.BeaconSinkKeyID != "norn-mini" {
		t.Fatalf("BeaconSinkKeyID = %q", cfg.BeaconSinkKeyID)
	}
	if cfg.BeaconSinkSecret != "secret" {
		t.Fatalf("BeaconSinkSecret = %q", cfg.BeaconSinkSecret)
	}
}

func TestNetworkMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: "local"},
		{name: "local", in: "local", want: "local"},
		{name: "tailnet", in: "tailnet", want: "tailnet"},
		{name: "tailscale alias", in: "tailscale", want: "tailnet"},
		{name: "public", in: "public", want: "public"},
		{name: "unknown", in: "other", want: "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := networkMode(tt.in); got != tt.want {
				t.Fatalf("networkMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedpandaConfig(t *testing.T) {
	t.Setenv("NORN_REDPANDA_BROKERS", "127.0.0.1:9092, redpanda.service.consul:9092")
	t.Setenv("NORN_RPK_PATH", "/opt/redpanda/bin/rpk")

	cfg := Load()

	if got, want := strings.Join(cfg.RedpandaBrokers, ","), "127.0.0.1:9092,redpanda.service.consul:9092"; got != want {
		t.Fatalf("RedpandaBrokers = %q, want %q", got, want)
	}
	if cfg.RedpandaRPKPath != "/opt/redpanda/bin/rpk" {
		t.Fatalf("RedpandaRPKPath = %q", cfg.RedpandaRPKPath)
	}
}
