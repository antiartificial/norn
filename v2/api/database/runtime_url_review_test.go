package database

import (
	"net/url"
	"testing"
)

func TestReviewRuntimeURLPreservesIPv6Authority(t *testing.T) {
	raw := connectionURL(DatabaseEndpoint{Host: "2001:db8::1", Port: 5432}, TargetIdentity{Role: "app", Database: "app"}, "password", map[string]string{"sslmode": "disable"}, false)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("IPv6 endpoint generated an invalid connection URL: %v", err)
	}
	if parsed.Hostname() != "2001:db8::1" || parsed.Port() != "5432" {
		t.Fatal("IPv6 endpoint was not preserved in runtime URL authority")
	}
}
