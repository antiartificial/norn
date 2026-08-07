package cloudflared

import "testing"

func TestIsPublicEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{"https://vigil.slopistry.com/health", true},
		{"api.example.com", true},
		{"https://mini.tail113139.ts.net:8144", false},
		{"https://vigil.norn", false},
		{"http://service.internal:8080", false},
		{"https://foo.localhost", false},
		{"localhost", false},
		{"127.0.0.1", false},
		{"service", false},
	}
	for _, test := range tests {
		t.Run(test.endpoint, func(t *testing.T) {
			if got := IsPublicEndpoint(test.endpoint); got != test.want {
				t.Fatalf("IsPublicEndpoint(%q) = %t, want %t", test.endpoint, got, test.want)
			}
		})
	}
}
