package connector

import "testing"

func TestHTTPOriginFormatsIPv6(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{address: "127.0.0.1", want: "http://127.0.0.1:3000"},
		{address: "::1", want: "http://[::1]:3000"},
		{address: "2001:db8::1", want: "http://[2001:db8::1]:3000"},
	}
	for _, test := range tests {
		if got := httpOrigin(test.address, 3000); got != test.want {
			t.Fatalf("httpOrigin(%q, 3000) = %q, want %q", test.address, got, test.want)
		}
	}
}
