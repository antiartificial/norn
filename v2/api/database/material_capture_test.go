package database

import (
	"strings"
	"testing"

	"norn/v2/api/capture"
)

// A password split by a truncation cut must not survive as a fragment on
// either side of the marker.
func TestRedactCapturedDropsSecretFragmentsAtCuts(t *testing.T) {
	const password = "S3cretCanaryValue"
	session := &Session{password: password}
	for offset := 0; offset <= len(password); offset++ {
		output := capture.New(32, 32)
		// The head ends mid-password; filler is dropped; the tail begins mid-password.
		_, _ = output.Write([]byte(strings.Repeat("h", 32-offset) + password + strings.Repeat("f", 200) + password[offset:] + strings.Repeat("t", 32)))
		redacted := session.RedactCaptured(output)
		for size := 4; size <= len(password); size++ {
			for start := 0; start+size <= len(password); start++ {
				if strings.Contains(redacted, password[start:start+size]) {
					t.Fatalf("offset %d leaked fragment %q in %q", offset, password[start:start+size], redacted)
				}
			}
		}
		if !strings.Contains(redacted, "bytes of output truncated") {
			t.Fatalf("truncation not reported: %q", redacted)
		}
	}
	whole := capture.New(64, 64)
	_, _ = whole.Write([]byte("connect failed for " + password + " at host"))
	if got := session.RedactCaptured(whole); got != "connect failed for [redacted] at host" {
		t.Fatalf("untruncated redaction = %q", got)
	}
}
