package model

import (
	"fmt"
	"net/url"
	"strings"
)

func ValidateRestrictedIngress(policy *RestrictedIngressSpec, endpoint Endpoint, processes map[string]Process) error {
	if policy == nil {
		return nil
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Port() != "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("restricted ingress requires a bare HTTPS hostname")
	}
	if endpoint.Process == "" {
		return fmt.Errorf("restricted ingress requires an explicit service process")
	}
	proc, ok := processes[endpoint.Process]
	if !ok || proc.Port <= 0 || proc.Schedule != "" || proc.Function != nil {
		return fmt.Errorf("restricted ingress process must be a port-bearing service")
	}
	if len(policy.PublicPaths) == 0 || len(policy.PublicPaths) > 16 {
		return fmt.Errorf("restricted ingress requires 1 to 16 public paths")
	}
	public := map[string]bool{}
	for _, path := range policy.PublicPaths {
		if !validRestrictedPath(path) || public[path] {
			return fmt.Errorf("restricted ingress has an invalid or duplicate public path")
		}
		public[path] = true
	}
	if len(policy.RetryGETPaths) > len(public) {
		return fmt.Errorf("restricted ingress has too many retry paths")
	}
	retried := map[string]bool{}
	for _, path := range policy.RetryGETPaths {
		if !public[path] || retried[path] {
			return fmt.Errorf("restricted ingress retry path must be a unique public path")
		}
		retried[path] = true
	}
	if endpoint.TrafficProbe != nil && !public[endpoint.TrafficProbe.Path] {
		return fmt.Errorf("restricted ingress traffic probe must be a public path")
	}
	if !validRestrictedPath(policy.PrivateReadinessPath) || strings.HasSuffix(policy.PrivateReadinessPath, "/") || public[policy.PrivateReadinessPath] || proc.Health == nil || proc.Health.Path != policy.PrivateReadinessPath {
		return fmt.Errorf("private readiness must be the process health path and outside public routes")
	}
	for path := range public {
		if strings.HasSuffix(path, "/") && strings.HasPrefix(policy.PrivateReadinessPath, path) {
			return fmt.Errorf("public path prefix contains private readiness")
		}
	}
	return nil
}

func validRestrictedPath(path string) bool {
	if len(path) < 2 || len(path) > 256 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "`\\\"'{}()&|?#% ") || strings.Contains(path, "..") {
		return false
	}
	parsed, err := url.Parse(path)
	return err == nil && parsed.Path == path && parsed.RawPath == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
