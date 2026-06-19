package model

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ValidationResult struct {
	App      string              `json:"app"`
	Valid    bool                `json:"valid"`
	Findings []ValidationFinding `json:"findings"`
}

type ValidationFinding struct {
	Severity string `json:"severity"` // error, warning, info
	Field    string `json:"field"`
	Message  string `json:"message"`
}

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var envNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
var kafkaTopicNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type ValidationOptions struct {
	NetworkMode   string
	StrictSecrets bool
}

func ValidateSpec(spec *InfraSpec) *ValidationResult {
	return ValidateSpecWithOptions(spec, ValidationOptions{NetworkMode: "local"})
}

func ValidateSpecWithOptions(spec *InfraSpec, opts ValidationOptions) *ValidationResult {
	r := &ValidationResult{App: spec.App, Valid: true}
	networkMode := normalizeNetworkMode(opts.NetworkMode)
	declaredSecrets := map[string]bool{}
	for _, key := range spec.Secrets {
		declaredSecrets[strings.ToUpper(strings.TrimSpace(key))] = true
	}

	// App name
	if spec.App == "" {
		r.add("error", "name", "app name is required")
	} else if !appNameRe.MatchString(spec.App) {
		r.add("error", "name", "app name must match ^[a-z0-9][a-z0-9-]*$")
	}

	// Processes
	if len(spec.Processes) == 0 {
		r.add("error", "processes", "at least one process is required")
	}

	for name, proc := range spec.Processes {
		field := fmt.Sprintf("processes.%s", name)

		// Port without health check
		if proc.Port > 0 && proc.Health == nil {
			r.add("warning", field+".health", "port defined without health check")
		}

		// Resource bounds
		if proc.Resources != nil {
			if proc.Resources.CPU > 0 && (proc.Resources.CPU < 10 || proc.Resources.CPU > 10000) {
				r.add("warning", field+".resources.cpu", fmt.Sprintf("cpu %d outside recommended range (10-10000 MHz)", proc.Resources.CPU))
			}
			if proc.Resources.Memory > 0 && (proc.Resources.Memory < 32 || proc.Resources.Memory > 8192) {
				r.add("warning", field+".resources.memory", fmt.Sprintf("memory %d outside recommended range (32-8192 MB)", proc.Resources.Memory))
			}
		}

		// Schedule format (5-6 fields)
		if proc.Schedule != "" {
			fields := strings.Fields(proc.Schedule)
			if len(fields) < 5 || len(fields) > 6 {
				r.add("error", field+".schedule", fmt.Sprintf("cron expression should have 5-6 fields, got %d", len(fields)))
			}
			if timezone := strings.TrimSpace(ResolveProcessTimezone(spec, proc)); timezone != "" {
				if _, err := time.LoadLocation(timezone); err != nil {
					r.add("error", field+".timezone", fmt.Sprintf("invalid timezone %q", timezone))
				}
			}
		}

		if proc.Metrics != nil && proc.Metrics.Enabled {
			if proc.Metrics.Path != "" && !strings.HasPrefix(proc.Metrics.Path, "/") {
				r.add("error", field+".metrics.path", "metrics path must start with /")
			}
			if proc.Metrics.Port < 0 || proc.Metrics.Port > 65535 {
				r.add("error", field+".metrics.port", "metrics port must be between 1 and 65535")
			}
			if proc.Metrics.Port == 0 && proc.Port == 0 {
				r.add("error", field+".metrics.port", "metrics requires process port or metrics.port")
			}
		}

		validateTuningPolicy(r, field+".tuning", proc.Tuning)
		validateEnvSecrets(r, field+".env", proc.Env, declaredSecrets, opts.StrictSecrets)
	}

	validateEnvSecrets(r, "env", spec.Env, declaredSecrets, opts.StrictSecrets)

	// Build requires dockerfile
	if spec.Build != nil && spec.Build.Dockerfile == "" {
		r.add("warning", "build.dockerfile", "build block present without dockerfile")
	}

	// Repo requires URL
	if spec.Repo != nil && spec.Repo.URL == "" {
		r.add("error", "repo.url", "repo block present without URL")
	}

	// Endpoint URLs valid
	for i, ep := range spec.Endpoints {
		if ep.URL == "" {
			r.add("error", fmt.Sprintf("endpoints[%d].url", i), "endpoint URL is required")
			continue
		}
		if _, err := url.Parse(ep.URL); err != nil {
			r.add("error", fmt.Sprintf("endpoints[%d].url", i), fmt.Sprintf("invalid URL: %v", err))
			continue
		}
		validateEndpointReachability(r, fmt.Sprintf("endpoints[%d].url", i), ep.URL, networkMode)
	}

	// Volumes
	for i, vol := range spec.Volumes {
		field := fmt.Sprintf("volumes[%d]", i)
		if vol.Name == "" {
			r.add("error", field+".name", "volume name is required")
		}
		if vol.Mount == "" {
			r.add("error", field+".mount", "volume mount path is required")
		} else if !strings.HasPrefix(vol.Mount, "/") {
			r.add("error", field+".mount", "volume mount path must be absolute")
		}
	}

	// Postgres infra requires database name
	if spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil {
		if spec.Infrastructure.Postgres.Database == "" {
			r.add("error", "infrastructure.postgres.database", "postgres database name is required")
		}
	}

	if spec.Infrastructure != nil && spec.Infrastructure.ObjectStorage != nil {
		ospec := spec.Infrastructure.ObjectStorage
		if ospec.Provider != "" && ospec.Provider != "garage" && ospec.Provider != "s3" {
			r.add("warning", "infrastructure.objectStorage.provider", "provider should be garage or s3-compatible")
		}
		if len(ospec.Buckets) == 0 {
			r.add("error", "infrastructure.objectStorage.buckets", "at least one object storage bucket is required")
		}
		seen := map[string]bool{}
		for i, bucket := range ospec.Buckets {
			field := fmt.Sprintf("infrastructure.objectStorage.buckets[%d]", i)
			if bucket.Name == "" {
				r.add("error", field+".name", "bucket name is required")
			} else {
				if !bucketNameRe.MatchString(bucket.Name) {
					r.add("error", field+".name", "bucket name must be DNS-compatible")
				}
				if seen[bucket.Name] {
					r.add("error", field+".name", "bucket name must be unique within the app")
				}
				seen[bucket.Name] = true
			}
			switch bucket.Access {
			case "", "readOnly", "readWrite", "owner":
			default:
				r.add("error", field+".access", "access must be readOnly, readWrite, or owner")
			}
			if bucket.Env != "" && !envNameRe.MatchString(bucket.Env) {
				r.add("error", field+".env", "env alias must contain only letters, numbers, and underscores")
			}
			if bucket.Public {
				r.add("warning", field+".public", "public bucket exposure is declared but not automatically exposed yet")
			}
		}
	}

	if spec.Infrastructure != nil && spec.Infrastructure.Kafka != nil {
		seen := map[string]bool{}
		for i, topic := range spec.Infrastructure.Kafka.Topics {
			field := fmt.Sprintf("infrastructure.kafka.topics[%d]", i)
			topic = strings.TrimSpace(topic)
			if topic == "" {
				r.add("error", field, "topic name is required")
				continue
			}
			if len(topic) > 249 {
				r.add("error", field, "topic name must be 249 characters or fewer")
			}
			if topic == "." || topic == ".." {
				r.add("error", field, "topic name is reserved")
			}
			if !kafkaTopicNameRe.MatchString(topic) {
				r.add("error", field, "topic name must contain only letters, numbers, dots, underscores, or hyphens")
			}
			if seen[topic] {
				r.add("error", field, "topic name must be unique within the app")
			}
			seen[topic] = true
		}
	}

	return r
}

func normalizeNetworkMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tailnet", "tailscale":
		return "tailnet"
	case "public":
		return "public"
	default:
		return "local"
	}
}

func validateEndpointReachability(r *ValidationResult, field, rawURL, networkMode string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	scope := hostScope(endpointHost(rawURL, parsed))
	switch {
	case networkMode != "local" && scope == "local":
		r.add("warning", field, fmt.Sprintf("local endpoint may not be reachable in %s network mode", networkMode))
	case networkMode == "local" && scope == "public":
		r.add("warning", field, "public endpoint in local network mode needs cloudflared/forge routing to be reachable")
	case networkMode == "public" && scope == "private":
		r.add("warning", field, "private endpoint may not be reachable in public network mode")
	}
}

func endpointHost(raw string, parsed *url.URL) string {
	if parsed != nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#") {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return strings.Trim(strings.TrimSpace(host), "[]")
	}
	return strings.TrimSuffix(strings.Trim(strings.TrimSpace(raw), "[]"), ".")
}

func hostScope(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" {
		return "local"
	}
	if strings.HasSuffix(host, ".ts.net") || strings.HasSuffix(host, ".norn") {
		return "private"
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "local"
		case ip.IsPrivate() || isTailnetIP(ip):
			return "private"
		default:
			return "public"
		}
	}
	return "public"
}

func isTailnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64
}

func validateTuningPolicy(r *ValidationResult, field string, tuning *TuningPolicy) {
	if tuning == nil {
		return
	}
	switch tuning.Mode {
	case "", "advisory", "auto":
	default:
		r.add("error", field+".mode", "tuning mode must be advisory or auto")
	}
	if tuning.Cooldown != "" {
		if _, err := time.ParseDuration(tuning.Cooldown); err != nil {
			r.add("error", field+".cooldown", fmt.Sprintf("invalid cooldown duration %q", tuning.Cooldown))
		}
	}
	for name, profile := range tuning.Profiles {
		validateTuningProfile(r, field+".profiles."+name, profile)
	}
	if tuning.Limits != nil {
		validateTuningProfile(r, field+".limits.min", tuning.Limits.Min)
		validateTuningProfile(r, field+".limits.max", tuning.Limits.Max)
		if tuning.Limits.Min.CPU > 0 && tuning.Limits.Max.CPU > 0 && tuning.Limits.Min.CPU > tuning.Limits.Max.CPU {
			r.add("error", field+".limits.cpu", "min cpu must be less than or equal to max cpu")
		}
		if tuning.Limits.Min.Memory > 0 && tuning.Limits.Max.Memory > 0 && tuning.Limits.Min.Memory > tuning.Limits.Max.Memory {
			r.add("error", field+".limits.memory", "min memory must be less than or equal to max memory")
		}
		if tuning.Limits.Min.Scale > 0 && tuning.Limits.Max.Scale > 0 && tuning.Limits.Min.Scale > tuning.Limits.Max.Scale {
			r.add("error", field+".limits.scale", "min scale must be less than or equal to max scale")
		}
	}
	for i, signal := range tuning.Signals {
		signalField := fmt.Sprintf("%s.signals[%d]", field, i)
		switch signal.Source {
		case "", "nomad", "prometheus", "app":
		default:
			r.add("error", signalField+".source", "signal source must be nomad, prometheus, or app")
		}
		if signal.Metric == "" {
			r.add("error", signalField+".metric", "signal metric is required")
		}
		if signal.Window != "" {
			if _, err := time.ParseDuration(signal.Window); err != nil {
				r.add("error", signalField+".window", fmt.Sprintf("invalid window duration %q", signal.Window))
			}
		}
	}
}

func validateTuningProfile(r *ValidationResult, field string, profile TuningProfile) {
	if profile.CPU < 0 {
		r.add("error", field+".cpu", "cpu must be non-negative")
	}
	if profile.Memory < 0 {
		r.add("error", field+".memory", "memory must be non-negative")
	}
	if profile.Scale < 0 {
		r.add("error", field+".scale", "scale must be non-negative")
	}
}

func validateEnvSecrets(r *ValidationResult, field string, env map[string]string, declaredSecrets map[string]bool, strict bool) {
	for key, value := range env {
		if !looksSecretLike(key, value) {
			continue
		}
		secretKey := strings.ToUpper(strings.TrimSpace(key))
		severity := "warning"
		if strict {
			severity = "error"
		}
		if declaredSecrets[secretKey] {
			r.add(severity, field+"."+key, "secret-like value is declared in secrets but also appears in plain env; remove the plaintext env entry")
			continue
		}
		r.add(severity, field+"."+key, "secret-like value should move to secrets.enc.yaml and be listed in secrets")
	}
}

func looksSecretLike(key, value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	secretMarkers := []string{
		"API_KEY",
		"AUTH_TOKEN",
		"CLIENT_SECRET",
		"CREDENTIAL",
		"DATABASE_URL",
		"DB_PASSWORD",
		"DSN",
		"PASSWORD",
		"PRIVATE_KEY",
		"SECRET",
		"TOKEN",
	}
	for _, marker := range secretMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	lowerValue := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lowerValue, "postgres://") ||
		strings.HasPrefix(lowerValue, "mysql://") ||
		strings.HasPrefix(lowerValue, "mongodb://") ||
		strings.HasPrefix(lowerValue, "redis://")
}

func (r *ValidationResult) add(severity, field, message string) {
	if severity == "error" {
		r.Valid = false
	}
	r.Findings = append(r.Findings, ValidationFinding{
		Severity: severity,
		Field:    field,
		Message:  message,
	})
}
