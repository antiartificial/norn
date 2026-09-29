package database

import (
	"net/url"
	"strings"
	"testing"
)

// connectionURL must be one token that standard URL parsers decode back to
// the exact catalog identity and secret, for every endpoint form.
func TestConnectionURLRoundTripsIdentityAndSecret(t *testing.T) {
	password := `p@ss:w/rd?#%&='" $\` + "é"
	target := TargetIdentity{Role: "shop_app", Database: "shop.db"}
	cases := []struct {
		endpoint DatabaseEndpoint
		host     string
		port     string
		query    map[string]string
	}{
		{DatabaseEndpoint{Host: "db.internal.example", Port: 25060}, "db.internal.example", "25060", nil},
		{DatabaseEndpoint{Host: "10.0.0.7", Port: 5432}, "10.0.0.7", "5432", nil},
		{DatabaseEndpoint{Host: "2001:db8::1", Port: 5432}, "2001:db8::1", "5432", nil},
		{DatabaseEndpoint{Host: "/var/run/postgresql", Port: 5433}, "localhost", "", map[string]string{"host": "/var/run/postgresql", "port": "5433"}},
	}
	for _, test := range cases {
		raw := connectionURL(test.endpoint, target, password, map[string]string{"sslmode": "disable"}, false)
		if strings.ContainsAny(raw, " \t\r\n'\"$\\`") {
			t.Fatalf("%s: URL is not a single safe token: %q", test.endpoint.Host, raw)
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", test.endpoint.Host, err)
		}
		secret, _ := parsed.User.Password()
		if parsed.Scheme != "postgresql" || parsed.User.Username() != target.Role || secret != password ||
			parsed.Hostname() != test.host || parsed.Port() != test.port || strings.TrimPrefix(parsed.Path, "/") != target.Database {
			t.Fatalf("%s: parsed %+v", test.endpoint.Host, parsed)
		}
		query := parsed.Query()
		if query.Get("sslmode") != "disable" {
			t.Fatalf("%s: sslmode = %q", test.endpoint.Host, query.Get("sslmode"))
		}
		for key, want := range test.query {
			if query.Get(key) != want {
				t.Fatalf("%s: query %s = %q", test.endpoint.Host, key, query.Get(key))
			}
		}
	}
	// Runtime URLs never carry host-local TLS file paths.
	settings := map[string]string{"sslmode": "verify-full", "sslrootcert": "/tmp/private/ca.pem"}
	if raw := connectionURL(DatabaseEndpoint{Host: "db.internal.example", Port: 5432}, target, "", settings, false); strings.Contains(raw, "sslrootcert") {
		t.Fatalf("runtime URL carries a local file: %s", raw)
	}
	if raw := connectionURL(DatabaseEndpoint{Host: "db.internal.example", Port: 5432}, target, "", settings, true); !strings.Contains(raw, "sslrootcert=%2Ftmp%2Fprivate%2Fca.pem") {
		t.Fatalf("local URL lost its CA file: %s", raw)
	}
}

func TestEndpointHostsAreCanonical(t *testing.T) {
	for host, want := range map[string]bool{
		"db.internal.example": true, "10.0.0.7": true, "2001:db8::1": true, "/var/run/postgresql": true,
		"2001:0db8::1": false, "fe80::1%en0": false, "010.0.0.7": false, "[2001:db8::1]": false, "db_internal": false, "/var/../etc": false,
	} {
		if got := validEndpointHost(host); got != want {
			t.Errorf("validEndpointHost(%q) = %v, want %v", host, got, want)
		}
	}
}
