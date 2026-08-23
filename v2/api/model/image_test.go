package model

import (
	"strings"
	"testing"
)

func TestIsContentAddressedImage(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, tt := range []struct {
		ref  string
		want bool
	}{
		{ref: "registry.example/app@sha256:" + digest, want: true},
		{ref: "registry.example/app:deadbeef", want: false},
		{ref: "@sha256:" + digest, want: false},
		{ref: "registry.example/app@sha256:short", want: false},
		{ref: "registry.example/app@sha256:" + strings.Repeat("z", 64), want: false},
	} {
		if got := IsContentAddressedImage(tt.ref); got != tt.want {
			t.Errorf("IsContentAddressedImage(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}
