package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// TargetIdentity is the provider/account/state-backend triple that a Fleet
// target fences (docs/v3/fleet-controller/plan.md, §2.2). It is canonicalized
// server-side before it is persisted or hashed into a target ID.
type TargetIdentity struct {
	Provider        string `json:"provider"`
	ProviderAccount string `json:"providerAccount"`
	StateBackend    string `json:"stateBackend"`
}

// CanonicalTarget canonicalizes in and derives its target ID. Provider and
// ProviderAccount are lowercased and trimmed. StateBackend must be an
// absolute URI with no userinfo, query or fragment; its scheme and host are
// lowercased, its path keeps its case, and a trailing "/" is removed (m7).
// The target ID is "tgt_" + hex(sha256(canonical JSON)); any ID a caller
// supplies must match it (checked by the caller, not here).
func CanonicalTarget(in TargetIdentity) (TargetIdentity, string, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if provider == "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target provider is required")
	}
	account := strings.ToLower(strings.TrimSpace(in.ProviderAccount))
	if account == "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target providerAccount is required")
	}
	raw := strings.TrimSpace(in.StateBackend)
	if raw == "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend is not a valid URI: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend must be an absolute URI with a host")
	}
	if parsed.User != nil {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend must not carry userinfo")
	}
	if parsed.RawQuery != "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend must not carry a query")
	}
	if parsed.Fragment != "" {
		return TargetIdentity{}, "", fmt.Errorf("fleet target stateBackend must not carry a fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	// Keep RawPath consistent with Path, or String() would re-escape Path
	// and an encoded "%2F" would collide with a literal "/".
	parsed.RawPath = strings.TrimSuffix(parsed.RawPath, "/")
	canon := TargetIdentity{Provider: provider, ProviderAccount: account, StateBackend: parsed.String()}
	blob, err := json.Marshal(canon)
	if err != nil {
		return TargetIdentity{}, "", err
	}
	sum := sha256.Sum256(blob)
	return canon, "tgt_" + hex.EncodeToString(sum[:]), nil
}

// Alias kinds a target may be resolved by. The proposal's third kind,
// "root:", is dropped in this pass (m6).
const (
	AliasKindCluster     = "cluster"
	AliasKindEnvironment = "environment"
)

// ParseAlias splits alias into its "kind:value" parts and reports whether
// kind is one this pass accepts (cluster or environment) and value is
// non-empty.
func ParseAlias(alias string) (kind, value string, ok bool) {
	i := strings.IndexByte(alias, ':')
	if i < 0 {
		return "", "", false
	}
	kind, value = alias[:i], alias[i+1:]
	if value == "" {
		return "", "", false
	}
	return kind, value, kind == AliasKindCluster || kind == AliasKindEnvironment
}
