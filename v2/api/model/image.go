package model

import "strings"

// IsContentAddressedImage reports whether ref pins a SHA-256 OCI manifest or
// index digest rather than relying on a mutable registry tag.
func IsContentAddressedImage(ref string) bool {
	const marker = "@sha256:"
	ref = strings.TrimSpace(ref)
	index := strings.LastIndex(ref, marker)
	if index <= 0 {
		return false
	}
	hex := ref[index+len(marker):]
	if len(hex) != 64 {
		return false
	}
	for _, char := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return true
}
