package cloudflared

import "testing"

func TestHTTPServiceURL(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    int
		want    string
	}{
		{name: "IPv4", address: "192.168.4.124", port: 8144, want: "http://192.168.4.124:8144"},
		{name: "IPv6", address: "::1", port: 3000, want: "http://[::1]:3000"},
		{name: "hostname", address: "localhost", port: 8800, want: "http://localhost:8800"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTTPServiceURL(tt.address, tt.port); got != tt.want {
				t.Fatalf("HTTPServiceURL(%q, %d) = %q, want %q", tt.address, tt.port, got, tt.want)
			}
		})
	}
}
