package model

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// TrafficProbeSpec is an exact, public response check signed by the pinned
// InfraSpec. It is optional for Mini endpoints; the first Fleet traffic proof
// requires it and never accepts a worker-supplied substitute.
type TrafficProbeSpec struct {
	Path       string `yaml:"path" json:"path"`
	BodySHA256 string `yaml:"bodySHA256" json:"bodySHA256"`
}

func ValidateTrafficProbe(probe *TrafficProbeSpec) error {
	if probe == nil || len(probe.Path) < 1 || len(probe.Path) > 512 || !strings.HasPrefix(probe.Path, "/") || strings.HasPrefix(probe.Path, "//") || strings.ContainsAny(probe.Path, "?#") || len(probe.BodySHA256) != 64 {
		return fmt.Errorf("traffic probe path or body digest is incomplete")
	}
	parsed, err := url.Parse(probe.Path)
	if err != nil || parsed.Path != probe.Path || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("traffic probe path is invalid")
	}
	digest, err := hex.DecodeString(probe.BodySHA256)
	if err != nil || hex.EncodeToString(digest) != probe.BodySHA256 {
		return fmt.Errorf("traffic probe body digest must be canonical SHA-256")
	}
	return nil
}
