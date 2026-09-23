package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func requestIdempotencyKey(cmd *cobra.Command, supplied, prefix string) (string, error) {
	key := strings.TrimSpace(supplied)
	if key == "" {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate idempotency key: %w", err)
		}
		key = prefix + "-" + hex.EncodeToString(random)
	}
	if len(key) > 200 {
		return "", fmt.Errorf("--idempotency-key must not exceed 200 characters")
	}
	// Emit before transport so an ambiguous failure never hides the key the
	// operator must reuse for the retry.
	fmt.Fprintf(cmd.ErrOrStderr(), "idempotency key: %s\n", key)
	return key, nil
}
