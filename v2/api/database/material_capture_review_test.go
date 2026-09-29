package database

import (
	"strings"
	"testing"

	"norn/v2/api/capture"
)

func TestReviewCaptureRedactionDoesNotCreateNewSecretCuts(t *testing.T) {
	const password = "S3cretCanaryValue"
	session := &Session{password: password}
	for offset := 0; offset < 64; offset++ {
		output := capture.New(64, 64)
		_, _ = output.Write([]byte(strings.Repeat("h", offset) + password + strings.Repeat("x", 256)))
		got := session.RedactCaptured(output)
		for start := 0; start+4 <= len(password); start++ {
			if strings.Contains(got, password[start:start+4]) {
				t.Fatalf("offset %d retained a credential fragment after trimming: %q", offset, got)
			}
		}
	}
}

func TestReviewCaptureRedactionProtectsBothBoundariesAndEncodedSecrets(t *testing.T) {
	const password = "Secret/with@reserved:value"
	session := &Session{password: password}
	for _, representation := range []string{password, percentEncode(password)} {
		for offset := 0; offset < 96; offset++ {
			for _, input := range []string{
				strings.Repeat("h", offset) + representation + strings.Repeat("x", 384),
				strings.Repeat("x", 384) + representation + strings.Repeat("t", offset),
			} {
				output := capture.New(96, 96)
				_, _ = output.Write([]byte(input))
				got := session.RedactCaptured(output)
				for start := 0; start+6 <= len(representation); start++ {
					if strings.Contains(got, representation[start:start+6]) {
						t.Fatalf("offset %d retained encoded/raw credential fragment", offset)
					}
				}
			}
		}
	}
}
