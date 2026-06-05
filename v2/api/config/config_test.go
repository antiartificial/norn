package config

import "testing"

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
